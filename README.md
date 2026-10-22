# Encryl

_Encryl_ — encrypted loop. A sync engine and WebSocket provider for very high
connection counts with bounded memory per connection, built for room-based and
event-based data transport.

Encryl keeps the per-connection cost small. An idle connection is a slab slot
and a file descriptor; it does not own a goroutine, a timer, a read buffer, or a
write buffer. Buffers come from a per-loop arena only while a receive or a
control send is in flight. The bounded write queue is the only per-connection
queue, and a full queue returns an error instead of blocking or dropping
silently.

## Status

The architecture decision records are in. They cover the io_uring-only choice,
upstream TLS, the wire format, buffer ownership, backpressure, rooms, the
WebSocket parser, benchmark method, public API, M0 verdicts, and connection
lifetime.

## Quick start

```go
cfg := encryl.DefaultConfig()
cfg.Addr = ":8080"
cfg.AllowedOrigins = []string{"https://app.example.com"}

srv, err := encryl.New(cfg)
if err != nil {
    log.Fatal(err) // every validation problem, at once
}
defer srv.Shutdown(context.Background())

srv.OnConnect(func(c *encryl.Conn) error {
    return c.Join("general")
})

srv.On("chat", func(c *encryl.Conn, data []byte) {
    // data is borrowed: valid only until this returns.
    // c.To excludes the caller; c.ToRoom includes it.
    _ = c.To("general").Emit("chat", data)
})

srv.OnRequest("rpc.echo", func(c *encryl.Conn, req []byte) ([]byte, error) {
    return req, nil
})

log.Fatal(srv.ListenAndServe(context.Background()))
```

## Running behind an HTTP server

`Start` runs the reactor without binding a listener. The application keeps its
HTTP server and hands upgraded connections to Encryl:

```go
if err := srv.Start(ctx); err != nil {
    log.Fatal(err)
}
defer srv.Shutdown(context.Background())

http.Handle("/ws", srv) // net/http
```

The adapters module covers fasthttp and Fiber v3:

```go
import fasthttpadapter "github.com/encryptedbusiness/encryl/adapters/fasthttp"
fasthttp.ListenAndServe(":8080", fasthttpadapter.Handler(srv))
```

```go
import fiberadapter "github.com/encryptedbusiness/encryl/adapters/fiberv3"
app.Get("/ws", fiberadapter.Handler(srv))
```

The fd is duplicated with `F_DUPFD_CLOEXEC`; the socket payload is not copied.
The reactor writes the `101`. TLS terminates upstream and HTTP/2 is not
supported.

## Wire protocol

Two encodings share the same event core:

- `encryl.v2` is the native 16-byte envelope: version, kind, flags, event ID,
  request ID, then the opaque payload. Native clients use it.
- `encryl.pb.v1` (or `?format=protobuf`) selects the protobuf command protocol.
  It is a small command/reply/push subset inspired by Centrifuge, not
  wire-compatible with Centrifuge or Grafana Live. A connection that offers no
  subprotocol negotiates protobuf by default.

One WebSocket binary message carries one or more varint-length-delimited
messages. Client commands get a reply with the same ID; asynchronous pushes
ride a reply with ID 0. The codec is hand-written and zero-allocation on the hot
path. The wire limits and close codes are in `docs/protocol.md` once the M0 documentation lands.

## How it works

A connection is owned by exactly one io_uring loop for its whole life. The loop
accepts, parses, dispatches, and writes without handing the connection to a
goroutine. Cross-loop work is a command to the owning loop, not a shared lock.

- The slab stores slot state in fixed-size chunks. Handles are `(slot,
  generation)` pairs; a stale handle returns `ErrClosed` instead of touching a
  reused slot.
- The write queue is bounded by messages and bytes. Admission is synchronous.
  The default policy returns `ErrBackpressure`; drop and close-slow policies are
  available.
- Every send is a room send. A direct send is an emit to the connection's
  implicit Self-Room, which costs no storage.
- The timer wheel has one hierarchical set of deadlines per loop. A connection
  has at most one active handshake, idle, ping, and close deadline.
- The event registry is frozen at start. Dispatch is one atomic load and one map
  lookup, with no per-message lock.

## Development

```sh
make fmt vet test test-race checkptr docs-check
make cross
```

Runtime work needs Linux 6.6+:

```sh
make limactl-up
make preflight
make test-uring
make autobahn
```

## License

MIT.
