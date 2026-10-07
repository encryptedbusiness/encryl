//go:build linux

package reactor

import (
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"golang.org/x/sys/unix"
)

// attach installs an already-upgraded socket. The framework has validated the
// handshake, so this path queues the 101 directly; there is no HTTP parser and
// no request buffer.
func (l *loop) drainAttach() {
	for {
		c, ok := l.attachRing.Pop()
		if !ok {
			return
		}
		l.attach(c)
	}
}

// dropAttach closes handoffs that were queued but never installed. It runs on
// loop exit so a shutdown race cannot strand a duplicated descriptor.
func (l *loop) dropAttach() {
	for {
		c, ok := l.attachRing.Pop()
		if !ok {
			return
		}
		if c.FD >= 0 {
			_ = unix.Close(c.FD)
		}
	}
}

func (l *loop) attach(c AttachCommand) {
	fd := c.FD
	if fd < 0 {
		return
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		l.c.acceptErrors.Add(1)
		return
	}
	spec := c.Spec

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
		_ = unix.Close(fd)
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
	sl.Encoding = spec.Encoding
	sl.WS.InitLimits(&l.wsLimits)

	l.c.live.Add(1)
	l.c.accepted.Add(1)
	l.armTimer(slot, connection.TimerHandshake, l.now+uint64(l.cfg.HandshakeTimeout/time.Millisecond))

	n := int(spec.SubprotocolLen)
	if n > len(spec.Subprotocol) {
		n = len(spec.Subprotocol)
	}
	if !l.queue101(slot, gen, spec.Accept, spec.Subprotocol[:n]) {
		return
	}
	if len(c.Data) > 0 {
		sl.SetPendingOwned(c.Data)
	}
	l.armPoll(slot, gen)
}
