//go:build linux

package reactor

import (
	"encoding/binary"
	"errors"
	"net/url"
	"strings"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	upgrade "github.com/encryptedbusiness/encryl/httpx"
	"github.com/encryptedbusiness/encryl/ws"
)

func (l *loop) originAllowed(req upgrade.Request) bool {
	if !req.HasOrigin {
		return true
	}
	origin := string(req.Origin)
	if len(l.cfg.AllowedOrigins) == 0 {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, string(req.Host))
	}
	for _, allowed := range l.cfg.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func (l *loop) queue101(slot, gen uint32, accept [28]byte, subprotocol []byte) bool {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return false
	}
	if sl.SendBid >= 0 || sl.SendFrame != nil {
		l.closeConn(slot, gen, 1011)
		return false
	}
	bid, ok := l.tx.Take(slot, gen, 256)
	if !ok {
		l.closeConn(slot, gen, 1011)
		return false
	}
	n := upgrade.Response101(l.tx.Bytes(bid), accept, subprotocol)
	sl.SendBid = bid
	sl.SendOff = 0
	sl.SendLen = int32(n)
	sl.Set(connection.Flag101Queued)
	l.submitSend(slot, gen, bid, 0, n)
	return true
}

// releasePending drops bytes that never reached the WebSocket parser. There is
// no per-connection owner to return them to; taking them here is what matters.
func (l *loop) releasePending(sl *connection.Slot) {
	_, _ = sl.TakePending()
}

func (l *loop) sendHTTPReject(slot, gen uint32, status int, reason string) {
	resp := upgrade.RejectResponse(status, reason)
	f := connection.NewFrame(len(resp))
	copy(f.Buf, resp)
	f.Counted = false
	f.CloseAfter = true
	f.CloseCode = 1008
	if !l.enqueueOwned(slot, gen, f) {
		l.closeConn(slot, gen, 1008)
	}
}

func (l *loop) feedWS(slot, gen uint32, data []byte) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return
	}
	err := sl.WS.Feed(data, func(e ws.Event) error {
		return l.handleWSEvent(slot, gen, e)
	})
	if err != nil {
		var pe *ws.ProtocolError
		if errors.As(err, &pe) {
			l.sendClose(slot, gen, pe.Code)
			return
		}
		l.closeConn(slot, gen, 1011)
	}
}

func (l *loop) handleWSEvent(slot, gen uint32, e ws.Event) error {
	if _, ok := l.slab.Get(slot, gen); !ok {
		return nil
	}
	switch e.Kind {
	case ws.EventMessage:
		l.c.messagesIn.Add(1)
		if l.cfg.EchoMessages {
			l.sendFrame(slot, gen, ws.Binary, e.Payload)
		}
		if l.cfg.OnMessage != nil {
			l.cfg.OnMessage(slot, gen, e.Payload)
		}
	case ws.EventPing:
		l.sendFrame(slot, gen, ws.Pong, e.Payload)
	case ws.EventPong:
	case ws.EventClose:
		if sl, ok := l.slab.Get(slot, gen); ok {
			sl.CloseRemote = true
		}
		code := e.CloseCode
		if code == 1005 {
			code = 1000
		}
		l.sendClose(slot, gen, code)
	}
	return nil
}

func (l *loop) sendClose(slot, gen uint32, code uint16) {
	l.sendCloseData(slot, gen, code, nil)
}

func (l *loop) sendCloseData(slot, gen uint32, code uint16, reason []byte) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return
	}
	if sl.Has(connection.FlagCloseFrameQueued) || sl.Has(connection.FlagCloseAfterSend) {
		return
	}
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], code)
	copy(payload[2:], reason)

	f := l.newFrame(ws.Close, payload, true, code)
	f.Counted = false
	sl.Set(connection.FlagCloseFrameQueued)
	l.armTimer(slot, connection.TimerClose, l.now+uint64(l.cfg.CloseTimeout/time.Millisecond))
	if !l.enqueueOwned(slot, gen, f) {
		l.closeConn(slot, gen, code)
	}
}
