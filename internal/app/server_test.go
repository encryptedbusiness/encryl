//go:build linux

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/gorilla/websocket"
)

func testServerConfig() Config {
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Loops = 1
	cfg.MaxConnections = 128
	cfg.ReadBufferSize = 4096
	cfg.BufferCount = 64
	cfg.TxBytes = 2 << 20
	cfg.AllowedOrigins = []string{"*"}
	cfg.HandshakeTimeout = 3 * time.Second
	cfg.IdleTimeout = time.Minute
	cfg.PingInterval = 10 * time.Second
	cfg.ShutdownTimeout = 3 * time.Second
	return cfg
}

func startTestServer(tb testing.TB, srv *Server) string {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for srv.state.Load() == nil && time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			cancel()
			tb.Fatalf("server did not start: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	st := srv.state.Load()
	if st == nil {
		cancel()
		tb.Fatal("server did not start")
	}
	tb.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				tb.Errorf("ListenAndServe: %v", err)
			}
		case <-time.After(3 * time.Second):
			tb.Error("ListenAndServe did not return")
		}
	})
	return "ws://" + st.loops[0].Addr().String() + "/ws"
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Subprotocols:     []string{"encryl.v2"},
	}
	c, _, err := d.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

func writeEvent(t *testing.T, c *websocket.Conn, eventID uint32, request uint64, payload []byte) {
	t.Helper()
	msg := event.EncodeMessage(event.Envelope{
		Kind:      event.KindEvent,
		Event:     eventID,
		RequestID: request,
	}, payload)
	if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

func readEnvelope(t *testing.T, c *websocket.Conn) event.Envelope {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	e, err := event.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return e
}

func TestServerACk(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error { return nil }); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 42, []byte("hi"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindAck || e.Event != 1024 || e.RequestID != 42 {
		t.Fatalf("ack = %+v", e)
	}
}

func TestServerHandlerSend(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.EmitID(1024, m.Payload)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 0, []byte("echo"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || e.Event != 1024 || string(e.Payload) != "echo" {
		t.Fatalf("event = %+v", e)
	}
	st := srv.Stats()
	if st.MessagesIn == 0 || st.MessagesOut == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

type roomLifecycle struct{}

func (*roomLifecycle) OnOpen(c *Conn) error { return c.Join("room") }
func (*roomLifecycle) OnMessage(*Conn, Message) error {
	return nil
}
func (*roomLifecycle) OnClose(*Conn, CloseInfo) {}

func TestServerEmitToRoom(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.SetHandler(&roomLifecycle{}); err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.To("room").EmitID(1024, m.Payload)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	a := dialWS(t, url)
	defer a.Close()
	b := dialWS(t, url)
	defer b.Close()

	// Give both OnOpen callbacks a moment to join the room.
	deadline := time.Now().Add(2 * time.Second)
	for srv.Stats().Memberships < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.Stats().Memberships; got < 2 {
		t.Fatalf("memberships = %d", got)
	}

	writeEvent(t, a, 1024, 0, []byte("fanout"))
	e := readEnvelope(t, b)
	if e.Kind != event.KindEvent || e.Event != 1024 || string(e.Payload) != "fanout" {
		t.Fatalf("fanout = %+v", e)
	}
}

func TestServerMalformedEnvelopeCloses(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	if err := c.WriteMessage(websocket.BinaryMessage, []byte("short")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 1002 {
		t.Fatalf("close error = %v", err)
	}
}
