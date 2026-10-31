//go:build linux

package app

import (
	"errors"
	"sync/atomic"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/encryptedbusiness/encryl/reactor"
)

func (s *Server) handleOpen(loop int, slot, gen uint32, acct *connection.Accounting, ip [16]byte, port uint16, family uint8, encoding uint8) {
	st := s.state.Load()
	if st == nil || loop < 0 || loop >= len(st.conns) {
		return
	}
	s.connCounts[int(encoding)].Add(1)
	// Native connections with no open/close lifecycle do not need a public
	// Conn until the first message. This keeps idle connection memory small.
	if !st.connAtOpen && encoding == 0 {
		return
	}
	if acct == nil {
		acct = st.loops[loop].EnsureAccounting(slot, gen)
		if acct == nil {
			return
		}
	}
	c := s.newConn(loop, slot, gen, acct, ip, port, family, encoding)
	st.conns[loop].set(slot, c)

	s.mu.RLock()
	mws := s.connectMW
	s.mu.RUnlock()
	for _, mw := range mws {
		if err := mw(c); err != nil {
			c.closed.Store(true)
			s.closeConn(loop, slot, gen, 1008)
			return
		}
	}
	if ch := s.connectHandler.Load(); ch != nil {
		if err := (*ch)(c); err != nil {
			c.closed.Store(true)
			s.closeConn(loop, slot, gen, 1008)
		}
	} else if lp := s.lifecyclePtr.Load(); lp != nil {
		if err := (*lp).OnOpen(c); err != nil {
			c.closed.Store(true)
			s.closeConn(loop, slot, gen, 1008)
		}
	}
}

func (s *Server) newConn(loop int, slot, gen uint32, acct *connection.Accounting, ip [16]byte, port uint16, family uint8, encoding uint8) *Conn {
	c := &Conn{
		srv:        s,
		loop:       uint16(loop),
		slot:       slot,
		gen:        gen,
		acct:       acct,
		peerIP:     ip,
		peerPort:   port,
		peerFamily: family,
		peerSet:    family != 0,
		encoding:   protocol.Encoding(encoding),
	}
	if c.encoding == protocol.EncodingBebopV1 {
		c.session = protocol.AcquireSession()
	}
	return c
}

func (s *Server) handleClose(loop int, slot, gen uint32, code uint16, reason string, remote bool) {
	st := s.state.Load()
	if st == nil || loop < 0 || loop >= len(st.conns) {
		return
	}
	c := st.conns[loop].get(slot)
	if c == nil {
		// A native idle connection has no public handle. Its encoding count is
		// still balanced here.
		s.connCounts[0].Add(-1)
		return
	}
	if c.gen != gen {
		return
	}
	c.closed.Store(true)
	st.conns[loop].clear(slot)
	s.rooms.RemoveConn(c.ref())

	if dh := s.disconnectHandler.Load(); dh != nil {
		(*dh)(c)
	} else if lp := s.lifecyclePtr.Load(); lp != nil {
		(*lp).OnClose(c, CloseInfo{Code: code, Reason: reason, Remote: remote})
	}
	s.connCounts[int(c.encoding)].Add(-1)
	if c.session != nil {
		protocol.ReleaseSession(c.session)
		c.session = nil
	}
}

func (s *Server) handleMessage(loop int, slot, gen uint32, payload []byte) {
	st := s.state.Load()
	if st == nil || loop < 0 || loop >= len(st.conns) {
		return
	}
	c := st.conns[loop].get(slot)
	if c == nil {
		meta, ok := st.loops[loop].ConnMeta(slot, gen)
		if !ok {
			return
		}
		acct := meta.Acct
		if acct == nil {
			acct = st.loops[loop].EnsureAccounting(slot, gen)
			if acct == nil {
				return
			}
		}
		c = s.newConn(loop, slot, gen, acct, meta.IP, meta.Port, meta.Family, meta.Encoding)
		st.conns[loop].set(slot, c)
	}
	if c.gen != gen || c.closed.Load() {
		return
	}

	if c.encoding != protocol.EncodingNative {
		s.handleProtocolMessage(c, payload)
		return
	}

	e, err := event.Decode(payload)
	if err != nil {
		s.closeConn(loop, slot, gen, 1002)
		return
	}
	m := Message{Event: e.Event, RequestID: e.RequestID, Payload: e.Payload, Binary: true}

	switch e.Kind {
	case event.KindEvent:
		c.replied = false
		var lc Handler
		if lp := s.lifecyclePtr.Load(); lp != nil {
			lc = *lp
		}

		h, found := s.events.Lookup(e.Event)
		var herr error
		if found {
			herr = h(c, m)
		} else if lc != nil {
			herr = lc.OnMessage(c, m)
			found = true
		}
		if !found {
			// v1/Socket.IO behavior: unknown events are dropped. A request
			// still gets an error so the caller cannot hang.
			s.recordRejection(e.Event)
			if e.RequestID != 0 {
				c.sendError(m, event.ErrCodeUnknownEvent, "unknown event")
			}
			return
		}
		if herr != nil {
			s.recordRejection(e.Event)
			c.sendError(m, errorCode(herr), herr.Error())
			return
		}
		if e.RequestID != 0 && e.Flags&event.FlagNoReply == 0 && !c.replied {
			c.sendACK(m)
		}
	case event.KindAck:
		// The token is peer-supplied. Consume the entry only when the
		// completing connection is the one that created it, so a guessed
		// token cannot run another connection's callback or destroy its
		// pending entry.
		entry, ok := s.pending.Take(e.RequestID, uint16(loop), slot, gen)
		if !ok {
			s.staleAcks.Add(1)
			return
		}
		if loop < len(st.loops) && st.loops[loop] != nil {
			st.loops[loop].CancelAck(entry.Slot)
		}
		if entry.Callback != nil {
			entry.Callback(event.Ack{Event: entry.Event, Payload: e.Payload})
		}
	case event.KindAuth:
		// v2 trusts the proxy; AUTH is a no-op until authentication exists.
	case event.KindControl:
		if len(e.Payload) > 0 && e.Payload[0] == 2 {
			s.closeConn(loop, slot, gen, 1001)
		}
	case event.KindError:
		s.closeConn(loop, slot, gen, 1002)
	}
}

func (s *Server) handleAckTimeout(ackSlot, ackGen uint32) {
	entry, ok := s.pending.Timeout(ackSlot, ackGen)
	if !ok || entry.Callback == nil {
		return
	}
	entry.Callback(event.Ack{Event: entry.Event, Err: ErrAckTimeout})
}

func (s *Server) closeConn(loop int, slot, gen uint32, code uint16) {
	_ = s.enqueue(loop, reactor.Command{
		Kind: reactor.CmdClose,
		Slot: slot,
		Gen:  gen,
		A:    uint64(code),
	})
}

func errorCode(err error) uint16 {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return event.ErrCodeUnauthorized
	case errors.Is(err, ErrPayloadTooLarge):
		return event.ErrCodePayloadTooLarge
	case errors.Is(err, ErrBackpressure):
		return event.ErrCodeBackpressure
	default:
		return event.ErrCodeInternal
	}
}

func (s *Server) recordRejection(eventID uint32) {
	v, _ := s.rejected.LoadOrStore(eventID, new(atomic.Uint64))
	v.Add(1)
}
