//go:build linux

package app

import (
	"errors"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/encryptedbusiness/encryl/reactor"
)

// Emit sends one event to this connection. It is an Emit to the connection's
// Self-Room.
//
// It returns synchronously: nil means the message was accepted into the write
// queue, ErrBackpressure means the queue is full under BackpressureReject, and
// ErrClosed means the connection is gone. A payload larger than MaxMessageSize
// returns ErrPayloadTooLarge. Emit does not block on the network.
func (c *Conn) Emit(event string, payload []byte) error {
	return c.EmitID(EventIDForName(event), payload)
}

// EmitID is the numeric low-level form of Emit.
func (c *Conn) EmitID(eventID uint32, payload []byte) error {
	if c != nil && c.encoding == protocol.EncodingProtobufV1 {
		return c.emitProto(eventID, payload)
	}
	return c.emit(eventID, payload, 0)
}

// EmitIDLocal is the loop-thread-only fast path for sending from an event
// handler. It bypasses the cross-loop command ring and eventfd wake. It must
// only be called from OnID, OnMessage, OnOpen, or another callback running on
// this connection's owning loop.
func (c *Conn) EmitIDLocal(eventID uint32, payload []byte) error {
	if c == nil || c.srv == nil || c.closed.Load() || c.acct == nil {
		return ErrClosed
	}
	if c.encoding == protocol.EncodingProtobufV1 {
		return c.emitProto(eventID, payload)
	}
	if int64(len(payload)) > c.srv.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	f := connection.BuildFrame(event.KindEvent, eventID, 0, 0, payload)
	return c.sendFrameLocal(f)
}

// EmitFrameLocal queues an immutable, pre-encoded frame on the owning loop.
// The caller keeps ownership of the frame; this method retains one reference
// for the queue. It is loop-thread-only.
func (c *Conn) EmitFrameLocal(f *connection.Frame) error {
	if c == nil || f == nil {
		return ErrClosed
	}
	f.Retain()
	return c.sendFrameLocal(f)
}

func (c *Conn) sendFrameLocal(f *connection.Frame) error {
	ok, err := c.admit(f)
	if !ok {
		return err
	}
	st := c.srv.state.Load()
	if st == nil || c.loop < 0 || int(c.loop) >= len(st.loops) || st.loops[c.loop] == nil {
		c.acct.Release(f.Cost)
		f.Release()
		return ErrClosed
	}
	// SendLocal consumes the frame whether it is queued, dropped, rejected, or
	// stale; enqueueOwned releases the admission/frame on every failure path.
	st.loops[c.loop].SendLocal(c.slot, c.gen, f)
	return nil
}

func (c *Conn) emit(eventID uint32, payload []byte, requestID uint64) error {
	if c == nil || c.srv == nil || c.closed.Load() || c.acct == nil {
		return ErrClosed
	}
	if int64(len(payload)) > c.srv.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	f := connection.BuildFrame(event.KindEvent, eventID, requestID, 0, payload)
	return c.sendFrame(f, reactor.Command{
		Kind:  reactor.CmdSend,
		Slot:  c.slot,
		Gen:   c.gen,
		Frame: f,
	})
}

// sendFrame admits and enqueues one frame for this connection.
func (c *Conn) sendFrame(f *connection.Frame, cmd reactor.Command) error {
	ok, err := c.admit(f)
	if !ok {
		return err
	}
	cmd.Frame = f
	if err := c.srv.enqueue(int(c.loop), cmd); err != nil {
		c.acct.Release(f.Cost)
		f.Release()
		return err
	}
	return nil
}

// admit applies the configured write-queue policy synchronously. On success
// the frame's budget is reserved and the caller must hand ownership to the loop
// or release it.
func (c *Conn) admit(f *connection.Frame) (bool, error) {
	if c == nil || c.acct == nil {
		f.Release()
		return false, ErrClosed
	}
	cfg := &c.srv.cfg
	res := c.acct.Admit(f.Cost, int32(cfg.WriteQueueMessages), cfg.WriteQueueBytes,
		connection.BackpressurePolicy(cfg.Backpressure))
	switch res {
	case connection.AdmitQueued:
		f.Admitted = true
		return true, nil
	case connection.AdmitDropped:
		c.srv.dropped.Add(1)
		f.Release()
		return false, nil
	case connection.AdmitCloseSlow:
		c.srv.slowConsumers.Add(1)
		f.Release()
		c.closed.Store(true)
		_ = c.srv.enqueue(int(c.loop), reactor.Command{
			Kind: reactor.CmdClose,
			Slot: c.slot,
			Gen:  c.gen,
			A:    1013,
		})
		return false, ErrSlowConsumer
	default:
		c.srv.backpressure.Add(1)
		f.Release()
		return false, ErrBackpressure
	}
}

// EmitWithAck sends an event with a non-zero request ID and invokes cb when the
// peer acknowledges it or the timeout expires. The callback runs on the
// connection's owning loop and must not block.
func (c *Conn) EmitWithAckID(eventID uint32, payload []byte, timeout time.Duration, cb func(Ack)) error {
	if c == nil || c.srv == nil || c.closed.Load() || c.acct == nil {
		return ErrClosed
	}
	if c.encoding == protocol.EncodingProtobufV1 {
		return c.emitWithAckProto(eventID, payload, timeout, cb)
	}
	return c.armEmitWithAck(eventID, payload, timeout, cb, func(token uint64) *connection.Frame {
		return connection.BuildFrame(event.KindEvent, eventID, token, 0, payload)
	})
}

// armEmitWithAck is the shared request/acknowledgement path for both encodings.
func (c *Conn) armEmitWithAck(eventID uint32, payload []byte, timeout time.Duration, cb func(Ack), build func(uint64) *connection.Frame) error {
	if cb == nil {
		return errors.New("encryl: nil Ack callback")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if int64(len(payload)) > c.srv.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	token, ackSlot, ok := c.srv.pending.Bind(c.loop, c.slot, c.gen, eventID, cb)
	if !ok {
		return ErrBackpressure
	}
	f := build(token)
	if ok, err := c.admit(f); !ok {
		c.srv.pending.Take(token, c.loop, c.slot, c.gen)
		if err == nil {
			return ErrBackpressure
		}
		return err
	}
	arm := reactor.Command{
		Kind: reactor.CmdAckArm,
		A:    event.PackAckToken(ackSlot, c.gen),
		B:    uint64(timeout / time.Millisecond),
	}
	if !c.srv.enqueueChecked(int(c.loop), arm) {
		c.srv.pending.Take(token, c.loop, c.slot, c.gen)
		c.acct.Release(f.Cost)
		f.Release()
		return ErrBackpressure
	}
	if err := c.srv.enqueue(int(c.loop), reactor.Command{
		Kind:  reactor.CmdSend,
		Slot:  c.slot,
		Gen:   c.gen,
		Frame: f,
	}); err != nil {
		_ = c.srv.enqueue(int(c.loop), reactor.Command{Kind: reactor.CmdAckCancel, A: uint64(ackSlot)})
		c.srv.pending.Take(token, c.loop, c.slot, c.gen)
		c.acct.Release(f.Cost)
		f.Release()
		return err
	}
	return nil
}

// Reply sends an acknowledgement for m with an optional payload. It uses
// m.RequestID, so calling it for an unsolicited message is an error. When a
// handler calls Reply, the automatic empty ACK is suppressed.
// Ask sends a named request to this connection and waits for an
// acknowledgement. The callback runs on the owning loop.
func (c *Conn) Ask(event string, payload []byte, timeout time.Duration, cb func(Ack)) error {
	return c.EmitWithAckID(EventIDForName(event), payload, timeout, cb)
}

func (c *Conn) Reply(m Message, payload []byte) error {
	if c == nil || c.srv == nil || c.closed.Load() {
		return ErrClosed
	}
	if m.RequestID == 0 {
		return errors.New("encryl: Message has no RequestID")
	}
	if int64(len(payload)) > c.srv.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	c.replied = true
	if c.encoding == protocol.EncodingProtobufV1 {
		c.sendProtoReply(&protocol.Reply{
			ID:   uint32(m.RequestID),
			Kind: protocol.ReplyAck,
			Ack:  protocol.AckResult{Payload: payload},
		})
		return nil
	}
	f := connection.BuildFrame(event.KindAck, m.Event, m.RequestID, 0, payload)
	return c.sendFrame(f, reactor.Command{
		Kind:  reactor.CmdSend,
		Slot:  c.slot,
		Gen:   c.gen,
		Frame: f,
	})
}

// sendACK sends an empty acknowledgement event.
func (c *Conn) sendACK(m Message) {
	if c.encoding == protocol.EncodingProtobufV1 {
		c.sendProtoReply(&protocol.Reply{ID: uint32(m.RequestID), Kind: protocol.ReplyAck})
		return
	}
	f := connection.BuildFrame(event.KindAck, m.Event, m.RequestID, 0, nil)
	_ = c.sendFrame(f, reactor.Command{
		Kind:  reactor.CmdSend,
		Slot:  c.slot,
		Gen:   c.gen,
		Frame: f,
	})
}

// sendError sends an ERROR envelope with the same RequestID.
func (c *Conn) sendError(m Message, code uint16, text string) {
	if c.encoding == protocol.EncodingProtobufV1 {
		c.sendProtoError(uint32(m.RequestID), protoCodeFromNative(code), text)
		return
	}
	body := make([]byte, 2+len(text))
	body[0] = byte(code >> 8)
	body[1] = byte(code)
	copy(body[2:], text)
	f := connection.BuildFrame(event.KindError, m.Event, m.RequestID, 0, body)
	_ = c.sendFrame(f, reactor.Command{
		Kind:  reactor.CmdSend,
		Slot:  c.slot,
		Gen:   c.gen,
		Frame: f,
	})
}
