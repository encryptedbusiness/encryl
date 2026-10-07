//go:build linux

package reactor

import (
	"syscall"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/uring/giouring"
	"golang.org/x/sys/unix"
)

type acceptDesc struct {
	inUse bool
}

// ackTimer is the wheel handle for one pending acknowledgement plus the slab
// generation it was armed for. The generation is carried through the timer so
// a stale fire cannot consume a rebound entry.
func (l *loop) armAccepts() {
	if l.stop.Load() || l.ln == nil {
		return
	}
	for i := range l.accepts {
		if !l.accepts[i].inUse {
			l.armAccept(i)
		}
	}
}

func (l *loop) armAccept(idx int) {
	if l.stop.Load() {
		return
	}
	as := &l.accepts[idx]
	if as.inUse {
		return
	}
	sqe := l.getSQE()
	if sqe == nil {
		return
	}
	as.inUse = true
	sqe.PrepareAccept(l.lnFD, 0, 0, uint32(unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC))
	sqe.UserData = EncodeUserData(OpAccept, uint32(idx), 0)
	l.c.submissions.Add(1)
}

func (l *loop) handleAccept(cqe *giouring.CompletionQueueEvent, idx uint32) {
	if int(idx) >= len(l.accepts) {
		l.c.stale.Add(1)
		return
	}
	as := &l.accepts[idx]
	as.inUse = false

	if cqe.Res < 0 {
		err := syscall.Errno(-cqe.Res)
		if err == unix.ECANCELED || err == unix.EBADF {
			return
		}
		if err != unix.EAGAIN {
			l.c.acceptErrors.Add(1)
			l.logf("accept error: %v", err)
		}
		l.armAccept(int(idx))
		return
	}

	fd := int(cqe.Res)
	l.armAccept(int(idx))
	l.acceptConn(fd)
}

func (l *loop) acceptConn(fd int) {
	_ = netio.TuneAcceptedFD(fd)

	var (
		ip     [16]byte
		port   uint16
		family uint8
	)
	if sa, err := unix.Getpeername(fd); err == nil {
		ip, port, family = peerInfo(sa)
	}

	slot, gen, ok := l.slab.Acquire(fd)
	if !ok {
		unix.Close(fd)
		l.c.acceptErrors.Add(1)
		return
	}
	if !l.registerFile(slot, fd) {
		_ = unix.Close(fd)
		_ = l.slab.Transition(slot, connection.StateClosed)
		_, _ = l.slab.Release(slot)
		l.c.acceptErrors.Add(1)
		return
	}
	// The file table now owns the socket. The process descriptor is no longer
	// needed; the direct index is the slot index.
	if err := unix.Close(fd); err != nil {
		l.c.fileTableErrs.Add(1)
	}
	if !l.slab.Transition(slot, connection.StateHandshaking) {
		l.c.illegal.Add(1)
	}
	l.slab.SetPeer(slot, ip[:], port, family)
	sl, _ := l.slab.GetAny(slot)
	sl.FD = int32(slot)
	sl.Direct = true
	if l.cfg.RawEcho {
		sl.SetAcct(&connection.Accounting{})
	}
	sl.SetHandshake(l.acquireHandshake())
	sl.WS.InitLimits(&l.wsLimits)

	l.c.live.Add(1)
	l.c.accepted.Add(1)

	if l.cfg.RawEcho {
		l.armTimer(slot, connection.TimerIdle, l.now+uint64(l.cfg.IdleTimeout/time.Millisecond))
	} else {
		l.armTimer(slot, connection.TimerHandshake, l.now+uint64(l.cfg.HandshakeTimeout/time.Millisecond))
	}
	l.armPoll(slot, gen)
	if l.cfg.RawEcho && l.cfg.OnOpen != nil {
		l.cfg.OnOpen(slot, gen, sl.Acct(), ip, port, family, 0)
	}
}

func peerInfo(sa unix.Sockaddr) (ip [16]byte, port uint16, family uint8) {
	switch s := sa.(type) {
	case *unix.SockaddrInet4:
		copy(ip[:], s.Addr[:])
		return ip, uint16(s.Port), 4
	case *unix.SockaddrInet6:
		copy(ip[:], s.Addr[:])
		return ip, uint16(s.Port), 6
	default:
		return ip, 0, 0
	}
}
