//go:build linux

package reactor

import (
	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/timerwheel"
)

// ackTimer is the wheel handle for one pending acknowledgement plus the slab
// generation it was armed for. The generation is carried through the timer so
// a stale fire cannot consume a rebound entry.
type ackTimer struct {
	handle uint32
	gen    uint32
}

// pendingCancel is a cancel submission that had to wait for submission-queue
// space.
func (l *loop) armTimer(slot uint32, kind connection.TimerKind, deadline uint64) {
	h, ok := l.slab.Timer(slot, kind)
	if !ok {
		return
	}
	if h == timerwheel.NoNode {
		var acquired bool
		h, acquired = l.wheel.Acquire()
		if !acquired {
			return
		}
		l.wheel.SetMeta(h, slot, uint8(kind))
		l.slab.SetTimer(slot, kind, h)
	}
	l.wheel.Add(h, l.now, deadline)
}

func (l *loop) disarmTimer(slot uint32, kind connection.TimerKind) {
	h, ok := l.slab.Timer(slot, kind)
	if !ok || h == timerwheel.NoNode {
		return
	}
	l.wheel.Remove(h)
	l.wheel.Release(h)
	l.slab.ClearTimer(slot, kind)
}

func (l *loop) disarmAllTimers(slot uint32) {
	for kind := connection.TimerKind(0); kind < connection.NumTimerKinds; kind++ {
		l.disarmTimer(slot, kind)
	}
}

func (l *loop) advanceTimers() {
	out := l.wheel.Advance(l.now, l.timerScratch[:0])
	for _, h := range out {
		n := l.wheel.Node(h)
		if n == nil {
			continue
		}
		owner := n.Owner
		kind := n.Kind
		l.wheel.Release(h)
		if kind == timerAckKind {
			var gen uint32
			if t, ok := l.ackTimers[owner]; ok {
				gen = t.gen
			}
			delete(l.ackTimers, owner)
			l.c.ackTimeouts.Add(1)
			if l.cfg.OnAckTimeout != nil {
				l.cfg.OnAckTimeout(owner, gen)
			}
			continue
		}
		l.slab.ClearTimer(owner, connection.TimerKind(kind))
		l.c.timerExpired.Add(1)
		sl, ok := l.slab.GetAny(owner)
		if !ok {
			continue
		}
		l.closeConn(owner, sl.Gen, 1008)
	}
	l.timerScratch = out
}

// armAckTimer arms one acknowledgement deadline. It reports false when the
// wheel has no node left, so the caller can fail the pending entry loudly
// instead of leaking an entry with no timer.
func (l *loop) armAckTimer(ackSlot, gen uint32, timeoutMs uint64) bool {
	if timeoutMs == 0 {
		timeoutMs = 1
	}
	if old, ok := l.ackTimers[ackSlot]; ok {
		l.wheel.Remove(old.handle)
		l.wheel.Release(old.handle)
		delete(l.ackTimers, ackSlot)
	}
	h, ok := l.wheel.Acquire()
	if !ok {
		return false
	}
	l.wheel.SetMeta(h, ackSlot, timerAckKind)
	l.wheel.Add(h, l.now, l.now+timeoutMs)
	l.ackTimers[ackSlot] = ackTimer{handle: h, gen: gen}
	return true
}

func (l *loop) cancelAckTimer(ackSlot uint32) {
	t, ok := l.ackTimers[ackSlot]
	if !ok {
		return
	}
	l.wheel.Remove(t.handle)
	l.wheel.Release(t.handle)
	delete(l.ackTimers, ackSlot)
}
