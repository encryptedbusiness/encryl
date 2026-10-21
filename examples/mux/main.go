//go:build linux

// Command mux demonstrates path-based HTTP routing with http.NewServeMux and
// encryl's attached reactor mode.
//
// HTTP paths:
//
//	GET  /                              usage
//	GET  /healthz                       liveness
//	GET  /api/stats                     encryl server stats as JSON
//	POST /api/rooms/{room}/broadcast    publish bytes to a room
//	GET  /ws                            WebSocket upgrade
//
//	go run ./examples/mux
//	curl http://127.0.0.1:18093/healthz
//	curl -X POST --data-binary 'hello' http://127.0.0.1:18093/api/rooms/lobby/broadcast
//	websocat ws://127.0.0.1:18093/ws
//
// On Linux only; the reactor is io_uring-only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18093", "HTTP listen address")
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
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(srv.Stats())
	})
	mux.HandleFunc("POST /api/rooms/{room}/broadcast", func(w http.ResponseWriter, r *http.Request) {
		room := r.PathValue("room")
		if room == "" {
			http.Error(w, "room is required", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if len(body) == 0 {
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}
		if err := srv.To(room).Emit("message", body); err != nil {
			if errors.Is(err, encryl.ErrPayloadTooLarge) {
				http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("encryl mux example\n\n" +
			"GET  /healthz\n" +
			"GET  /api/stats\n" +
			"POST /api/rooms/{room}/broadcast\n" +
			"GET  /ws\n"))
	})

	hs := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("mux example: HTTP http://%s/  WebSocket ws://%s/ws", *addr, *addr)
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
