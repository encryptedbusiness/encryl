//go:build linux

package app

import (
	"testing"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
)

func newRegistrationServer() *Server {
	return &Server{
		events:     event.NewRegistry[*Conn](),
		eventNames: protocol.NewEventRegistry(),
	}
}

func TestEventIDForNameStable(t *testing.T) {
	cases := map[string]uint32{
		"chat":         0xa24bf9ab,
		"chat.message": 0x5158bc5e,
		"ping":         0x165df089,
		"rpc.echo":     0x34830b6f,
	}
	for name, want := range cases {
		if got := EventIDForName(name); got != want {
			t.Fatalf("EventIDForName(%q) = %#x, want %#x", name, got, want)
		}
	}
}

func TestStringRegistration(t *testing.T) {
	srv := newRegistrationServer()
	h := func(c *Conn, data []byte) {}
	if err := srv.On("chat", h); err != nil {
		t.Fatal(err)
	}
	if got, ok := srv.eventNames.ID("chat"); !ok || got != EventIDForName("chat") {
		t.Fatalf("registry ID = %#x %v", got, ok)
	}
	if err := srv.On("chat", h); err == nil {
		t.Fatal("duplicate event accepted")
	}
	if err := srv.On("", h); err == nil {
		t.Fatal("empty event accepted")
	}
	if err := srv.On("nil", nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	if err := srv.OnRequest("chat", func(c *Conn, req []byte) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("event/request name collision accepted")
	}
	if err := srv.OnRequest("rpc", func(c *Conn, req []byte) ([]byte, error) { return req, nil }); err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(7, func(c *Conn, m Message) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestStringRegistrationAfterStart(t *testing.T) {
	srv := newRegistrationServer()
	srv.started.Store(true)
	if err := srv.On("chat", func(c *Conn, data []byte) {}); err == nil {
		t.Fatal("On accepted after start")
	}
	if err := srv.OnRequest("rpc", func(c *Conn, req []byte) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("OnRequest accepted after start")
	}
	if err := srv.OnConnect(func(c *Conn) error { return nil }); err == nil {
		t.Fatal("OnConnect accepted after start")
	}
	if err := srv.OnDisconnect(func(c *Conn) {}); err == nil {
		t.Fatal("OnDisconnect accepted after start")
	}
}

func TestStringCollision(t *testing.T) {
	srv := newRegistrationServer()
	if err := srv.RegisterEvent("a", 123); err != nil {
		t.Fatal(err)
	}
	if err := srv.RegisterEvent("b", 123); err == nil {
		t.Fatal("ID collision accepted")
	}
	if err := srv.RegisterEvent("a", 124); err == nil {
		t.Fatal("name collision accepted")
	}
}

func TestStringLifecycleRegistration(t *testing.T) {
	srv := newRegistrationServer()
	if err := srv.OnConnect(func(c *Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := srv.OnConnect(func(c *Conn) error { return nil }); err == nil {
		t.Fatal("duplicate connect handler accepted")
	}
	if err := srv.OnDisconnect(func(c *Conn) {}); err != nil {
		t.Fatal(err)
	}
	if err := srv.OnDisconnect(func(c *Conn) {}); err == nil {
		t.Fatal("duplicate disconnect handler accepted")
	}
}
