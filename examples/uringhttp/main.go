//go:build linux

// Command uringhttp is the io_uring counterpart of a normal http.Server
// example. It builds a ServeMux with several paths and serves it through
// httpuring.Listen, which exposes an io_uring-backed net.Listener to
// net/http.
//
//	go run ./examples/uringhttp
//	curl http://127.0.0.1:18094/ping
//	curl -X POST --data-binary 'hello' http://127.0.0.1:18094/post
//	curl -OJ http://127.0.0.1:18094/get-file
//	curl -F file=@README.md http://127.0.0.1:18094/file-info
//
// Compare with the standard net.Listener using -no-uring.
//
// On Linux only; the reactor is io_uring-only.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl/httpuring"
)

//go:embed gopher.txt
var gopher []byte

func main() {
	addr := flag.String("addr", "127.0.0.1:18094", "HTTP listen address")
	noURing := flag.Bool("no-uring", false, "use the standard net.Listener instead")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong\n"))
	})
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "you sent: %s", body)
	})
	mux.HandleFunc("/get-file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="gopher.txt"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gopher)
	})
	mux.HandleFunc("/file-info", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file field", http.StatusBadRequest)
			return
		}
		defer file.Close()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "Uploaded File: %s\n", header.Filename)
		_, _ = fmt.Fprintf(w, "File Size: %d\n", header.Size)
		_, _ = fmt.Fprintf(w, "MIME Header: %v\n", header.Header)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("uringhttp example\n\n" +
			"GET  /ping\n" +
			"POST /post\n" +
			"GET  /get-file\n" +
			"POST /file-info\n"))
	})

	ln, err := listen(*addr, *noURing)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	log.Printf("uringhttp listening on http://%s/ (uring=%v)", *addr, !*noURing)
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func listen(addr string, noURing bool) (net.Listener, error) {
	if noURing {
		return net.Listen("tcp", addr)
	}
	return httpuring.Listen(addr)
}
