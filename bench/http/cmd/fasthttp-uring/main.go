//go:build linux

// Command fasthttp-uring runs the TFB JSON/Plaintext shape on fasthttp over
// httpuring's io_uring net.Listener.
package main

import (
	"flag"
	"log"
	"net"

	"github.com/encryptedbusiness/encryl/bench/http/internal/payload"
	"github.com/encryptedbusiness/encryl/httpuring"
	"github.com/valyala/fasthttp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18100", "listen address")
	noURing := flag.Bool("no-uring", false, "use net.Listen instead of httpuring")
	flag.Parse()

	handler := func(ctx *fasthttp.RequestCtx) {
		switch string(ctx.Path()) {
		case "/json":
			var buf [32]byte
			body := payload.AppendMessage(buf[:0], payload.Message{Message: "Hello, World!"})
			ctx.Response.Header.Set("Server", "encryl")
			ctx.Response.Header.SetContentType("application/json")
			ctx.Response.SetBodyRaw(body)
		case "/plaintext":
			ctx.Response.Header.Set("Server", "encryl")
			ctx.Response.Header.SetContentType("text/plain")
			ctx.Response.SetBodyString(payload.Plaintext)
		default:
			ctx.NotFound()
		}
	}

	ln, err := listen(*addr, *noURing)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("fasthttp benchmark listening on %s (uring=%v)", *addr, !*noURing)
	log.Fatal(fasthttp.Serve(ln, handler))
}

func listen(addr string, noURing bool) (net.Listener, error) {
	if noURing {
		return net.Listen("tcp", addr)
	}
	return httpuring.Listen(addr)
}
