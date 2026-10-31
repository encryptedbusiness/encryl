//go:build linux

package app

import (
	"errors"
	"fmt"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/encryptedbusiness/encryl/reactor"
)

// handleProtocolMessage decodes one Bebop frame and handles each command in
// order on the owning loop.
func (s *Server) handleProtocolMessage(c *Conn, payload []byte) {
	if c.session == nil {
		s.closeConn(int(c.loop), c.slot, c.gen, 1002)
		return
	}
	cmds, err := c.session.Decode(payload)
	if err != nil {
		s.closeConn(int(c.loop), c.slot, c.gen, 1002)
		return
	}
	for i := range cmds {
		cmd := &cmds[i]
		if !c.session.Connected() && cmd.Kind != protocol.CommandConnect {
			c.sendProtocolReply(&protocol.Reply{
				ID:    cmd.ID,
				Error: &protocol.Error{Code: protocol.CodeBadRequest, Message: []byte("connect required")},
			})
			s.closeConn(int(c.loop), c.slot, c.gen, 1002)
			return
		}
		switch cmd.Kind {
		case protocol.CommandConnect:
			s.handleProtoConnect(c, cmd)
		case protocol.CommandSubscribe:
			s.handleProtoSubscribe(c, cmd)
		case protocol.CommandUnsubscribe:
			s.handleProtoUnsubscribe(c, cmd)
		case protocol.CommandPublish:
			s.handleProtoPublish(c, cmd)
		case protocol.CommandSend:
			s.handleProtoSend(c, cmd)
		case protocol.CommandAck:
			s.handleProtoAck(c, cmd)
		case protocol.CommandPong:
			// Liveness is handled by the WebSocket ping/pong today.
		default:
			c.sendProtocolReply(&protocol.Reply{
				ID:    cmd.ID,
				Error: &protocol.Error{Code: protocol.CodeBadRequest, Message: []byte("unknown command")},
			})
		}
	}
}

func (s *Server) handleProtoConnect(c *Conn, cmd *protocol.Command) {
	if c.session.Connected() {
		c.sendProtocolReply(&protocol.Reply{
			ID:    cmd.ID,
			Error: &protocol.Error{Code: protocol.CodeBadRequest, Message: []byte("already connected")},
		})
		return
	}
	err := s.verifyToken(cmd.Connect.Token)
	if err != nil {
		c.sendProtocolReply(&protocol.Reply{
			ID:    cmd.ID,
			Error: &protocol.Error{Code: protocol.CodeUnauthorized, Message: []byte(err.Error())},
		})
		s.closeConn(int(c.loop), c.slot, c.gen, 1008)
		return
	}
	c.session.SetConnected(true)

	subs := make([]protocol.SubscribeResult, 0, len(cmd.Connect.Subscriptions))
	for _, name := range cmd.Connect.Subscriptions {
		room := string(name)
		if room == "" {
			continue
		}
		if err := c.Join(room); err != nil {
			continue
		}
		subs = append(subs, protocol.SubscribeResult{
			RoomID:  c.session.AddRoom(room),
			EventID: s.roomEvent(room),
		})
	}
	c.sendProtocolReply(&protocol.Reply{
		ID:   cmd.ID,
		Kind: protocol.ReplyConnect,
		Connect: protocol.ConnectResult{
			ClientID:      []byte(fmt.Sprintf("%d:%d", c.slot, c.gen)),
			PingSeconds:   s.pingSeconds(),
			Subscriptions: subs,
		},
	})
}

func (s *Server) handleProtoSubscribe(c *Conn, cmd *protocol.Command) {
	room := string(cmd.Subscribe.Room)
	if room == "" {
		c.sendProtocolReply(&protocol.Reply{
			ID:    cmd.ID,
			Error: &protocol.Error{Code: protocol.CodeBadRequest, Message: []byte("room is required")},
		})
		return
	}
	if err := c.Join(room); err != nil {
		c.sendProtocolReply(&protocol.Reply{ID: cmd.ID, Error: protoError(err)})
		return
	}
	c.sendProtocolReply(&protocol.Reply{
		ID:   cmd.ID,
		Kind: protocol.ReplySubscribe,
		Subscribe: protocol.SubscribeResult{
			RoomID:  c.session.AddRoom(room),
			EventID: s.roomEvent(room),
		},
	})
}

func (s *Server) handleProtoUnsubscribe(c *Conn, cmd *protocol.Command) {
	room, ok := c.session.RoomName(cmd.Unsubscribe.RoomID)
	if !ok {
		c.sendProtocolReply(&protocol.Reply{
			ID:    cmd.ID,
			Error: &protocol.Error{Code: protocol.CodeNotSubscribed, Message: []byte("not subscribed")},
		})
		return
	}
	_ = c.Leave(room)
	c.session.RemoveRoom(cmd.Unsubscribe.RoomID)
	c.sendProtocolReply(&protocol.Reply{ID: cmd.ID})
}

func (s *Server) handleProtoPublish(c *Conn, cmd *protocol.Command) {
	if cmd.Publish.RoomID != 0 {
		if _, ok := c.session.RoomName(cmd.Publish.RoomID); !ok {
			c.sendProtocolReply(&protocol.Reply{
				ID:    cmd.ID,
				Error: &protocol.Error{Code: protocol.CodeNotSubscribed, Message: []byte("not subscribed")},
			})
			return
		}
	}
	room := c.protoRoom(cmd.Publish.RoomID, cmd.Publish.Room)
	eventID, ok := s.protoEvent(cmd.Publish.Event, room)
	if !ok {
		c.sendProtocolReply(&protocol.Reply{
			ID:    cmd.ID,
			Error: &protocol.Error{Code: protocol.CodeUnknownEvent, Message: []byte("unknown event")},
		})
		return
	}
	m := Message{Event: eventID, RequestID: uint64(cmd.ID), Payload: cmd.Publish.Payload, Binary: true}

	c.replied = false
	if h, found := s.events.Lookup(eventID); found {
		if err := h(c, m); err != nil {
			if !c.replied {
				c.sendProtocolError(cmd.ID, protoErrorCode(err), err.Error())
			}
			return
		}
		if cmd.ID != 0 && !c.replied {
			c.sendProtocolReply(&protocol.Reply{ID: cmd.ID, Kind: protocol.ReplyPublish})
		}
		return
	}

	if room == "" {
		c.sendProtocolReply(&protocol.Reply{
			ID:    cmd.ID,
			Error: &protocol.Error{Code: protocol.CodeUnknownRoom, Message: []byte("room is required")},
		})
		return
	}
	if err := s.To(room).EmitID(eventID, cmd.Publish.Payload); err != nil {
		c.sendProtocolReply(&protocol.Reply{ID: cmd.ID, Error: protoError(err)})
		return
	}
	if cmd.ID != 0 {
		c.sendProtocolReply(&protocol.Reply{ID: cmd.ID, Kind: protocol.ReplyPublish})
	}
}

func (s *Server) handleProtoSend(c *Conn, cmd *protocol.Command) {
	eventID, ok := s.protoEvent(cmd.Send.Event, "")
	if !ok {
		return
	}
	h, found := s.events.Lookup(eventID)
	if !found {
		return
	}
	_ = h(c, Message{Event: eventID, Payload: cmd.Send.Payload, Binary: true})
}

func (s *Server) handleProtoAck(c *Conn, cmd *protocol.Command) {
	entry, ok := s.pending.Take(cmd.Ack.Token, c.loop, c.slot, c.gen)
	if !ok {
		s.staleAcks.Add(1)
		return
	}
	st := s.state.Load()
	if st != nil && int(entry.Loop) < len(st.loops) && st.loops[entry.Loop] != nil {
		st.loops[entry.Loop].CancelAck(entry.Slot)
	}
	if entry.Callback != nil {
		entry.Callback(event.Ack{Event: entry.Event, Payload: cmd.Ack.Payload})
	}
}

func (c *Conn) protoRoom(roomID uint32, room []byte) string {
	if roomID != 0 {
		if name, ok := c.session.RoomName(roomID); ok {
			return name
		}
		return ""
	}
	return string(room)
}

func (s *Server) protoEvent(ref protocol.EventRef, room string) (uint32, bool) {
	if ref.ID != 0 {
		return ref.ID, true
	}
	if len(ref.Name) > 0 {
		return s.eventNames.IDBytes(ref.Name)
	}
	if room != "" {
		if id, ok := s.channelEvent(room); ok {
			return id, true
		}
	}
	return 0, false
}

func (s *Server) roomEvent(room string) uint32 {
	id, _ := s.channelEvent(room)
	return id
}

func (s *Server) channelEvent(room string) (uint32, bool) {
	s.mu.RLock()
	id, ok := s.channelEvents[room]
	s.mu.RUnlock()
	return id, ok
}

func (s *Server) pingSeconds() uint32 {
	if s.cfg.PingInterval <= 0 {
		return 0
	}
	return uint32(s.cfg.PingInterval / time.Second)
}

func (s *Server) verifyToken(token []byte) error {
	if s.tokenVerify == nil {
		return nil
	}
	return s.tokenVerify(token)
}

func protoErrorCode(err error) uint32 {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return protocol.CodeUnauthorized
	case errors.Is(err, ErrRoomLimit):
		return protocol.CodeLimitExceeded
	case errors.Is(err, ErrPayloadTooLarge):
		return protocol.CodeLimitExceeded
	case errors.Is(err, ErrBackpressure), errors.Is(err, ErrSlowConsumer):
		return protocol.CodeBackpressure
	case errors.Is(err, ErrClosed):
		return protocol.CodeInternal
	default:
		return protocol.CodeInternal
	}
}

func protoCodeFromNative(code uint16) uint32 {
	switch code {
	case event.ErrCodeUnauthorized:
		return protocol.CodeUnauthorized
	case event.ErrCodeUnknownEvent:
		return protocol.CodeUnknownEvent
	case event.ErrCodePayloadTooLarge, event.ErrCodeRateLimited:
		return protocol.CodeLimitExceeded
	case event.ErrCodeBackpressure:
		return protocol.CodeBackpressure
	default:
		return protocol.CodeInternal
	}
}

func protoError(err error) *protocol.Error {
	return &protocol.Error{Code: protoErrorCode(err), Message: []byte(err.Error())}
}

// sendProtocolReply encodes and enqueues one reply.
func (c *Conn) sendProtocolReply(r *protocol.Reply) {
	size := protocol.FramedReplySizeBebop(r)
	f, h := connection.NewFrameWithHeader(size)
	out := protocol.AppendFramedReplyBebop(f.Buf[h:h], r)
	f.Buf = f.Buf[:h+len(out)]
	f.Counted = true
	_ = c.sendFrame(f, reactor.Command{Kind: reactor.CmdSend, Slot: c.slot, Gen: c.gen, Frame: f})
}

// buildProtocolPush encodes one push into a frame. The caller owns the reference.
func (c *Conn) buildProtocolPush(p *protocol.Push) *connection.Frame {
	size := protocol.FramedPushSizeBebop(p)
	f, h := connection.NewFrameWithHeader(size)
	out := protocol.AppendFramedPushBebop(f.Buf[h:h], p)
	f.Buf = f.Buf[:h+len(out)]
	f.Counted = true
	return f
}

// sendProtocolPush encodes and enqueues one push.
func (c *Conn) sendProtocolPush(p *protocol.Push) {
	f := c.buildProtocolPush(p)
	_ = c.sendFrame(f, reactor.Command{Kind: reactor.CmdSend, Slot: c.slot, Gen: c.gen, Frame: f})
}

func (c *Conn) sendProtocolError(id uint32, code uint32, text string) {
	c.sendProtocolReply(&protocol.Reply{
		ID:    id,
		Error: &protocol.Error{Code: code, Message: []byte(text)},
	})
}

// emitProtocol sends one Self-Room event to a protocol connection.
func (c *Conn) emitProtocol(eventID uint32, payload []byte) error {
	if c == nil || c.srv == nil || c.closed.Load() || c.acct == nil {
		return ErrClosed
	}
	if int64(len(payload)) > c.srv.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	c.sendProtocolPush(&protocol.Push{
		Kind:    protocol.PushMessage,
		Message: protocol.Message{EventID: eventID, Payload: payload},
	})
	return nil
}

// emitWithAckProtocol sends a server-initiated request and arms its timeout.
func (c *Conn) emitWithAckProtocol(eventID uint32, payload []byte, timeout time.Duration, cb func(Ack)) error {
	return c.armEmitWithAck(eventID, payload, timeout, cb, func(token uint64) *connection.Frame {
		p := protocol.Push{
			Kind:    protocol.PushRequest,
			Request: protocol.Request{Token: token, EventID: eventID, Payload: payload},
		}
		return c.buildProtocolPush(&p)
	})
}
