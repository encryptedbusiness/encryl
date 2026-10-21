//go:build linux

// Command chat is the runnable chat example: one net/http listener, named
// events, rooms, a Topic hot path, and a request/reply round trip.
//
// The server uses JSON payloads but treats them as opaque bytes; the library
// never decodes them. The browser client is served from the embedded files and
// speaks the native 16-byte envelope with the encryl.v2 subprotocol.
//
//	go run ./examples/chat
//	open http://127.0.0.1:18082
//
// The /ws route hands the upgrade to encryl; /, /app.js, and /healthz stay
// ordinary net/http routes on the same listener.
//
// Events:
//
//	chat.join  {"room":"..."} joins a room; empty joins "lobby"
//	chat.say   {"room":"...","text":"..."} broadcasts to the room except sender
//	chat.ask   {"question":"..."} returns {"question":"...","at":nanos}
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
)

//go:embed index.html app.js
var web embed.FS

const lobbyRoom = "lobby"

type joinMessage struct {
	Room string `json:"room"`
}

type sayMessage struct {
	Room string `json:"room"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

type askRequest struct {
	Question string `json:"question"`
}

type askResponse struct {
	Question string `json:"question"`
	At       int64  `json:"at"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18082", "HTTP and WebSocket listen address")
	wsURL := flag.String("ws-url", "ws://127.0.0.1:18082/ws", "WebSocket URL used by the browser")
	loops := flag.Int("loops", 0, "event loops; 0 uses NumCPU")
	flag.Parse()

	cfg := encryl.DefaultConfig()
	cfg.Loops = *loops
	cfg.AllowedOrigins = []string{"*"}
	cfg.MaxConnections = 100_000

	srv, err := encryl.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	// One Topic per event: the name hashes once at startup, and every send
	// below goes through EmitID/EmitWithAckID. No per-message hash.
	join := srv.Topic("chat.join")
	say := srv.Topic("chat.say")
	ask := srv.Topic("chat.ask")

	if err := srv.OnConnect(func(c *encryl.Conn) error {
		return c.Join(lobbyRoom)
	}); err != nil {
		log.Fatal(err)
	}
	if err := join.On(func(c *encryl.Conn, data []byte) {
		var m joinMessage
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if m.Room == "" {
			m.Room = lobbyRoom
		}
		_ = c.Join(m.Room)
	}); err != nil {
		log.Fatal(err)
	}
	if err := say.On(func(c *encryl.Conn, data []byte) {
		var m sayMessage
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if m.Room == "" {
			m.Room = lobbyRoom
		}
		if m.At == 0 {
			m.At = time.Now().UnixNano()
		}
		out, err := json.Marshal(m)
		if err != nil {
			return
		}
		_ = say.To(c, m.Room).Emit(out)
	}); err != nil {
		log.Fatal(err)
	}
	if err := ask.OnRequest(func(c *encryl.Conn, data []byte) ([]byte, error) {
		var req askRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, err
		}
		return json.Marshal(askResponse{Question: req.Question, At: time.Now().UnixNano()})
	}); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}

	index, err := web.ReadFile("index.html")
	if err != nil {
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(index), "{{WS_URL}}", *wsURL)))
	})
	mux.Handle("GET /app.js", http.FileServer(http.FS(web)))

	hs := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("chat UI on http://%s  WebSocket on ws://%s/ws", *addr, *addr)
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
