//go:build linux

package fasthttpadapter_test

import (
	"context"
	"net"
	"testing"
	"time"

	encryl "github.com/encryptedbusiness/encryl"
	fasthttpadapter "github.com/encryptedbusiness/encryl/adapters/fasthttp"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/gorilla/websocket"
	"github.com/valyala/fasthttp"
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

func TestFasthttpAttached(t *testing.T) {
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
	go func() { _ = fasthttp.Serve(ln, fasthttpadapter.Handler(srv)) }()
	defer ln.Close()

	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second, Subprotocols: []string{"encryl.v2"}}
	c, _, err := d.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	msg := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: 1024, RequestID: 9}, []byte("ping"))
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
	if e.Kind != event.KindAck || e.RequestID != 9 || string(e.Payload) != "pong" {
		t.Fatalf("reply = %+v", e)
	}
}
