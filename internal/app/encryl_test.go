//go:build linux

package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewServerStartsNoWork(t *testing.T) {
	linuxOnly(t)
	srv, err := New(loopbackConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.Stats().ConnectionsOpen != 0 {
		t.Fatal("New must not open connections")
	}
	if time.Since(srv.Stats().StartedAt) < 0 {
		t.Fatal("StartedAt is in the future")
	}
}

func TestOn(t *testing.T) {
	linuxOnly(t)
	srv, err := New(loopbackConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := func(*Conn, Message) error { return nil }
	if err := srv.OnID(7, h); err != nil {
		t.Fatalf("On: %v", err)
	}
	if err := srv.OnID(7, h); err == nil {
		t.Fatal("duplicate On must fail")
	}
	if err := srv.OnID(8, nil); err == nil {
		t.Fatal("nil EventHandler must fail")
	}
}

func TestSetHandlerOnlyOnce(t *testing.T) {
	linuxOnly(t)
	srv, err := New(loopbackConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &nopHandler{}
	if err := srv.SetHandler(h); err != nil {
		t.Fatalf("SetHandler: %v", err)
	}
	if err := srv.SetHandler(h); err == nil {
		t.Fatal("second SetHandler must fail")
	}
	if err := srv.SetHandler(nil); err == nil {
		t.Fatal("nil Handler must fail")
	}
}

func TestConnZeroValueIsSafe(t *testing.T) {
	var c *Conn
	if err := c.EmitID(1, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil Conn Send = %v, want ErrClosed", err)
	}
	if err := c.Join("room"); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil Conn Join = %v, want ErrClosed", err)
	}
	if err := c.Close(1000, ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil Conn Close = %v, want ErrClosed", err)
	}
	if c.ID() != (ConnID{}) {
		t.Fatalf("nil Conn ID = %+v, want zero", c.ID())
	}
	if c.RemoteAddr() != nil {
		t.Fatal("nil Conn RemoteAddr must be nil")
	}
}

func TestListenAndServeShutdown(t *testing.T) {
	linuxOnly(t)
	cfg := loopbackConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Loops = 1
	cfg.MaxConnections = 64
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for srv.state.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if srv.state.Load() == nil {
		t.Fatal("server did not start")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not return")
	}
}

func TestConfigCopyIsIsolated(t *testing.T) {
	linuxOnly(t)
	cfg := loopbackConfig()
	cfg.AllowedOrigins = []string{"https://a.example"}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := srv.Config()
	got.AllowedOrigins[0] = "https://evil.example"

	if again := srv.Config(); again.AllowedOrigins[0] != "https://a.example" {
		t.Fatalf("Config() leaked a mutable slice: %q", again.AllowedOrigins[0])
	}
}

type nopHandler struct{}

func (*nopHandler) OnOpen(*Conn) error             { return nil }
func (*nopHandler) OnMessage(*Conn, Message) error { return nil }
func (*nopHandler) OnClose(*Conn, CloseInfo)       {}
