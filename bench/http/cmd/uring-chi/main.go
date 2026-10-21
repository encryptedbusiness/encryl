//go:build linux

// Command uring-chi runs chi and its middleware through httpuring's
// ring-native http.Handler adapter. Use -workers=0 for inline execution or a
// positive value for the request/response ring worker pool.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/encryptedbusiness/encryl/bench/http/internal/payload"
	"github.com/encryptedbusiness/encryl/httpuring"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

var (
	jsonBody  = []byte(`{"message":"Hello, World!"}`)
	plainBody = []byte(payload.Plaintext)
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18105", "listen address")
	workers := flag.Int("workers", 1, "ring workers; 0 runs the handler inline")
	flag.Parse()

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Get("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonBody)
	})
	router.Get("/plaintext", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(plainBody)
	})
	router.Group(func(r chi.Router) {
		r.Use(middleware.BasicAuth("bench", map[string]string{"user": "pass"}))
		r.Get("/auth", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok\n"))
		})
	})

	srv := &httpuring.Server{
		Addr:         *addr,
		HTTPHandler:  router,
		Workers:      *workers,
		ServerHeader: "encryl",
	}
	log.Printf("uring-chi listening on %s (workers=%d)", *addr, *workers)
	log.Fatal(srv.ListenAndServe())
}
