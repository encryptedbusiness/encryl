//go:build linux

package reactor

import (
	"bytes"
	"context"
	"errors"
	"github.com/encryptedbusiness/encryl/netio"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testWSLoop(t *testing.T, origins []string) (Loop, *loop) {
	t.Helper()
	ln, err := netio.Listen("127.0.0.1:0", false, 256)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	l, err := NewLoop(LoopConfig{
		Listener:         ln,
		ReadBufferSize:   4096,
		BufferCount:      64,
		TxBytes:          1 << 20,
		MaxConnections:   256,
		RingEntries:      256,
		MaxHeaderBytes:   8192,
		MaxFrameSize:     1 << 20,
		MaxMessageSize:   4 << 20,
		MaxFragments:     128,
		AllowedOrigins:   origins,
		EchoMessages:     true,
		IdleTimeout:      30 * time.Second,
		HandshakeTimeout: 5 * time.Second,
		ShutdownTimeout:  2 * time.Second,
		Logf:             t.Logf,
	})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	lp := l.(*loop)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = l.Shutdown()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("loop did not stop")
		}
	})
	return l, lp
}

func wsDial(t *testing.T, l Loop, header http.Header) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Subprotocols:     []string{"encryl.v2"},
	}
	c, resp, err := d.Dial("ws://"+l.Addr().String()+"/ws", header)
	if err != nil {
		if resp != nil {
			t.Fatalf("Dial: %v (status %d)", err, resp.StatusCode)
		}
		t.Fatalf("Dial: %v", err)
	}
	if c.Subprotocol() != "encryl.v2" {
		c.Close()
		t.Fatalf("subprotocol = %q", c.Subprotocol())
	}
	return c
}

func TestWebSocketBinaryEcho(t *testing.T) {
	l, _ := testWSLoop(t, []string{"*"})
	c := wsDial(t, l, nil)
	defer c.Close()

	if err := c.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	kind, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if kind != websocket.BinaryMessage || string(msg) != "hello" {
		t.Fatalf("echo = kind %d %q", kind, msg)
	}
	_ = c.Close()
	waitLive(t, l, 0)
}

func TestWebSocketLargeEcho(t *testing.T) {
	l, _ := testWSLoop(t, []string{"*"})
	c := wsDial(t, l, nil)
	defer c.Close()

	msg := bytes.Repeat([]byte("0123456789abcdef"), 8192) // 128 KiB
	if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if kind != websocket.BinaryMessage || !bytes.Equal(got, msg) {
		t.Fatalf("large echo mismatch: kind %d len %d", kind, len(got))
	}
}

func TestWebSocketPingPong(t *testing.T) {
	l, _ := testWSLoop(t, []string{"*"})
	c := wsDial(t, l, nil)
	defer c.Close()

	pong := make(chan string, 1)
	c.SetPongHandler(func(s string) error {
		pong <- s
		return nil
	})
	if err := c.WriteControl(websocket.PingMessage, []byte("ping!"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	go func() {
		_, _, _ = c.ReadMessage()
	}()
	select {
	case s := <-pong:
		if s != "ping!" {
			t.Fatalf("pong = %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pong")
	}
}

func TestWebSocketCloseHandshake(t *testing.T) {
	l, _ := testWSLoop(t, []string{"*"})
	c := wsDial(t, l, nil)
	defer c.Close()

	if err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "bye")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 1000 {
		t.Fatalf("close error = %v", err)
	}
}

func TestWebSocketTextRejected(t *testing.T) {
	l, _ := testWSLoop(t, []string{"*"})
	c := wsDial(t, l, nil)
	defer c.Close()

	if err := c.WriteMessage(websocket.TextMessage, []byte("text")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 1003 {
		t.Fatalf("close error = %v, want 1003", err)
	}
}

func TestWebSocketOrigin(t *testing.T) {
	l, _ := testWSLoop(t, []string{"https://app.example.com"})

	good := http.Header{"Origin": []string{"https://app.example.com"}}
	c := wsDial(t, l, good)
	c.Close()

	bad := http.Header{"Origin": []string{"https://evil.example"}}
	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Subprotocols:     []string{"encryl.v2"},
	}
	conn, resp, err := d.Dial("ws://"+l.Addr().String()+"/ws", bad)
	if err == nil {
		conn.Close()
		t.Fatal("mismatched Origin was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestWebSocketHandshakeTimeout(t *testing.T) {
	ln, err := netio.Listen("127.0.0.1:0", false, 64)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLoop(LoopConfig{
		Listener:         ln,
		ReadBufferSize:   4096,
		BufferCount:      16,
		TxBytes:          1 << 20,
		MaxConnections:   16,
		RingEntries:      256,
		MaxHeaderBytes:   8192,
		AllowedOrigins:   []string{"*"},
		HandshakeTimeout: 100 * time.Millisecond,
		ShutdownTimeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	defer func() {
		cancel()
		_ = l.Shutdown()
		select {
		case <-done: //nolint:errcheck // wait for l.Run to return; the error is not asserted here
		case <-time.After(3 * time.Second):
			t.Error("loop did not stop")
		}
	}()

	c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("handshake timeout did not close the connection")
	}
}
