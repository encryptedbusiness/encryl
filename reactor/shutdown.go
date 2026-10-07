//go:build linux

package reactor

import (
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"golang.org/x/sys/unix"
)

func (l *loop) progressShutdown() {
	if !l.shuttingDown {
		l.shuttingDown = true
		l.shutdownStart = l.now
		if l.ln != nil {
			_ = l.ln.Close()
			for i := range l.accepts {
				if !l.accepts[i].inUse {
					continue
				}
				sqe := l.getSQE()
				if sqe == nil {
					break
				}
				sqe.PrepareCancel64(EncodeUserData(OpAccept, uint32(i), 0), 0)
				sqe.UserData = EncodeUserData(OpCancel, uint32(i), 0)
				l.c.submissions.Add(1)
			}
		}
	}

	hw := l.slab.HighWater()
	for slot := uint32(0); slot < hw; slot++ {
		sl, ok := l.slab.GetAny(slot)
		if !ok {
			continue
		}
		l.closeConn(slot, sl.Gen, 1001)
	}

	if l.now-l.shutdownStart > uint64(l.cfg.ShutdownTimeout/time.Millisecond) {
		l.forceCloseAll()
	}
}

func (l *loop) forceCloseAll() {
	hw := l.slab.HighWater()
	for slot := uint32(0); slot < hw; slot++ {
		sl, ok := l.slab.GetAny(slot)
		if !ok {
			continue
		}
		l.disarmAllTimers(slot)
		l.releaseHandshake(sl)
		l.releasePending(sl)
		if sl.SendFrame != nil {
			l.releaseFrame(sl, sl.SendFrame)
			sl.SendFrame = nil
		}
		if sl.SendBid >= 0 {
			l.tx.Release(sl.SendBid, slot, sl.Gen)
			sl.SendBid = -1
		}
		l.dropQueue(sl)
		if sl.FD >= 0 {
			if sl.Direct {
				l.clearFile(slot)
			} else {
				unix.Close(int(sl.FD))
			}
			sl.FD = -1
		}
		sl.State = connection.StateClosed
		sl.InFlight = 0
		if l.cfg.OnClose != nil {
			l.cfg.OnClose(slot, sl.Gen, sl.CloseCode, "", sl.CloseRemote)
		}
		if _, ok := l.slab.Release(slot); ok {
			l.c.closed.Add(1)
			l.c.live.Add(-1)
		}
	}
}
