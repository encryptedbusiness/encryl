# Named events

The public event API is a name and a function. There is no generated code and
no typed-event layer.

```go
srv.OnConnect(func(c *encryl.Conn) error {
    return c.Join("general")
})

srv.On("chat", func(c *encryl.Conn, data []byte) {
    // data is borrowed for this call; copy it to retain it.
    _ = c.To("general").Emit("chat", data)
})

srv.OnRequest("rpc.echo", func(c *encryl.Conn, req []byte) ([]byte, error) {
    return req, nil
})
```

That is the whole surface for the common case. The server does not require
numeric constants, a registration table, a struct tag, a generator, or a
dependency.

## Handler model

- `Server.On("chat", handler)` registers one global handler for the name.
- `Server.OnRequest("rpc", handler)` registers a request handler; the returned
  bytes are the acknowledgement payload.
- `OnConnect`/`OnDisconnect` are the connection-scoped hooks.
- Handlers run on the connection's owning loop. Do not block.
- The payload is borrowed: valid only until the handler returns.
- There are no per-connection handler maps. That is deliberate: 1M sockets with
  a handler map each is the memory problem this library exists to avoid.
  Connection-specific state belongs in `c.SetUserData`/`c.UserData`.

The handler has no error return. For a normal event, return value or error
routing is application-specific; send an event back or close the connection.
A request handler returns `([]byte, error)` so the acknowledgement is explicit.

## Sending

```go
c.Emit("welcome", data)                     // this connection
c.To("room").Emit("chat", data)             // room, excluding c
c.ToRoom("room").Emit("chat", data)         // room, including c
srv.Emit("announcement", data)              // all connections, including c
srv.Emit("announcement", data)              // all
srv.To("room").Emit("chat", data)           // room from the server
srv.To("a").To("b").Emit("chat", data)      // each targeted room
```

A connection in more than one targeted room may receive one frame per room.
Chaining:

```go
c.To("room").Except(otherID).Volatile().Emit("typing", data)
```

`Emit` returns the existing `ErrBackpressure`, `ErrClosed`, and
`ErrPayloadTooLarge`. `Volatile` is the existing best-effort write policy.

## Requests and acknowledgements

Client-to-server:

```go
srv.OnRequest("rpc.echo", func(c *encryl.Conn, req []byte) ([]byte, error) {
    return append([]byte("ok:"), req...), nil
})
```

Server-to-client:

```go
c.Ask("rpc.echo", req, time.Second, func(a encryl.Ack) {
    // a.Payload is the client's response; a.Err is ErrAckTimeout on timeout.
})
```

The request ID is carried in the existing envelope; the pending-ACK slab and
its identity checks are unchanged. Broadcast acknowledgements are not part of
this API.

## Event names and the wire

The native envelope keeps its 16-byte header. The event field is the FNV-1a
32-bit hash of the name:

```text
EventIDForName("chat")                = 0xa24bf9ab
EventIDForName("chat.message")        = 0x5158bc5e
EventIDForName("rpc.echo")            = 0x34830b6f
```

- `On`/`OnRequest` hash once at registration, register the name with the
  server's name table, and install the handler under the numeric ID.
- `Emit`/`To`/`ToRoom`/`Broadcast` hash once per send, not per recipient.
- Inbound dispatch receives the numeric ID; no per-message string lookup.
- The wire does not carry the event name.
- Clients compute the same hash. `encryl.EventIDForName` is exported for
  Go clients; the algorithm is ten lines in any language.
- A protobuf client can send `EventRef.Name`; the server resolves it through
  the name table. Pushes carry the numeric ID.

The hash is tested against fixed vectors. A collision is detected at
registration: a name mapped to an existing different name's ID is an error.
Use `OnID`/`EmitID` with explicit IDs for a collision-sensitive application.
A 32-bit ID is the existing wire contract; changing it to 64-bit would be a
separate protocol version.

## Unknown events

An unsolicited event with no handler is dropped, matching v1 and Socket.IO.
It is counted in the existing rejection stats; no error frame is sent. An
unknown event **with a request ID** gets an error acknowledgement so the caller
cannot hang. This keeps the wire quiet on typos while making request failures
visible.

## Low-level numeric API

The old API is still available for benchmarks, migration, and clients that
already have numeric IDs:

```go
srv.OnID(1024, func(c *encryl.Conn, m encryl.Message) error {
    return c.EmitID(1024, m.Payload)
})
c.EmitID(1024, data)
srv.To("room").EmitID(1024, data)
srv.OnRequestID(1024, handler)
```

These methods bypass the name table. The string API is the documented path.

## Rooms and connection state

```go
c.Join("room")
c.Leave("room")
c.Rooms(func(name string) bool { ... })
c.SetUserData(session)
session := c.UserData()
```

`SetUserData` is one atomic pointer slot, not a map. Pass a struct if you need
multiple fields. Rooms are removed automatically on disconnect.

## Performance

8 vCPU Linux VM, Go 1.27.2, arm64. Full report:
[`docs/benchmarks/2026-10-23-named-events.md`](benchmarks/2026-10-23-named-events.md).

| Path | Result |
| ---- | ------ |
| `EventIDForName("chat")` | 1.13 ns, 0 B/op, 0 allocs/op |
| `EventIDForName("chat.message")` | 3.44 ns, 0 B/op, 0 allocs/op |
| `EventIDForName("room:presence:update")` | 5.26 ns, 0 B/op, 0 allocs/op |
| Named echo, 100k | 26.81 µs/op, **0 B/op, 0 allocs/op** |
| Topic echo (no hash), 100k | 26.80 µs/op, **0 B/op, 0 allocs/op** |
| Numeric echo, same harness | 26.99 µs/op, 0 B/op, 0 allocs/op |

The hash is below the frame/queue cost and happens once per send, not per
recipient. The handler registry is frozen at `ListenAndServe`: dispatch is one
atomic load and one map lookup, no lock.

## Zero-hash hot path

At millions of sends per second, even a 1–5 ns hash is avoidable. Create a
`Topic` once; it stores the name and ID:

```go
chat := srv.Topic("chat")

chat.On(func(c *encryl.Conn, data []byte) {
    _ = chat.To(c, "general").Emit(data) // EmitID, no hash
})

chat.Emit(c, data)
chat.Broadcast(data)
chat.Ask(c, data, time.Second, cb)
```

`Topic.On`/`OnRequest` register by the precomputed ID. `Topic.To`,
`Topic.ToRoom`, and `Topic.ToConns` return `TopicTarget`; `TopicTarget.Emit`
uses the numeric ID and still supports `Except`/`Volatile`.

The hash is only on `Emit`/`To`/`Broadcast` calls made with a string. Inbound
frames carry the numeric ID and never hash. A fan-out hashes once for the whole
send, not once per recipient. At 1M string emits/s, the hash is 1.2–5.3 ms of
CPU per second; `Topic` makes it zero.

## Migration from v1

v1:

```go
socket.On["chat"] = func(sub *encryl.Subscriber, data []byte) {
    socket.Emit("chat", sub, data, "general")
}
```

Now:

```go
srv.On("chat", func(c *encryl.Conn, data []byte) {
    _ = c.To("general").Emit("chat", data)
})
```

v1's `connected`/`disconnect` map keys become `OnConnect`/`OnDisconnect`.
v1's `Emit(event, exclude, data, rooms...)` becomes `c.To(room).Emit(event, data)`
or `srv.To(room).Emit(event, data)`.

The wire is not v1's JSON. A v1 JS client needs a new client that hashes event
names and speaks the 16-byte envelope. The v1 server API shape is preserved.

## Limits and non-goals

- No per-connection handlers; use `OnConnect` and user data.
- No automatic payload decoding; borrowed bytes.
- No wildcard event subscriptions.
- No Socket.IO wire compatibility (Engine.IO/JSON namespaces); the API is
  inspired by Socket.IO, not wire-compatible.
- Event IDs are 32-bit FNV hashes; collision detection is at registration.
- A `Topic` handle for a precomputed ID is a possible later optimization; the
  current hash is already 1–6 ns.
