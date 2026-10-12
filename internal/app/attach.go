//go:build linux

package app

import (
	"crypto/tls"
	"net"
	"syscall"

	"github.com/encryptedbusiness/encryl/reactor"
	"golang.org/x/sys/unix"
)

// Upgrade takes an already-hijacked connection and hands its descriptor to the
// reactor. The fd is duplicated with F_DUPFD_CLOEXEC; the caller retains
// ownership of conn and must close it. On success the reactor owns buffered.
func (s *Server) Upgrade(conn net.Conn, spec UpgradeSpec, buffered []byte) error {
	if s == nil || conn == nil || !s.running.Load() {
		return ErrClosed
	}
	if !spec.prepared {
		return ErrInvalidUpgrade
	}
	st := s.state.Load()
	if st == nil || len(st.loops) == 0 {
		return ErrClosed
	}
	select {
	case <-s.stopCh:
		return ErrClosed
	default:
	}

	base := conn
	if u, ok := conn.(interface{ UnsafeConn() net.Conn }); ok {
		base = u.UnsafeConn()
	}
	dup, err := duplicateConnFD(base)
	if err != nil {
		return err
	}

	idx := int((s.attachSeq.Add(1) - 1) % uint32(len(st.loops)))
	cmd := reactor.AttachCommand{
		FD: dup,
		Spec: reactor.AttachSpec{
			Accept:         spec.Accept,
			Subprotocol:    spec.Subprotocol,
			SubprotocolLen: spec.SubprotocolLen,
			Encoding:       spec.Encoding,
		},
		Data: buffered,
	}
	if !st.loops[idx].EnqueueAttach(cmd) {
		_ = unix.Close(dup)
		s.backpressure.Add(1)
		return ErrBackpressure
	}
	return nil
}

// duplicateConnFD takes a kernel reference to conn's socket. It deliberately
// avoids conn.File(): F_DUPFD_CLOEXEC is one syscall, preserves O_NONBLOCK,
// and does not introduce an os.File lifetime.
func duplicateConnFD(conn net.Conn) (int, error) {
	if _, ok := conn.(*tls.Conn); ok {
		return -1, ErrUnsupported
	}
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return -1, ErrUnsupported
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return -1, err
	}
	var (
		dup    int
		dupErr error
	)
	if err := raw.Control(func(fd uintptr) {
		dup, dupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return -1, err
	}
	if dupErr != nil {
		return -1, dupErr
	}
	if dup < 0 {
		return -1, ErrUnsupported
	}
	return dup, nil
}
