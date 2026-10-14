//go:build linux

package app

import (
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
)

func TestTopicEcho(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	echo := srv.Topic("echo")
	if err := echo.On(func(c *Conn, data []byte) {
		_ = echo.Emit(c, data)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialWS(t, url)
	defer c.Close()

	writeEvent(t, c, echo.ID(), 0, []byte("hello"))
	e := readEnvelope(t, c)
	if e.Kind != event.KindEvent || e.Event != echo.ID() || string(e.Payload) != "hello" {
		t.Fatalf("envelope = %+v", e)
	}
}

func TestTopicRoom(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnConnect(func(c *Conn) error { return c.Join("room") }); err != nil {
		t.Fatal(err)
	}
	chat := srv.Topic("chat")
	if err := chat.On(func(c *Conn, data []byte) {
		_ = chat.To(c, "room").Emit(data)
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

	writeEvent(t, a, chat.ID(), 0, []byte("hello"))
	e := readEnvelope(t, b)
	if e.Event != chat.ID() || string(e.Payload) != "hello" {
		t.Fatalf("envelope = %+v", e)
	}
}
