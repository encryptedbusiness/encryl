//go:build linux

package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"golang.org/x/sys/unix"
)

func startAttachedHTTP(t *testing.T, srv *Server) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		_ = srv.Shutdown(context.Background())
		t.Fatal(err)
	}
	hs := &http.Server{Handler: http.HandlerFunc(srv.ServeHTTP)}
	go func() { _ = hs.Serve(ln) }()

	cleanup := func() {
		_ = hs.Close()
		_ = srv.Shutdown(context.Background())
		cancel()
	}
	return "ws://" + ln.Addr().String() + "/ws", cleanup
}

func TestServeHTTPAttached(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.Reply(m, []byte("pong"))
	}); err != nil {
		t.Fatal(err)
	}
	url, cleanup := startAttachedHTTP(t, srv)
	defer cleanup()

	c := dialWS(t, url)
	defer c.Close()
	writeEvent(t, c, 1024, 7, []byte("ping"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindAck || e.RequestID != 7 || string(e.Payload) != "pong" {
		t.Fatalf("reply = %+v", e)
	}
}

func TestServeHTTPPipelinedFrame(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		got <- string(m.Payload)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	url, cleanup := startAttachedHTTP(t, srv)
	defer cleanup()

	addr := strings.TrimPrefix(strings.TrimSuffix(url, "/ws"), "ws://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	req := "GET /ws HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + testWSKey + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: encryl.v2\r\n\r\n"

	payload := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: 1024}, []byte("pipelined"))
	frame := maskedBinaryFrameForKey(payload)
	if _, err := conn.Write(append([]byte(req), frame...)); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "101") {
		t.Fatalf("status line = %q", line)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}

	select {
	case v := <-got:
		if v != "pipelined" {
			t.Fatalf("payload = %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipelined frame was not delivered")
	}
}

func TestServeHTTPRejectsBeforeHijack(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = http.Serve(ln, http.HandlerFunc(srv.ServeHTTP)) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStartShutdownAttached(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.running.Load() {
		t.Fatal("running after shutdown")
	}
	if err := srv.Start(context.Background()); !errors.Is(err, errAlreadyStarted) {
		t.Fatalf("second Start = %v", err)
	}
}

func TestDuplicateConnFD(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	dup, err := duplicateConnFD(server)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dup)

	flags, err := unix.FcntlInt(uintptr(dup), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("duplicate is not close-on-exec")
	}
	status, err := unix.FcntlInt(uintptr(dup), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if status&unix.O_NONBLOCK == 0 {
		t.Fatal("duplicate lost O_NONBLOCK")
	}

	_ = server.Close()
	if _, err := unix.Write(dup, []byte("x")); err != nil {
		t.Fatalf("write after closing original: %v", err)
	}
	got := make([]byte, 1)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[0] != 'x' {
		t.Fatalf("read %q", got)
	}
}

func TestDuplicateConnFDAllocs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	allocs := testing.AllocsPerRun(100, func() {
		fd, err := duplicateConnFD(server)
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
	})
	// net.Conn.SyscallConn allocates its short-lived rawConn wrapper. The
	// handoff itself adds no allocation; keep the wrapper overhead bounded.
	if allocs > 8 {
		t.Fatalf("duplicateConnFD allocs = %v, want <= 8", allocs)
	}
}

func TestDuplicateConnFDRejectsTLS(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	tc := tls.Client(client, &tls.Config{InsecureSkipVerify: true})
	if _, err := duplicateConnFD(tc); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicateConnFD(tls) = %v, want ErrUnsupported", err)
	}
}

func maskedBinaryFrameForKey(payload []byte) []byte {
	const maskLen = 4
	key := [maskLen]byte{1, 2, 3, 4}
	out := make([]byte, 0, 6+len(payload))
	out = append(out, 0x82)
	if len(payload) < 126 {
		out = append(out, 0x80|byte(len(payload)))
	} else {
		out = append(out, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	}
	out = append(out, key[:]...)
	for i, b := range payload {
		out = append(out, b^key[i%maskLen])
	}
	return out
}
