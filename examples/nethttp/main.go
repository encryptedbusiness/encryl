//go:build linux

// Command nethttp demonstrates an explicit net.Listener owned by net/http
// while encryl runs its reactor and io_uring loops in attached mode.
//
// The listener is created once, passed to http.Server.Serve, and only the
// /ws route is handed to encryl. All other paths stay ordinary net/http.
//
//	go run ./examples/nethttp
//	curl http://127.0.0.1:18092/healthz
//	websocat ws://127.0.0.1:18092/ws
//
// On Linux only; the reactor is io_uring-only.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18092", "HTTP listen address")
	loops := flag.Int("loops", 0, "event loops; 0 uses NumCPU")
	flag.Parse()

	cfg := encryl.DefaultConfig()
	cfg.Loops = *loops
	cfg.MaxConnections = 100_000
	cfg.AllowedOrigins = []string{"*"}

	srv, err := encryl.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.OnConnect(func(c *encryl.Conn) error {
		return c.Join("lobby")
	}); err != nil {
		log.Fatal(err)
	}
	if err := srv.On("echo", func(c *encryl.Conn, data []byte) {
		_ = c.Emit("echo", data)
	}); err != nil {
		log.Fatal(err)
	}
	if err := srv.On("broadcast", func(c *encryl.Conn, data []byte) {
		_ = c.To("lobby").Emit("broadcast", data)
	}); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /ws", srv)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("encryl nethttp example\nWebSocket: /ws\n"))
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	hs := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("nethttp example: HTTP http://%s/  WebSocket ws://%s/ws", *addr, *addr)
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Printf("HTTP server: %v", err)
		}
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdown)
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("encryl shutdown: %v", err)
	}
}
