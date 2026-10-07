//go:build linux

package reactor

import (
	"errors"
	"syscall"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	upgrade "github.com/encryptedbusiness/encryl/httpx"
	"github.com/encryptedbusiness/encryl/uring/giouring"
	"golang.org/x/sys/unix"
)

func (l *loop) armPoll(slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		return
	}
	if sl.Has(connection.FlagPollArmed) {
		return
	}
	sqe := l.getSQE()
	if sqe == nil {
		l.closeConn(slot, gen, 1011)
		return
	}
	if sl.Direct {
		sqe.PreparePollMultishotFixed(uint32(sl.FD), pollMask)
	} else {
		sqe.PreparePollMultishot(int(sl.FD), pollMask)
	}
	sqe.UserData = EncodeUserData(OpPoll, slot, gen)
	sl.Set(connection.FlagPollArmed)
	l.slab.BeginOp(slot)
	l.c.submissions.Add(1)
}

func (l *loop) handlePoll(cqe *giouring.CompletionQueueEvent, slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		l.c.stale.Add(1)
		return
	}

	if cqe.Res < 0 {
		if sl.Has(connection.FlagPollArmed) {
			sl.Clear(connection.FlagPollArmed)
			l.endOp(slot, gen)
		}
		if cqe.Res == -int32(unix.ECANCELED) {
			l.maybeClose(slot, gen)
			return
		}
		l.c.recvErrors.Add(1)
		l.logf("poll error slot=%d gen=%d: %v", slot, gen, syscall.Errno(-cqe.Res))
		l.closeConn(slot, gen, 1002)
		return
	}

	if cqe.Flags&giouring.CQEFMore == 0 {
		sl.Clear(connection.FlagPollArmed)
		l.endOp(slot, gen)
	}

	mask := uint32(cqe.Res)
	if mask&(unix.POLLIN|unix.POLLERR|unix.POLLHUP|unix.POLLRDHUP) != 0 {
		sl.Set(connection.FlagReadable)
	}
	if sl.Has(connection.FlagReadable) && !sl.ReadPaused {
		if !l.armRecv(slot, gen) {
			l.park(slot, gen)
		}
	}
	if !sl.Has(connection.FlagPollArmed) && !l.stop.Load() {
		l.armPoll(slot, gen)
	}
}

func (l *loop) armRecv(slot, gen uint32) bool {
	sl, ok := l.slab.Get(slot, gen)
	if !ok || sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		return true
	}
	if sl.ReadPaused {
		return true
	}
	if sl.Has(connection.FlagRecvArmed) {
		return true
	}
	bid, ok := l.rx.Take()
	if !ok {
		l.c.bufExhausted.Add(1)
		return false
	}
	sqe := l.getSQE()
	if sqe == nil {
		l.rx.Put(bid)
		return false
	}
	if sl.Direct {
		sqe.PrepareRecvFixed(uint32(sl.FD), l.rx.Addr(bid), uint32(l.rx.Size()), 0)
	} else {
		sqe.PrepareRecv(int(sl.FD), l.rx.Addr(bid), uint32(l.rx.Size()), 0)
	}
	sqe.UserData = EncodeUserData(OpRecv, slot, gen)
	sl.RecvBid = bid
	sl.Set(connection.FlagRecvArmed)
	l.slab.BeginOp(slot)
	l.c.submissions.Add(1)
	return true
}

func (l *loop) handleRecv(cqe *giouring.CompletionQueueEvent, slot, gen uint32) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		l.c.stale.Add(1)
		return
	}
	bid := sl.RecvBid
	sl.RecvBid = -1
	sl.Clear(connection.FlagRecvArmed)
	if bid >= 0 {
		defer func() {
			l.rx.Put(bid)
			l.wakeParked()
		}()
	}
	l.endOp(slot, gen)

	if sl.State == connection.StateClosing || sl.State == connection.StateClosed {
		l.maybeClose(slot, gen)
		return
	}
	if bid < 0 {
		l.c.recvErrors.Add(1)
		l.closeConn(slot, gen, 1011)
		return
	}
	if cqe.Res < 0 {
		switch syscall.Errno(-cqe.Res) {
		case unix.EAGAIN:
			sl.Clear(connection.FlagReadable)
		case unix.ECANCELED:
			l.maybeClose(slot, gen)
		default:
			l.c.recvErrors.Add(1)
			l.logf("recv error slot=%d gen=%d: %v", slot, gen, syscall.Errno(-cqe.Res))
			l.closeConn(slot, gen, 1002)
		}
		return
	}
	if cqe.Res == 0 {
		sl.CloseRemote = true
		l.closeConn(slot, gen, 1000)
		return
	}

	n := int(cqe.Res)
	l.c.bytesIn.Add(uint64(n))
	l.armTimer(slot, connection.TimerIdle, l.now+uint64(l.cfg.IdleTimeout/time.Millisecond))
	data := l.rx.Bytes(bid)[:n]

	if l.cfg.RawEcho {
		l.echo(slot, gen, data)
	} else {
		l.handleData(slot, gen, data)
	}

	if n == l.rx.Size() {
		if !l.armRecv(slot, gen) {
			l.park(slot, gen)
		}
	}
}

func (l *loop) echo(slot, gen uint32, data []byte) {
	f := connection.NewFrame(len(data))
	copy(f.Buf, data)
	f.Counted = false
	l.enqueueOwned(slot, gen, f)
}

func (l *loop) handleData(slot, gen uint32, data []byte) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return
	}
	switch sl.State {
	case connection.StateHandshaking:
		if sl.Has(connection.Flag101Queued) {
			sl.SetPending(append(sl.Pending(), data...))
			return
		}
		p := sl.Handshake()
		if p == nil {
			l.closeConn(slot, gen, 1008)
			return
		}
		res, consumed, err := p.Feed(data)
		if err != nil {
			l.releaseHandshake(sl)
			var rej *upgrade.RejectError
			if errors.As(err, &rej) {
				l.sendHTTPReject(slot, gen, rej.Status, rej.Reason)
			} else {
				l.closeConn(slot, gen, 1008)
			}
			return
		}
		if res == upgrade.NeedMore {
			return
		}
		if res != upgrade.Complete {
			l.releaseHandshake(sl)
			l.closeConn(slot, gen, 1008)
			return
		}
		req := p.Request()
		if !l.originAllowed(req) {
			l.releaseHandshake(sl)
			l.sendHTTPReject(slot, gen, 403, "origin not allowed")
			return
		}
		sl.Encoding = req.Encoding
		ok = l.queue101(slot, gen, req.Accept, req.Subprotocol)
		l.releaseHandshake(sl)
		if !ok {
			return
		}
		if consumed < len(data) {
			sl.SetPending(append(sl.Pending()[:0], data[consumed:]...))
		}
	case connection.StateOpen:
		l.feedWS(slot, gen, data)
	}
}
