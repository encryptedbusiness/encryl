//go:build linux

package reactor

import (
	"time"
	"unsafe"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/uring/giouring"
	"github.com/encryptedbusiness/encryl/ws"
	"golang.org/x/sys/unix"
)

// newFrame builds one encoded wire frame. The caller owns the returned
// reference.
func (l *loop) newFrame(op ws.Opcode, payload []byte, closeAfter bool, code uint16) *connection.Frame {
	hl := ws.FrameHeaderLen(len(payload))
	f := connection.NewFrame(hl + len(payload))
	n := ws.AppendFrameHeader(f.Buf, op, len(payload))
	copy(f.Buf[n:], payload)
	f.CloseAfter = closeAfter
	f.CloseCode = code
	return f
}

func (l *loop) sendFrame(slot, gen uint32, op ws.Opcode, payload []byte) bool {
	f := l.newFrame(op, payload, false, 0)
	f.Counted = op == ws.Binary
	return l.enqueueOwned(slot, gen, f)
}

// full reports whether one more frame of cost bytes would exceed the
// connection's budget. It counts the in-flight frame in addition to the queue.
func (l *loop) full(sl *connection.Slot, cost int32) bool {
	if sl.Acct() == nil {
		return false
	}
	msgs, bytes := sl.Acct().Pending()
	return int64(msgs)+1 > int64(l.cfg.WriteQueueMessages) || bytes+int64(cost) > l.cfg.WriteQueueBytes
}

// evictOldest drops the oldest queued frame to make room.
func (l *loop) evictOldest(sl *connection.Slot) bool {
	node := sl.Pop()
	if node == nil {
		return false
	}
	f := node.Frame
	node.Frame = nil
	l.nodes.Put(node)
	l.c.queued.Add(-1)
	l.c.queuedBytes.Add(-int64(f.Cost))
	l.c.evicted.Add(1)
	l.releaseFrame(sl, f)
	return true
}

// enqueueOwned consumes one reference of f.
func (l *loop) enqueueOwned(slot, gen uint32, f *connection.Frame) bool {
	if f == nil {
		return false
	}
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		l.c.staleSends.Add(1)
		if f.Admitted && f.Counted {
			l.c.backpressured.Add(1)
		}
		f.Release()
		return false
	}
	if sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		l.releaseAdmitted(sl, f)
		f.Release()
		return false
	}

	if f.Volatile && (sl.SendFrame != nil || sl.SendBid >= 0 || !sl.QueueEmpty()) {
		l.c.dropped.Add(1)
		l.releaseAdmitted(sl, f)
		f.Release()
		return false
	}

	if f.Counted && f.Admitted {
		// The producer reserved budget under the admission mutex and already
		// received nil. Honor a pending DropOldest eviction, then queue the
		// frame unconditionally: a later silent drop would break the contract.
		if sl.Acct() != nil && sl.Acct().TakeEvict() {
			if !l.evictOldest(sl) {
				l.c.dropped.Add(1)
				l.releaseAccount(sl, f)
				f.Release()
				return false
			}
		}
	} else if f.Counted {
		// A fan-out frame is Counted but not Admitted: the loop has not
		// reserved its budget yet, so a drop must not release anything.
		if l.full(sl, f.Cost) {
			switch l.cfg.Backpressure {
			case connection.BackpressureDropNewest:
				l.c.dropped.Add(1)
				l.releaseAdmitted(sl, f)
				f.Release()
				return false
			case connection.BackpressureDropOldest:
				if !l.evictOldest(sl) || l.full(sl, f.Cost) {
					l.c.dropped.Add(1)
					l.releaseAdmitted(sl, f)
					f.Release()
					return false
				}
			case connection.BackpressureCloseSlow:
				l.releaseAdmitted(sl, f)
				f.Release()
				l.closeConn(slot, gen, 1013)
				return false
			default:
				l.c.backpressured.Add(1)
				l.c.dropped.Add(1)
				l.releaseAdmitted(sl, f)
				f.Release()
				return false
			}
		}
		if sl.Acct() != nil {
			sl.Acct().Add(f.Cost)
		}
	}

	node := l.nodes.Get()
	node.Frame = f
	sl.Push(node)
	l.c.queued.Add(1)
	l.c.queuedBytes.Add(int64(f.Cost))
	if f.Counted {
		l.c.messagesOut.Add(1)
	}
	if sl.Acct() != nil {
		if _, bytes := sl.Acct().Pending(); bytes >= readPauseBytes {
			l.pauseReads(slot, gen)
		}
	}
	l.pump(slot, gen)
	return true
}

// releaseAdmitted returns budget only when the producer reserved it. A
// fan-out frame is Counted but not Admitted until the loop adds it, so
// releasing on a pre-admission drop would consume another frame's budget.
func (l *loop) releaseAdmitted(sl *connection.Slot, f *connection.Frame) {
	if f.Admitted {
		l.releaseAccount(sl, f)
	}
}

func (l *loop) releaseAccount(sl *connection.Slot, f *connection.Frame) {
	if !f.Counted || sl == nil || sl.Acct() == nil {
		return
	}
	sl.Acct().Release(f.Cost)
}

func (l *loop) releaseFrame(sl *connection.Slot, f *connection.Frame) {
	if f == nil {
		return
	}
	l.releaseAccount(sl, f)
	f.Release()
}

// pump starts the next queued frame when the connection has no send in flight.
func (l *loop) pump(slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || sl.SendFrame != nil || sl.SendBid >= 0 {
		return
	}
	if sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		return
	}
	node := sl.Pop()
	if node == nil {
		return
	}
	f := node.Frame
	node.Frame = nil
	l.nodes.Put(node)
	l.c.queued.Add(-1)
	l.c.queuedBytes.Add(-int64(f.Cost))

	if f.CloseAfter {
		sl.Set(connection.FlagCloseAfterSend)
		if f.CloseCode != 0 {
			sl.CloseCode = f.CloseCode
		}
		sl.Clear(connection.FlagCloseFrameQueued)
	}
	sl.SendFrame = f
	sl.SendOff = 0
	sl.SendLen = int32(len(f.Buf))
	l.submitFrame(slot, gen, f, 0)
}

func (l *loop) submitFrame(slot, gen uint32, f *connection.Frame, off int) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || len(f.Buf) == 0 {
		l.releaseFrame(sl, f)
		return
	}
	if sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		sl.SendFrame = nil
		l.releaseFrame(sl, f)
		return
	}
	sqe := l.getSQE()
	if sqe == nil {
		sl.SendFrame = nil
		l.releaseFrame(sl, f)
		l.closeConn(slot, gen, 1011)
		return
	}
	n := len(f.Buf) - off
	addr := uintptr(unsafe.Pointer(&f.Buf[0])) + uintptr(off)
	if sl.Direct {
		sqe.PrepareSendFixed(uint32(sl.FD), addr, uint32(n), unix.MSG_NOSIGNAL)
	} else {
		sqe.PrepareSend(int(sl.FD), addr, uint32(n), unix.MSG_NOSIGNAL)
	}
	sqe.UserData = EncodeUserData(OpSend, slot, gen)
	l.slab.BeginOp(slot)
	l.c.submissions.Add(1)
}

func (l *loop) submitSend(slot, gen uint32, bid int32, off, n int) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		l.tx.Release(bid, slot, gen)
		return
	}
	if sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		l.tx.Release(bid, slot, gen)
		sl.SendBid = -1
		sl.SendOff, sl.SendLen = 0, 0
		return
	}
	sqe := l.getSQE()
	if sqe == nil {
		l.tx.Release(bid, slot, gen)
		sl.SendBid = -1
		l.closeConn(slot, gen, 1011)
		return
	}
	addr := l.tx.Addr(bid) + uintptr(off)
	if sl.Direct {
		sqe.PrepareSendFixed(uint32(sl.FD), addr, uint32(n), unix.MSG_NOSIGNAL)
	} else {
		sqe.PrepareSend(int(sl.FD), addr, uint32(n), unix.MSG_NOSIGNAL)
	}
	sqe.UserData = EncodeUserData(OpSend, slot, gen)
	l.slab.BeginOp(slot)
	l.c.submissions.Add(1)
}

func (l *loop) handleSend(cqe *giouring.CompletionQueueEvent, slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		l.c.stale.Add(1)
		return
	}
	l.endOp(slot, gen)

	f := sl.SendFrame
	bid := sl.SendBid
	if f == nil && bid < 0 {
		l.c.stale.Add(1)
		return
	}
	if cqe.Res < 0 {
		l.c.sendErrors.Add(1)
		if f != nil {
			sl.SendFrame = nil
			l.releaseFrame(sl, f)
		}
		if bid >= 0 {
			l.tx.Release(bid, slot, gen)
			sl.SendBid = -1
		}
		sl.SendOff, sl.SendLen = 0, 0
		l.closeConn(slot, gen, 1006)
		return
	}

	written := int(sl.SendOff) + int(cqe.Res)
	l.c.bytesOut.Add(uint64(cqe.Res))
	if written < int(sl.SendLen) {
		sl.SendOff = int32(written)
		if f != nil {
			l.submitFrame(slot, gen, f, written)
		} else {
			l.submitSend(slot, gen, bid, written, int(sl.SendLen)-written)
		}
		return
	}

	if f != nil {
		sl.SendFrame = nil
		l.releaseFrame(sl, f)
	}
	if bid >= 0 {
		l.tx.Release(bid, slot, gen)
		sl.SendBid = -1
	}
	sl.SendOff, sl.SendLen = 0, 0

	if sl.Has(connection.Flag101Queued) {
		sl.Clear(connection.Flag101Queued)
		l.disarmTimer(slot, connection.TimerHandshake)
		if !l.slab.Transition(slot, connection.StateOpen) {
			l.c.illegal.Add(1)
		}
		l.armTimer(slot, connection.TimerIdle, l.now+uint64(l.cfg.IdleTimeout/time.Millisecond))
		ip, port, family, _ := l.slab.Peer(slot)
		if l.cfg.OnOpen != nil {
			l.cfg.OnOpen(slot, gen, sl.Acct(), ip, port, family, sl.Encoding)
		}
		if pending, _ := sl.TakePending(); len(pending) > 0 {
			l.feedWS(slot, gen, pending)
		}
	}

	if sl.State == connection.StateClosing {
		l.maybeClose(slot, gen)
		return
	}
	if sl.Has(connection.FlagCloseAfterSend) {
		sl.Clear(connection.FlagCloseAfterSend)
		code := sl.CloseCode
		if code == 0 {
			code = 1008
		}
		l.closeConn(slot, gen, code)
		return
	}
	l.pump(slot, gen)
	l.resumeReads(slot, gen)
}
