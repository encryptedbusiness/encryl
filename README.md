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

The protobuf codec and schema are in. The command/reply/push vocabulary builds
and passes its tests; the upgrade path and dispatcher are next.

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

For a handler that emits at high rates, precompute the ID once:

```go
chat := srv.Topic("chat")
chat.On(func(c *encryl.Conn, data []byte) {
    _ = chat.To(c, "general").Emit(data) // no hash per emit
})
```

`srv.On` remains the one-line convenience.

## Protobuf codec

The new `protocol` package implements a small command/reply/push codec:
length-delimited messages, a `Connect` command, subscribe/publish/send/ack, and
pushes for events, Self-Room messages, join/leave, disconnect, and server
requests. It is a protobuf subset inspired by Centrifuge, not wire-compatible
with it.

The codec and the `.proto` schema are in place. Wiring it into the upgrade path
and the event dispatcher is next.

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
make fmt vet test test-race checkptr
make cross
```

Runtime work needs Linux 6.6+:

```sh
make limactl-up
make preflight
make test-uring
```

## License

MIT.
