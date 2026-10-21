//go:build linux

// Command uringhttp_chi runs a chi router on httpuring's ring-native
// http.Handler adapter. It demonstrates chi URL parameters, route groups,
// built-in BasicAuth middleware, and the optional request/response worker ring.
//
//	go run ./examples/uringhttp_chi
//	curl -i http://127.0.0.1:18095/api/ping
//	curl -i http://127.0.0.1:18095/api/items/42
//	curl -i -u user:pass http://127.0.0.1:18095/api/secure
//	curl -i -X POST --data-binary 'hello' http://127.0.0.1:18095/api/echo
//
// Use -bridge to run the same router on a normal net.Listener instead.
//
// On Linux only; the ring is io_uring-only.
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

	"github.com/encryptedbusiness/encryl/httpuring"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type itemResponse struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18095", "HTTP listen address")
	workers := flag.Int("workers", 0, "ring workers; 0 runs chi inline")
	bridge := flag.Bool("bridge", false, "use net/http + httpuring net.Listener")
	noURing := flag.Bool("no-uring", false, "use standard net.Listener (implies -bridge)")
	flag.Parse()

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(5 * time.Second))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Example", "uringhttp_chi")
			next.ServeHTTP(w, r)
		})
	})

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	router.Route("/api", func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"message": "pong"})
		})
		r.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, itemResponse{
				ID:      chi.URLParam(r, "id"),
				Message: "item from chi route",
			})
		})
		r.Post("/echo", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"body": string(body)})
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.SetHeader("X-Protected", "true"))
			r.Get("/private", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{"message": "protected route"})
			})
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.BasicAuth("example", map[string]string{"user": "pass"}))
			r.Get("/secure", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{"message": "authenticated"})
			})
		})
	})
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *bridge || *noURing {
		ln, err := listen(*addr, *noURing)
		if err != nil {
			log.Fatal(err)
		}
		server := &http.Server{Handler: router}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
		log.Printf("uringhttp_chi bridge listening on http://%s/ (uring=%v)", *addr, !*noURing)
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
		return
	}

	srv := &httpuring.Server{
		Addr:         *addr,
		HTTPHandler:  router,
		Workers:      *workers,
		ServerHeader: "encryl",
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Printf("uringhttp_chi ring listening on http://%s/ (workers=%d)", *addr, *workers)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func listen(addr string, noURing bool) (net.Listener, error) {
	if noURing {
		return net.Listen("tcp", addr)
	}
	return httpuring.Listen(addr)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
