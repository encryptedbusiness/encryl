# Running behind net/http, fasthttp, and Fiber

Encryl normally owns the listening socket through `ListenAndServe`. When an
application already has an HTTP server, it can keep that listener and hand only
an upgraded connection to encryl:

```go
if err := srv.Start(ctx); err != nil { ... }
defer srv.Shutdown(context.Background())
```

`Start` initializes the reactor loops without binding a TCP address. It freezes
the event registry, so register handlers before calling it. `Start` and
`ListenAndServe` are mutually exclusive, and `Shutdown` stops either mode.

## net/http

`*encryl.Server` implements `http.Handler`:

```go
if err := srv.Start(ctx); err != nil {
    log.Fatal(err)
}
defer srv.Shutdown(context.Background())

mux := http.NewServeMux()
mux.Handle("GET /ws", srv) // ServeHTTP
log.Fatal(http.ListenAndServe(":8080", mux))
```

When the application already owns a `net.Listener`, pass it to
`http.Server.Serve` instead:

```go
ln, err := net.Listen("tcp", ":8080")
if err != nil {
    log.Fatal(err)
}
hs := &http.Server{Handler: mux}
log.Fatal(hs.Serve(ln))
```

`examples/nethttp` is the explicit-listener version; `examples/mux` adds
path-based HTTP routes, `srv.Stats()`, and a REST broadcast endpoint.

`ServeHTTP` validates the already-parsed request before hijacking. A rejection
uses the normal `http.Error` path; a valid upgrade is hijacked, the fd is
duplicated with `F_DUPFD_CLOEXEC`, and the reactor writes the `101`.

## net/http directly on io_uring

`httpuring` provides both a raw HTTP/1.1 server and a `net.Listener` adapter
for ordinary `http.Server` on io_uring.

For maximum throughput, use the direct loop; it does not import net/http or
fasthttp on the hot path:

```go
mux := &httpuring.Mux{}
mux.HandleFunc("/healthz", func(req *httpuring.Request, res *httpuring.Response) {
    res.Body = []byte("ok\n")
})
srv := &httpuring.Server{Addr: ":8080", Handler: mux, ServerHeader: "app"}
log.Fatal(srv.ListenAndServe())
```

`Request.Path`, `Request.Method`, `Request.Header`, and `Request.Body` are
borrowed slices valid during the handler. `Response.Body` starts from a
reusable per-connection scratch buffer; append to it to avoid a per-request
allocation. The direct loop is exact-path routing only; use the `net.Listener`
adapter below when you need `http.ServeMux` or chi.

### chi, Echo, Gin, or http.ServeMux on the ring

`httpuring.Server` also accepts any `http.Handler`:

```go
router := chi.NewRouter()
router.Use(middleware.RequestID)
router.Use(middleware.BasicAuth("api", map[string]string{"user": "pass"}))
router.Get("/api/ping", func(w http.ResponseWriter, r *http.Request) {
    _, _ = w.Write([]byte("pong\n"))
})

srv := &httpuring.Server{
    Addr:        ":8080",
    HTTPHandler: router, // any http.Handler
    Workers:     0,      // inline: fastest for small handlers
}
log.Fatal(srv.ListenAndServe())
```

`Workers: 0` runs the handler inline in the ring loop. A positive value sends
the request through the request ring to a worker, then returns the response
through the response ring; use that for handlers that block on a database or
external service. `/ws` must use `Workers: 0` because `Hijack` is inline-only.
Chi's `BasicAuth` and normal `middleware` work unchanged.

Raw HTTP slow clients are bounded by `HeaderTimeout` (10s), `IdleTimeout`
(2m), `WriteTimeout` (30s), `MaxOutputBytes` (256 KiB), and an optional
`MaxConnections` cap. Slow WebSocket consumers are governed by the per-
connection write queue and `Backpressure` policy.
Echo and Gin also implement `http.Handler`, so the same adapter applies.

The `net.Listener` adapter remains available for `http.Server`:

```go
import "github.com/encryptedbusiness/encryl/httpuring"

ln, err := httpuring.Listen(":8080")
if err != nil {
    log.Fatal(err)
}
defer ln.Close()

mux := http.NewServeMux()
mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
    _, _ = w.Write([]byte("pong\n"))
})
log.Fatal((&http.Server{Handler: mux}).Serve(ln))
```

One ring handles accept, recv, send, and deadline cancellation. `Read` and
`Write` are blocking for `net/http`; the listener loop is the reactor that
services them. This is the shape of `go-uring`'s `http-server` example and is
demonstrated in `examples/uringhttp`.

## fasthttp

The adapter is in the separate `adapters` module so the core package does not
depend on fasthttp or Fiber:

```go
import fasthttpadapter "github.com/encryptedbusiness/encryl/adapters/fasthttp"

if err := srv.Start(ctx); err != nil {
    log.Fatal(err)
}
defer srv.Shutdown(context.Background())

log.Fatal(fasthttp.ListenAndServe(":8080", fasthttpadapter.Handler(srv)))
```

For a single route in a larger fasthttp handler:

```go
func route(ctx *fasthttp.RequestCtx) {
    if string(ctx.Path()) == "/ws" {
        _ = fasthttpadapter.Upgrade(ctx, srv)
        return
    }
    // normal HTTP handling
}
```

The adapter validates before hijacking, calls `HijackSetNoResponse(true)`, and
passes any bytes fasthttp already buffered to the reactor. Encryl writes
the `101`, not fasthttp.

## Fiber v3

Fiber is a fasthttp wrapper, so the handoff is the same:

```go
import fiberadapter "github.com/encryptedbusiness/encryl/adapters/fiberv3"

if err := srv.Start(ctx); err != nil {
    log.Fatal(err)
}
defer srv.Shutdown(context.Background())

app := fiber.New()
app.Get("/ws", fiberadapter.Handler(srv))
log.Fatal(app.Listen(":8080"))
```

The Fiber adapter uses `c.RequestCtx()` to reach the underlying
`*fasthttp.RequestCtx`. Run authentication and routing middleware before the
websocket route; validation and the 101 are handled by encryl.

Fiber v2 exposes the same fasthttp request through `c.Context()`. The v3
adapter is a separate package because the major module path differs; a v2 glue
handler is:

```go
app.Get("/ws", func(c *fiber.Ctx) error {
    return fasthttpadapter.Upgrade(c.Context(), srv)
})
```

## Delivery and ownership

- The framework keeps ownership of its HTTP listener.
- Encryl duplicates the accepted socket. The adapter closes the original
  `net.Conn` after the handoff; the duplicate is owned by the reactor.
- No socket payload is copied during the handoff. The only copy is the bytes the
  framework had already read into its userspace request buffer; if none exist,
  the handoff has no data copy.
- The reactor writes the `101` and all WebSocket frames. Frameworks must not
  write an upgrade response after handing over the connection.
- TLS terminates upstream. A `*tls.Conn` is rejected; HTTP/2 cannot be hijacked
  and is rejected.

## Examples

```sh
go run ./examples/http
# http://127.0.0.1:18090/healthz
# ws://127.0.0.1:18090/ws

go run ./examples/nethttp
# explicit net.Listener + http.Server.Serve
# http://127.0.0.1:18092/healthz
# ws://127.0.0.1:18092/ws

go run ./examples/mux
# path-routed ServeMux + REST broadcast API
# http://127.0.0.1:18093/healthz
# ws://127.0.0.1:18093/ws

go run ./examples/uringhttp
# standard http.Server + ServeMux on httpuring's io_uring listener
# http://127.0.0.1:18094/ping
# add -no-uring to compare with a standard net.Listener

cd examples/uringhttp_chi
go run .
# chi router + middleware on httpuring
# http://127.0.0.1:18095/api/ping

cd ../uringhttp_echo
go run .
# raw HTTP echo on httpuring.Server
# http://127.0.0.1:18111/healthz

cd ../uringhttp_websocket
go run .
# one listener: chi HTTP API + encryl WebSocket upgrade
# http://127.0.0.1:18096/healthz
# ws://127.0.0.1:18096/ws

cd ../fiber
go run .
# http://127.0.0.1:18091/healthz
# ws://127.0.0.1:18091/ws
```

The `http`, `nethttp`, `mux`, and Fiber examples expose `echo` and `broadcast`
events and join `lobby` on connect. `examples/nethttp` owns the TCP listener
explicitly; `examples/mux` adds path-based HTTP routes and a REST broadcast
endpoint. `examples/uringhttp` is the plain-HTTP io_uring listener example.
`examples/uringhttp_chi` mounts chi and middleware on that listener, while
`examples/uringhttp_websocket` serves chi API routes and upgrades `/ws` into
encryl on one raw `httpuring` listener, using `ResponseWriter.Hijack`.
There is no net/http server in the `/ws` path. The Fiber example is a separate module so the core
module stays independent of Fiber; the chi examples are separate modules for
the same reason.

## Tests

The adapter tests run only on Linux because the reactor is io_uring-only:

```sh
cd adapters && go test ./...
./scripts/linux-run.sh bash -lc 'cd adapters && go test ./...'
```

They cover a real fasthttp hijack, a Fiber v3 route, the net/http handler, and a
client frame pipelined behind the upgrade request.
