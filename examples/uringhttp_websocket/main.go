//go:build linux

// Command uringhttp_websocket serves HTTP API routes and WebSocket upgrades on
// one io_uring listener. chi owns the HTTP routing/middleware; /ws is handed to
// encryl, whose reactor owns the upgraded socket after the 101.
//
//	go run ./examples/uringhttp_websocket
//	curl http://127.0.0.1:18096/healthz
//	curl -X POST --data-binary 'hello' \
//	  http://127.0.0.1:18096/api/rooms/lobby/broadcast
//	websocat -s encryl.v2 ws://127.0.0.1:18096/ws
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
	"github.com/encryptedbusiness/encryl/httpuring"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18096", "HTTP and WebSocket listen address")
	loops := flag.Int("loops", 0, "reactor loops; 0 uses NumCPU")
	maxConns := flag.Int("max-conns", 100_000, "connection slab ceiling")
	ping := flag.Duration("ping", 30*time.Second, "server ping interval; 0 disables")
	quiet := flag.Bool("quiet", false, "disable per-request HTTP logging")
	noURing := flag.Bool("no-uring", false, "use the standard net.Listener instead")
	flag.Parse()

	cfg := encryl.DefaultConfig()
	cfg.Loops = *loops
	cfg.MaxConnections = *maxConns
	cfg.AcceptBacklog = 65535
	cfg.BufferCount = 8192
	cfg.TxBytes = 16 << 20
	cfg.PingInterval = *ping
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

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	if !*quiet {
		router.Use(middleware.Logger)
	}
	router.Use(middleware.Recoverer)

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	router.Get("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(srv.Stats())
	})
	router.Post("/api/rooms/{room}/broadcast", func(w http.ResponseWriter, r *http.Request) {
		room := chi.URLParam(r, "room")
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
	router.Get("/ws", srv.ServeHTTP)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	if *noURing {
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			log.Fatal(err)
		}
		server := &http.Server{Handler: router}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
			_ = srv.Shutdown(shutdown)
		}()
		log.Printf("uringhttp_websocket bridge listening on http://%s/ (websocket ws://%s/ws, standard listener)", *addr, *addr)
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
		return
	}

	hs := &httpuring.Server{
		Addr:         *addr,
		HTTPHandler:  router,
		Workers:      0,
		Loops:        1,
		ServerHeader: "encryl",
	}
	go func() {
		<-ctx.Done()
		_ = hs.Close()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("uringhttp_websocket ring listening on http://%s/ (websocket ws://%s/ws)", *addr, *addr)
	if err := hs.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
