//go:build linux

package netio

import (
	"fmt"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

func TestListenResolveAndClose(t *testing.T) {
	l, err := Listen("127.0.0.1:0", false, 0)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if l.FD() < 0 {
		t.Fatalf("FD = %d", l.FD())
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 || !addr.IP.IsLoopback() {
		t.Fatalf("Addr = %v", l.Addr())
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if l.FD() != -1 {
		t.Fatalf("FD after close = %d", l.FD())
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestListenReusePort(t *testing.T) {
	first, err := Listen("127.0.0.1:0", true, 8)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer first.Close()

	port := first.Addr().(*net.TCPAddr).Port
	second, err := Listen(fmt.Sprintf("127.0.0.1:%d", port), true, 8)
	if err != nil {
		t.Fatalf("second Listen on port %d: %v", port, err)
	}
	defer second.Close()

	if second.FD() == first.FD() {
		t.Fatal("reuse-port listener returned the same descriptor")
	}
	if got := second.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("second port = %d, want %d", got, port)
	}
}

func TestListenRejectsBadAddresses(t *testing.T) {
	if _, err := Listen("not an address", false, 0); err == nil {
		t.Fatal("bad address accepted")
	}
	if _, err := Listen("127.0.0.1:70000", false, 0); err == nil {
		t.Fatal("out-of-range port accepted")
	}
}

func TestSockaddrToTCP(t *testing.T) {
	v4 := sockaddrToTCP(&unix.SockaddrInet4{Port: 8080, Addr: [4]byte{127, 0, 0, 1}})
	if v4 == nil || v4.Port != 8080 || !v4.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("v4 = %v", v4)
	}

	v6 := sockaddrToTCP(&unix.SockaddrInet6{Port: 9090, Addr: [16]byte{0: 0, 1: 0, 2: 0, 3: 0, 4: 0, 5: 0, 6: 0, 7: 0, 8: 0, 9: 0, 10: 0, 11: 0, 12: 0, 13: 0, 14: 0, 15: 1}})
	if v6 == nil || v6.Port != 9090 || !v6.IP.Equal(net.ParseIP("::1")) {
		t.Fatalf("v6 = %v", v6)
	}

	if got := sockaddrToTCP(&unix.SockaddrUnix{Name: "x"}); got != nil {
		t.Fatalf("unsupported sockaddr = %v", got)
	}
}

func TestTuneAcceptedFD(t *testing.T) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("Socket: %v", err)
	}
	defer unix.Close(fd)
	if err := TuneAcceptedFD(fd); err != nil {
		t.Fatalf("TuneAcceptedFD: %v", err)
	}
	nodelay, err := unix.GetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY)
	if err != nil {
		t.Fatalf("GetsockoptInt: %v", err)
	}
	if nodelay != 1 {
		t.Fatalf("TCP_NODELAY = %d", nodelay)
	}
}
