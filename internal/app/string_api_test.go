//go:build linux

package app

import (
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/gorilla/websocket"
)

func TestStringEventRoom(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnConnect(func(c *Conn) error { return c.Join("room") }); err != nil {
		t.Fatal(err)
	}
	if err := srv.On("chat", func(c *Conn, data []byte) {
		_ = c.To("room").Emit("chat", data)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	a := dialWS(t, url)
	defer a.Close()
	b := dialWS(t, url)
	defer b.Close()

	deadline := time.Now().Add(2 * time.Second)
	for srv.Stats().Memberships < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.Stats().Memberships; got != 2 {
		t.Fatalf("memberships = %d", got)
	}

	writeEvent(t, a, EventIDForName("chat"), 0, []byte("hello"))
	e := readEnvelope(t, b)
	if e.Kind != event.KindEvent || e.Event != EventIDForName("chat") || string(e.Payload) != "hello" {
		t.Fatalf("envelope = %+v", e)
	}
}

func TestStringEventSelfAndUserData(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnConnect(func(c *Conn) error {
		c.SetUserData("alice")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.On("who", func(c *Conn, data []byte) {
		name, _ := c.UserData().(string)
		_ = c.Emit("who", []byte(name))
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, EventIDForName("who"), 0, nil)
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || string(e.Payload) != "alice" {
		t.Fatalf("envelope = %+v", e)
	}
}

func TestStringOnRequest(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnRequest("rpc.echo", func(c *Conn, req []byte) ([]byte, error) {
		return append([]byte("ok:"), req...), nil
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, EventIDForName("rpc.echo"), 9, []byte("x"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindAck || e.RequestID != 9 || e.Event != EventIDForName("rpc.echo") {
		t.Fatalf("envelope = %+v", e)
	}
	if string(e.Payload) != "ok:x" {
		t.Fatalf("payload = %q", e.Payload)
	}
}

func TestStringAsk(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()
	sc := <-opened

	results := make(chan Ack, 1)
	if err := sc.Ask("rpc.ask", []byte("q"), time.Second, func(a Ack) {
		results <- a
	}); err != nil {
		t.Fatal(err)
	}
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || e.Event != EventIDForName("rpc.ask") {
		t.Fatalf("request envelope = %+v", e)
	}
	msg := event.EncodeMessage(event.Envelope{
		Kind:      event.KindAck,
		Event:     e.Event,
		RequestID: e.RequestID,
	}, []byte("answer"))
	if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-results:
		if a.Err != nil || string(a.Payload) != "answer" {
			t.Fatalf("ack = %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask callback did not run")
	}
}

func TestStringUnknownEventDropped(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.On("known", func(c *Conn, data []byte) {
		_ = c.Emit("known", data)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, EventIDForName("unknown"), 0, []byte("drop"))
	writeEvent(t, c, EventIDForName("known"), 0, []byte("keep"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || e.Event != EventIDForName("known") || string(e.Payload) != "keep" {
		t.Fatalf("first response = %+v", e)
	}
}

func TestStringProtobufPublishByName(t *testing.T) {
	srv, err := New(testProtoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.On("chat.pb", func(c *Conn, data []byte) {
		_ = c.ToRoom("room").Emit("chat.pb", data)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	a := dialProto(t, url)
	defer a.Close()
	b := dialProto(t, url)
	defer b.Close()
	connectProto(t, a)
	connectProto(t, b)
	sub := subscribeProto(t, a, 2, "room")
	subscribeProto(t, b, 2, "room")

	writeProto(t, a, protocol.Command{
		ID:   3,
		Kind: protocol.CommandPublish,
		Publish: protocol.Publish{
			RoomID:  sub.RoomID,
			Event:   protocol.EventRef{Name: []byte("chat.pb")},
			Payload: []byte("hello"),
		},
	})

	for _, r := range readProto(t, b) {
		if r.Push.Kind != protocol.PushEvent {
			continue
		}
		push := r.Push.Event
		if push.EventID != EventIDForName("chat.pb") || string(push.Room) != "room" || string(push.Payload) != "hello" {
			t.Fatalf("push = %+v", push)
		}
		return
	}
	t.Fatal("protobuf client did not receive the named event")
}

func TestStringDisconnect(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{}, 1)
	if err := srv.OnDisconnect(func(c *Conn) { closed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	_ = c.Close()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("OnDisconnect did not run")
	}
}
