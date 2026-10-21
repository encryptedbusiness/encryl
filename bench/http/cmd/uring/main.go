//go:build linux

// Command uring runs the TFB JSON/Plaintext shape on httpuring.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl/bench/http/internal/payload"
	"github.com/encryptedbusiness/encryl/httpuring"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18097", "listen address")
	noURing := flag.Bool("no-uring", false, "use net.Listen instead of httpuring")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		var buf [32]byte
		body := payload.AppendMessage(buf[:0], payload.Message{Message: "Hello, World!"})
		w.Header().Set("Server", "encryl")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "27")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/plaintext", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "encryl")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "13")
		_, _ = w.Write([]byte(payload.Plaintext))
	})

	ln, err := listen(*addr, *noURing)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("uring benchmark listening on %s (uring=%v)", *addr, !*noURing)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func listen(addr string, noURing bool) (net.Listener, error) {
	if noURing {
		return net.Listen("tcp", addr)
	}
	return httpuring.Listen(addr)
}
