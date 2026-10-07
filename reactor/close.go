//go:build linux

package reactor

import (
	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/uring/giouring"
)

// pendingCancel is a cancel submission that had to wait for submission-queue
// space.
type pendingCancel struct {
	op   Op
	slot uint32
	gen  uint32
}

func (l *loop) closeConn(slot, gen uint32, code uint16) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return
	}
	if sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		l.maybeClose(slot, gen)
		return
	}
	l.logf("close slot=%d gen=%d state=%s code=%d inflight=%d", slot, gen, sl.State, code, sl.InFlight)
	if sl.CloseCode == 0 {
		sl.CloseCode = code
	}
	if !l.slab.Transition(slot, connection.StateClosing) {
		l.c.illegal.Add(1)
		return
	}
	l.disarmAllTimers(slot)
	l.releaseHandshake(sl)
	l.releasePending(sl)
	l.dropQueue(sl)

	if sl.Has(connection.FlagPollArmed) {
		l.cancelOp(OpPoll, slot, gen)
	}
	if sl.Has(connection.FlagRecvArmed) {
		l.cancelOp(OpRecv, slot, gen)
	}
	if sl.SendBid >= 0 || sl.SendFrame != nil {
		l.cancelOp(OpSend, slot, gen)
	}
	l.maybeClose(slot, gen)
}

// dropQueue releases every queued frame. The in-flight frame is released by its
// completion.
func (l *loop) dropQueue(sl *connection.Slot) {
	for {
		node := sl.Pop()
		if node == nil {
			return
		}
		f := node.Frame
		node.Frame = nil
		l.nodes.Put(node)
		l.c.queued.Add(-1)
		l.c.queuedBytes.Add(-int64(f.Cost))
		l.releaseFrame(sl, f)
	}
}

func (l *loop) maybeClose(slot, gen uint32) {
	if !l.tryMaybeClose(slot, gen) {
		l.retryCloses = append(l.retryCloses, slot)
	}
}

// tryMaybeClose submits the close when it can. It reports false only when the
// submission queue is full; the caller retries it on the next iteration.
func (l *loop) tryMaybeClose(slot, gen uint32) bool {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return true
	}
	if sl.State != connection.StateClosing || sl.InFlight != 0 || sl.Has(connection.FlagCloseQueued) {
		return true
	}
	sqe := l.getSQE()
	if sqe == nil {
		return false
	}
	sl.Set(connection.FlagCloseQueued)
	if sl.Direct {
		sqe.PrepareCloseDirect(uint32(sl.FD))
	} else {
		sqe.PrepareClose(int(sl.FD))
	}
	sqe.UserData = EncodeUserData(OpClose, slot, gen)
	l.c.submissions.Add(1)
	l.c.closeSubs.Add(1)
	return true
}

func (l *loop) handleClose(cqe *giouring.CompletionQueueEvent, slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		l.c.stale.Add(1)
		return
	}
	l.c.closeDone.Add(1)
	if cqe.Res < 0 {
		l.c.closeErrors.Add(1)
		l.c.closeErrno.CompareAndSwap(0, -cqe.Res)
	}
	l.disarmAllTimers(slot)
	l.releaseHandshake(sl)
	l.releasePending(sl)
	l.dropQueue(sl)
	if sl.State != connection.StateClosed {
		l.slab.Transition(slot, connection.StateClosed)
	}
	sl.InFlight = 0
	if l.cfg.OnClose != nil {
		l.cfg.OnClose(slot, gen, sl.CloseCode, "", sl.CloseRemote)
	}
	if _, ok := l.slab.Release(slot); ok {
		l.c.closed.Add(1)
		l.c.live.Add(-1)
	}
}

func (l *loop) endOp(slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return
	}
	if sl.InFlight > 0 {
		sl.InFlight--
	}
	if sl.State == connection.StateClosing {
		l.maybeClose(slot, gen)
	}
}

func (l *loop) cancelOp(op Op, slot, gen uint32) {
	if !l.tryCancel(op, slot, gen) {
		l.retryCancels = append(l.retryCancels, pendingCancel{op: op, slot: slot, gen: gen})
	}
}

func (l *loop) tryCancel(op Op, slot, gen uint32) bool {
	sqe := l.getSQE()
	if sqe == nil {
		return false
	}
	sqe.PrepareCancel64(EncodeUserData(op, slot, gen), 0)
	sqe.UserData = EncodeUserData(OpCancel, slot, gen)
	l.c.submissions.Add(1)
	return true
}

// retryPendingOps re-submits work that could not enter a full submission
// queue. It runs after completions free SQ space and before the ring waits.
func (l *loop) retryPendingOps() {
	if len(l.retryCancels) > 0 {
		keep := l.retryCancels[:0]
		for _, c := range l.retryCancels {
			if !l.tryCancel(c.op, c.slot, c.gen) {
				keep = append(keep, c)
			}
		}
		l.retryCancels = keep
	}
	if len(l.retryCloses) > 0 {
		keep := l.retryCloses[:0]
		for _, slot := range l.retryCloses {
			sl, ok := l.slab.GetAny(slot)
			if !ok {
				continue
			}
			if !l.tryMaybeClose(slot, sl.Gen) {
				keep = append(keep, slot)
			}
		}
		l.retryCloses = keep
	}
}

func (l *loop) park(slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || sl.Has(connection.FlagParked) {
		return
	}
	sl.Set(connection.FlagParked)
	l.parked = append(l.parked, slot)
}

func (l *loop) wakeParked() {
	for l.rx.FreeCount() > 0 && len(l.parked) > 0 {
		n := len(l.parked) - 1
		slot := l.parked[n]
		l.parked = l.parked[:n]
		sl, ok := l.slab.GetAny(slot)
		if !ok || !sl.Has(connection.FlagParked) {
			continue
		}
		sl.Clear(connection.FlagParked)
		if !l.armRecv(slot, sl.Gen) {
			l.park(slot, sl.Gen)
			return
		}
	}
}
