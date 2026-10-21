//go:build linux

// Command uringhttp_echo is a minimal raw HTTP echo server on httpuring.Server.
// It uses the direct ring loop, not net/http or fasthttp.
//
//	go run ./examples/uringhttp_echo
//	curl -X POST --data-binary 'hello' http://127.0.0.1:18111/echo
//	curl http://127.0.0.1:18111/healthz
//
// On Linux only; the ring is io_uring-only.
package main

import (
	"bytes"
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/encryptedbusiness/encryl/httpuring"
)

var textPlain = []byte("text/plain; charset=utf-8")

func main() {
	addr := flag.String("addr", "127.0.0.1:18111", "HTTP listen address")
	flag.Parse()

	mux := &httpuring.Mux{}
	mux.HandleFunc("/healthz", func(req *httpuring.Request, res *httpuring.Response) {
		res.ContentType = textPlain
		res.Body = []byte("ok\n")
	})
	mux.HandleFunc("/echo", func(req *httpuring.Request, res *httpuring.Response) {
		if !bytes.Equal(req.Method, []byte("POST")) && !bytes.Equal(req.Method, []byte("PUT")) {
			res.Status = 405
			res.ContentType = textPlain
			res.Body = []byte("use POST or PUT\n")
			return
		}
		res.ContentType = textPlain
		res.Body = append(res.Body[:0], req.Body...)
	})

	srv := &httpuring.Server{
		Addr:         *addr,
		Handler:      mux,
		ServerHeader: "encryl",
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Printf("uringhttp_echo listening on http://%s/", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
