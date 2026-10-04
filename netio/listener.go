//go:build linux

package netio

import (
	"fmt"
	"net"
	"sync"

	"golang.org/x/sys/unix"
)

// Listener is a raw listening socket owned by the reactor. It is created with
// Listen so the backlog and SO_REUSEPORT options are honored.
type Listener interface {
	FD() int
	Addr() net.Addr
	Close() error
}

type listener struct {
	mu   sync.Mutex
	fd   int
	addr net.Addr
}

func (l *listener) FD() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fd
}

func (l *listener) Addr() net.Addr { return l.addr }

func (l *listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd < 0 {
		return nil
	}
	err := unix.Close(l.fd)
	l.fd = -1
	return err
}

// Listen binds a TCP listener with an explicit backlog and optional
// SO_REUSEPORT. A port of zero is resolved before returning, which is how tests
// and the first loop discover an ephemeral port.
func Listen(addr string, reusePort bool, backlog int) (Listener, error) {
	if backlog <= 0 {
		backlog = 4096
	}
	ta, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("platform: resolve %q: %w", addr, err)
	}
	if ta.IP == nil {
		ta.IP = net.IPv4zero
	}

	var (
		family int
		sa     unix.Sockaddr
	)
	if ip4 := ta.IP.To4(); ip4 != nil {
		family = unix.AF_INET
		s := &unix.SockaddrInet4{Port: ta.Port}
		copy(s.Addr[:], ip4)
		sa = s
	} else {
		family = unix.AF_INET6
		s := &unix.SockaddrInet6{Port: ta.Port}
		copy(s.Addr[:], ta.IP.To16())
		sa = s
	}

	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("platform: socket: %w", err)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("platform: SO_REUSEADDR: %w", err)
	}
	if reusePort {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("platform: SO_REUSEPORT: %w", err)
		}
	}
	if family == unix.AF_INET6 && ta.IP.IsUnspecified() {
		if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("platform: IPV6_V6ONLY: %w", err)
		}
	}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("platform: bind %q: %w", addr, err)
	}
	if err := unix.Listen(fd, backlog); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("platform: listen: %w", err)
	}
	bound, err := unix.Getsockname(fd)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("platform: getsockname: %w", err)
	}

	return &listener{fd: fd, addr: sockaddrToTCP(bound)}, nil
}

func sockaddrToTCP(sa unix.Sockaddr) *net.TCPAddr {
	switch s := sa.(type) {
	case *unix.SockaddrInet4:
		ip := make(net.IP, 4)
		copy(ip, s.Addr[:])
		return &net.TCPAddr{IP: ip, Port: s.Port}
	case *unix.SockaddrInet6:
		ip := make(net.IP, 16)
		copy(ip, s.Addr[:])
		zone := ""
		if s.ZoneId != 0 {
			if iface, err := net.InterfaceByIndex(int(s.ZoneId)); err == nil {
				zone = iface.Name
			}
		}
		return &net.TCPAddr{IP: ip, Port: s.Port, Zone: zone}
	default:
		return nil
	}
}

// TuneAcceptedFD applies the socket options the reactor expects on an accepted
// TCP descriptor. It is one syscall per accept.
func TuneAcceptedFD(fd int) error {
	return unix.SetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1)
}
