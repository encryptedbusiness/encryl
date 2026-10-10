//go:build linux

package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/gorilla/websocket"
)

func openedConns(srv *Server) chan *Conn {
	ch := make(chan *Conn, 8)
	_ = srv.UseConnect(func(c *Conn) error {
		ch <- c
		return nil
	})
	return ch
}

func writeRawEnvelope(t *testing.T, c *websocket.Conn, e event.Envelope, payload []byte) {
	t.Helper()
	msg := event.EncodeMessage(e, payload)
	if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

func waitEvent(t *testing.T, c *websocket.Conn, want uint32) event.Envelope {
	t.Helper()
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || e.Event != want {
		t.Fatalf("event = %+v, want event %d", e, want)
	}
	return e
}

func TestServerSelfRoomEmit(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)

	a := dialWS(t, url)
	defer a.Close()
	ca := <-opened
	b := dialWS(t, url)
	defer b.Close()
	cb := <-opened
	if ca.ID() == cb.ID() {
		t.Fatalf("two connections share an ID: %+v", ca.ID())
	}

	if err := srv.ToConns(ca.ID()).EmitID(1024, []byte("one")); err != nil {
		t.Fatalf("ToConns Emit: %v", err)
	}
	e := waitEvent(t, a, 1024)
	if string(e.Payload) != "one" {
		t.Fatalf("payload = %q", e.Payload)
	}

	_ = b.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := b.ReadMessage(); err == nil {
		t.Fatal("targeted emit leaked to another connection")
	}
}

func TestServerEmitIDLocal(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.EmitIDLocal(1025, []byte("local"))
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 0, []byte("ping"))
	e := waitEvent(t, c, 1025)
	if string(e.Payload) != "local" {
		t.Fatalf("payload = %q", e.Payload)
	}
}

func TestServerEmitFrameLocal(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	responseCh := make(chan *connection.Frame, 1)
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		f := connection.BuildFrame(event.KindEvent, 1025, 0, 0, []byte("frame"))
		responseCh <- f
		return c.EmitFrameLocal(f)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 0, []byte("ping"))
	e := waitEvent(t, c, 1025)
	if string(e.Payload) != "frame" {
		t.Fatalf("payload = %q", e.Payload)
	}
	// EmitFrameLocal retains a queue reference; the caller's base reference
	// remains and is released here.
	response := <-responseCh
	response.Release()
}

func TestServerReply(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.Reply(m, []byte("pong"))
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 7, []byte("ping"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindAck || e.RequestID != 7 || string(e.Payload) != "pong" {
		t.Fatalf("reply = %+v", e)
	}
	_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("Reply did not suppress the automatic empty ACK")
	}
}

func TestServerUnknownEvent(t *testing.T) {
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

	// Unknown unsolicited events are dropped, v1/Socket.IO style. A known
	// event immediately after it must be the first frame on the wire.
	writeEvent(t, c, 9999, 0, []byte("x"))
	writeEvent(t, c, 1024, 0, []byte("keep"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || e.Event != 1024 || string(e.Payload) != "keep" {
		t.Fatalf("response = %+v", e)
	}
}

func TestServerMiddlewareOrder(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu       sync.Mutex
		order    []string
		finished = make(chan struct{})
	)
	if err := srv.Use(func(next EventHandler) EventHandler {
		return func(c *Conn, m Message) error {
			mu.Lock()
			order = append(order, "before")
			mu.Unlock()
			err := next(c, m)
			mu.Lock()
			order = append(order, "after")
			mu.Unlock()
			close(finished)
			return err
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		mu.Lock()
		order = append(order, "handler")
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 0, nil)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("middleware chain did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"before", "handler", "after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v", order)
		}
	}
}

func TestServerUseConnectReject(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UseConnect(func(c *Conn) error { return ErrUnauthorized }); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 1008 {
		t.Fatalf("close = %v, want 1008", err)
	}
}

func TestServerEmitWithAck(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)
	client := dialWS(t, url)
	defer client.Close()
	serverConn := <-opened

	acks := make(chan Ack, 1)
	if err := serverConn.EmitWithAckID(1024, []byte("question"), time.Second, func(a Ack) {
		acks <- a
	}); err != nil {
		t.Fatalf("EmitWithAck: %v", err)
	}

	e := waitEvent(t, client, 1024)
	if e.RequestID == 0 {
		t.Fatal("request id is zero")
	}
	if string(e.Payload) != "question" {
		t.Fatalf("payload = %q", e.Payload)
	}
	writeRawEnvelope(t, client, event.Envelope{
		Kind:      event.KindAck,
		Event:     1024,
		RequestID: e.RequestID,
	}, []byte("answer"))

	select {
	case a := <-acks:
		if a.Err != nil {
			t.Fatalf("ack err = %v", a.Err)
		}
		if a.Event != 1024 || string(a.Payload) != "answer" {
			t.Fatalf("ack = %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ack callback did not run")
	}
}

func TestServerEmitWithAckTimeout(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)
	client := dialWS(t, url)
	defer client.Close()
	serverConn := <-opened

	acks := make(chan Ack, 1)
	if err := serverConn.EmitWithAckID(1024, nil, 50*time.Millisecond, func(a Ack) {
		acks <- a
	}); err != nil {
		t.Fatalf("EmitWithAck: %v", err)
	}
	// Drain the event so the client does not block the connection.
	_ = waitEvent(t, client, 1024)

	select {
	case a := <-acks:
		if !errors.Is(a.Err, ErrAckTimeout) {
			t.Fatalf("err = %v, want ErrAckTimeout", a.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout callback did not run")
	}
}

func TestServerWriteQueueBurst(t *testing.T) {
	cfg := testServerConfig()
	cfg.WriteQueueMessages = 64
	cfg.WriteQueueBytes = (1 << 20) + 64
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const burst = 20
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		for i := 0; i < burst; i++ {
			if err := c.EmitID(1024, m.Payload); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 0, []byte("burst"))
	for i := 0; i < burst; i++ {
		e := waitEvent(t, c, 1024)
		if string(e.Payload) != "burst" {
			t.Fatalf("frame %d payload = %q", i, e.Payload)
		}
	}
	st := srv.Stats()
	if st.DroppedMessages != 0 || st.Backpressure != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestServerRoomExcept(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan *Conn, 8)
	if err := srv.UseConnect(func(c *Conn) error {
		if err := c.Join("chat"); err != nil {
			return err
		}
		opened <- c
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	a := dialWS(t, url)
	defer a.Close()
	ca := <-opened
	b := dialWS(t, url)
	defer b.Close()
	cb := <-opened
	c := dialWS(t, url)
	defer c.Close()
	cc := <-opened

	deadline := time.Now().Add(2 * time.Second)
	for srv.RoomSize("chat") < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.RoomSize("chat"); got != 3 {
		t.Fatalf("room size = %d", got)
	}

	if err := srv.To("chat").Except(ca.ID()).EmitID(1024, []byte("hello")); err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, conn := range []*websocket.Conn{b, c} {
		e := waitEvent(t, conn, 1024)
		if string(e.Payload) != "hello" {
			t.Fatalf("payload = %q", e.Payload)
		}
	}
	_ = a.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := a.ReadMessage(); err == nil {
		t.Fatal("excluded connection received the frame")
	}
	_ = cb
	_ = cc
}

func TestServerRoomsIterator(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	if err := srv.OnID(1024, func(c *Conn, m Message) error { return c.Join("x") }); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()
	sc := <-opened

	writeEvent(t, c, 1024, 0, nil)
	deadline := time.Now().Add(2 * time.Second)
	for srv.RoomSize("x") != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if srv.RoomSize("x") != 1 {
		t.Fatal("join did not happen")
	}

	var names []string
	sc.Rooms(func(name string) bool {
		names = append(names, name)
		return true
	})
	foundSelf, foundRoom := false, false
	for _, n := range names {
		if n == "x" {
			foundRoom = true
		}
		if n != "" && n != "x" {
			foundSelf = true
		}
	}
	if !foundSelf || !foundRoom {
		t.Fatalf("rooms = %v", names)
	}
}

func TestServerAckRejectsForgedToken(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)

	a := dialWS(t, url)
	defer a.Close()
	ca := <-opened
	b := dialWS(t, url)
	defer b.Close()
	<-opened

	acks := make(chan Ack, 2)
	if err := ca.EmitWithAckID(1024, []byte("question"), 2*time.Second, func(ack Ack) {
		acks <- ack
	}); err != nil {
		t.Fatalf("EmitWithAck: %v", err)
	}
	sent := waitEvent(t, a, 1024)

	// A different connection on the same loop forges the token.
	writeRawEnvelope(t, b, event.Envelope{
		Kind:      event.KindAck,
		Event:     1024,
		RequestID: sent.RequestID,
	}, []byte("forged"))
	select {
	case ack := <-acks:
		t.Fatalf("forged token completed the callback: %+v", ack)
	case <-time.After(200 * time.Millisecond):
	}
	if st := srv.Stats(); st.StaleAcks == 0 {
		t.Fatal("forged token was not counted as stale")
	}

	// The owner can still complete its own entry.
	writeRawEnvelope(t, a, event.Envelope{
		Kind:      event.KindAck,
		Event:     1024,
		RequestID: sent.RequestID,
	}, []byte("real"))
	select {
	case ack := <-acks:
		if ack.Err != nil || string(ack.Payload) != "real" {
			t.Fatalf("ack = %+v", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner ack did not complete")
	}
}

func TestServerEmitWithAckNeverSilentlyDrops(t *testing.T) {
	cfg := testServerConfig()
	cfg.WriteQueueMessages = 1
	cfg.WriteQueueBytes = (1 << 20) + 64
	cfg.Backpressure = BackpressureDropNewest
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gotErr := make(chan error, 1)
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		// The handler runs on the loop, so nothing drains while it fills the
		// queue synchronously.
		_ = c.EmitID(1024, m.Payload)
		gotErr <- c.EmitWithAckID(1024, m.Payload, time.Second, func(Ack) {})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 0, []byte("x"))
	select {
	case err := <-gotErr:
		if !errors.Is(err, ErrBackpressure) {
			t.Fatalf("EmitWithAck = %v, want ErrBackpressure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not run")
	}
}

func TestServerReplyChecksSize(t *testing.T) {
	cfg := testServerConfig()
	cfg.ReadBufferSize = 512
	cfg.MaxFrameSize = 512
	cfg.MaxMessageSize = 1024
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gotErr := make(chan error, 1)
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		gotErr <- c.Reply(m, make([]byte, 2000))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, 1024, 7, []byte("x"))
	select {
	case err := <-gotErr:
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("Reply = %v, want ErrPayloadTooLarge", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not run")
	}
}

type closeRecorder struct {
	ch chan CloseInfo
}

func (*closeRecorder) OnOpen(*Conn) error             { return nil }
func (*closeRecorder) OnMessage(*Conn, Message) error { return nil }
func (r *closeRecorder) OnClose(_ *Conn, info CloseInfo) {
	select {
	case r.ch <- info:
	default:
	}
}

func TestServerCloseInfoRemote(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	rec := &closeRecorder{ch: make(chan CloseInfo, 1)}
	if err := srv.SetHandler(rec); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)

	if err := c.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(1000, "bye"), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	select {
	case info := <-rec.ch:
		if !info.Remote {
			t.Fatalf("CloseInfo.Remote = false for a peer-initiated close: %+v", info)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnClose did not run")
	}
	_ = c.Close()
}

func TestServerVolatileDeliversWhenIdle(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()
	sc := <-opened

	if err := srv.ToConns(sc.ID()).Volatile().EmitID(1024, []byte("v")); err != nil {
		t.Fatalf("volatile emit: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, data, err := c.ReadMessage()
	if err != nil {
		q := -1
		if st := srv.state.Load(); st != nil && len(st.loops) > 0 {
			q = st.loops[0].QueueLen()
		}
		t.Fatalf("volatile read: %v stats=%+v id=%+v queuelen=%d", err, srv.Stats(), sc.ID(), q)
	}
	e, err := event.Decode(data)
	if err != nil || string(e.Payload) != "v" {
		t.Fatalf("payload = %q err=%v", e.Payload, err)
	}
}
