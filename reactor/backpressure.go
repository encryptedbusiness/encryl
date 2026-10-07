//go:build linux

package reactor

import (
	connection "github.com/encryptedbusiness/encryl/connslab"
)

const (
	readPauseBytes  = 64 << 10
	readResumeBytes = 32 << 10
)

// pauseReads stops receive re-arming while the outbound queue drains. It
// cancels an in-flight receive but leaves the poll armed so the socket wakes
// when the peer sends more data after the pause clears.
func (l *loop) pauseReads(slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || sl.ReadPaused {
		return
	}
	sl.ReadPaused = true
	l.c.readPauses.Add(1)
	if sl.Has(connection.FlagRecvArmed) {
		l.cancelOp(OpRecv, slot, gen)
	}
}

// resumeReads clears the pause when the outbound queue is below the low
// threshold.
func (l *loop) resumeReads(slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || !sl.ReadPaused {
		return
	}
	if sl.Acct() != nil {
		_, bytes := sl.Acct().Pending()
		if bytes > readResumeBytes {
			return
		}
	}
	sl.ReadPaused = false
	if sl.Has(connection.FlagReadable) {
		if !l.armRecv(slot, gen) {
			l.park(slot, gen)
		}
	}
}
