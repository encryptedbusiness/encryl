//go:build linux

package fiberadapter_test

import (
	"context"
	"net"
	"testing"
	"time"

	encryl "github.com/encryptedbusiness/encryl"
	fiberadapter "github.com/encryptedbusiness/encryl/adapters/fiberv3"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/gofiber/fiber/v3"
	"github.com/gorilla/websocket"
)

func attachedConfig() encryl.Config {
	cfg := encryl.DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Loops = 1
	cfg.MaxConnections = 64
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

func TestFiberV3Attached(t *testing.T) {
	srv, err := encryl.New(attachedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *encryl.Conn, m encryl.Message) error {
		return c.Reply(m, []byte("pong"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Get("/ws", fiberadapter.Handler(srv))
	go func() { _ = app.Listener(ln) }()
	defer func() { _ = app.Shutdown() }()

	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second, Subprotocols: []string{"encryl.v2"}}
	c, _, err := d.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	msg := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: 1024, RequestID: 11}, []byte("ping"))
	if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	e, err := event.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != event.KindAck || e.RequestID != 11 || string(e.Payload) != "pong" {
		t.Fatalf("reply = %+v", e)
	}
}
