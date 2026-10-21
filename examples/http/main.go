//go:build linux

// Command http demonstrates encryl behind a net/http server.
//
// The HTTP server owns the listener. The /ws handler hijacks an upgraded
// connection and hands the descriptor to encryl.
//
//	go run ./examples/http
//	websocat ws://127.0.0.1:18090/ws
//
// On Linux only; the reactor is io_uring-only.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18090", "HTTP listen address")
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
	mux.Handle("/ws", srv)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	hs := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("net/http example: HTTP http://%s/  WebSocket ws://%s/ws", *addr, *addr)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
