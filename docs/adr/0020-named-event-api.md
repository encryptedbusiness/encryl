# 0020 — Named events over the numeric event kernel

Status: Accepted
Date: 2026-10-23
Deciders: sairash
Related: ADR-0003 (wire format), ADR-0014 (vertical slices), ADR-0018
(Bebop protocol)

## Context

The event kernel is fast: a 16-byte envelope, numeric event IDs, an immutable
room directory, encode-once fan-out, and bounded write queues. The public API
had drifted away from that simplicity: applications first assigned numeric IDs,
then a name registry was layered on top, then a typed/codegen layer was added.
The typed layer made the developer define payload structs, run two generators,
and depend on MessagePack; it did not fit the desired API and was removed.

The comparison that mattered:

- v1 exposed `socket.On["chat"] = func(sub, data []byte)` and
  `socket.Emit("chat", sub, data, "general")`. One line per event, raw bytes,
  global handlers.
- Socket.IO exposes `socket.on("chat", cb)` and
  `socket.emit("chat", data)`, rooms, acks, and middleware. It is the best
  call-site ergonomics, but it keeps a handler map and ack map per socket and
  serializes JSON arrays.
- NATS/MQTT/Phoenix/SignalR/Centrifuge all use a string topic/event name at the
  edge with a global or channel-level subscription registry, and binary
  payloads. They scale by not giving every connection its own handler map.
- uWebSockets.js and Bun use topic pub/sub with raw binary payloads.

The consistent pattern is: string event/topic, global handler registry, binary
payload, rooms separate from handlers.

## Decision

### 1. Global named handlers

```go
srv.OnConnect(func(c *encryl.Conn) error { return c.Join("general") })
srv.On("chat", func(c *encryl.Conn, data []byte) {
    _ = c.To("general").Emit("chat", data)
})
srv.OnRequest("rpc.echo", func(c *encryl.Conn, req []byte) ([]byte, error) {
    return req, nil
})
```

There is one handler per name, registered once per server. There are no
per-connection handler maps. `OnConnect`/`OnDisconnect` provide the
connection-scoped hook, and `SetUserData`/`UserData` provide one application
slot per connection. This is v1's memory model with Socket.IO's call-site
clarity.

### 2. Borrowed bytes, no decode

`On` receives `func(*Conn, []byte)`. The payload aliases the receive buffer and
is valid only for the call. The application decodes JSON, MessagePack,
Bebop, or a custom format. The library does not reflect, allocate, or copy.

`OnRequest` receives `([]byte) ([]byte, error)`; the response travels in the
existing acknowledgement payload.

### 3. Names hash to the existing wire ID

The native envelope is unchanged: 16 bytes with a 32-bit event field. The event
field is FNV-1a 32 of the name. `On` registers the name and handler under that
ID; `Emit` hashes once per send, never per recipient. Clients compute the same
hash. Bebop clients may send `EventRef.Name`; the server resolves it through
the same name table.

This avoids a new wire version, a handshake name table, and per-message string
bytes. The hash is 1–6 ns and allocation-free; the measured named echo equals
the numeric echo.

A collision is rejected at registration. `OnID`/`EmitID` remain for explicit
numeric IDs and collision-sensitive applications. For a zero-hash hot path,
`srv.Topic("chat")` precomputes the ID and `Topic.On`/`Topic.Emit`/`Topic.To`
use it without hashing.

### 4. One frozen registry

`On`, `OnRequest`, `OnID`, and middleware register in one event registry.
`ListenAndServe` freezes an immutable map snapshot. Dispatch is one atomic load
and one map lookup, with no per-message mutex. `OnConnect`/`OnDisconnect` are
atomic pointers. The lifecycle `Handler` interface remains for compatibility.

### 5. Unknown events are dropped

v1 and Socket.IO do not send an error for an event with no listener. A dropped
unknown event is cheaper than an error frame. An unknown event with a request
ID sends an error ACK so a caller cannot hang.

### 6. No code generation, no typed layer

The typed-event/request layer, `encrylc`, `msgp`, the generated example, and
their documentation and dependency were removed. The public surface is names,
functions, and bytes.

## Consequences

- The call site is one line per event and no numeric constants.
- The wire is 16 bytes + payload; no event name, no JSON escaping.
- String emits pay a 1–6 ns hash once per send.
- The handler map is global; per-connection handler maps are intentionally
  absent. Connection state is `OnConnect` + user data.
- Payloads are borrowed; retaining them requires a copy.
- Clients must reproduce FNV-1a and use a client that speaks the 16-byte
  envelope. A v1 JSON client is not compatible.
- A 32-bit hash collision is possible; registration rejects a collision, and
  `OnID` covers the edge case. A 64-bit event ID would be a new wire version.
- Socket.IO wire compatibility is not provided; the API is inspired by
  Socket.IO, not compatible with its Engine.IO/JSON protocol.

## Alternatives rejected

- **Per-connection `socket.on` maps.** Socket.IO's model; too much memory for
  the 1M-connection target.
- **Automatic JSON/MessagePack decode.** Allocates per message and brings back
  reflection or codegen.
- **String names on the wire.** Removes the client hash, but adds bytes to every
  frame and still needs a per-message lookup. The existing numeric envelope and
  FNV hash won the measurement.
- **64-bit event IDs / v3 envelope.** Better collisions, larger headers, and a
  wire break. Keep 32-bit with registration-time detection.
- **Keeping the typed layer.** It solved a different problem and made the
  common path heavier.

## Measurements

8 vCPU Linux VM, Go 1.27.2, arm64:

| Path | ns/op | B/op | allocs/op |
| ---- | ----: | ---: | --------: |
| `EventIDForName("chat")` | 1.13 | 0 | 0 |
| `EventIDForName("chat.message")` | 3.44 | 0 | 0 |
| `EventIDForName("room:presence:update")` | 5.26 | 0 | 0 |
| Named echo, 100k | 26,807 | **0** | **0** |
| Topic echo, 100k | 26,804 | **0** | **0** |
| Numeric echo, 100k | 26,993 | **0** | **0** |

See `docs/benchmarks/2026-10-23-named-events.md`.

## Sources

- v1 `v1.0.2:encryl.go`, `types.go`, `utilities.go`, chat example.
- [Socket.IO server API](https://socket.io/docs/v4/server-api/)
- [Socket.IO `socket.ts`](https://raw.githubusercontent.com/socketio/socket.io/main/packages/socket.io/lib/socket.ts)
- [Socket.IO protocol](https://raw.githubusercontent.com/socketio/socket.io-protocol/main/Readme.md)
- [Socket.IO rooms](https://socket.io/docs/v4/rooms/)
- [NATS Go client](https://pkg.go.dev/github.com/nats-io/nats.go)
- [NATS client protocol](https://docs.nats.io/reference/protocols/client)
- [Phoenix Channels](https://hexdocs.pm/phoenix/channels.html)
- [SignalR overview](https://learn.microsoft.com/en-us/aspnet/core/signalr/introduction?view=aspnetcore-9.0)
- [Centrifuge](https://github.com/centrifugal/centrifuge)
- [uWebSockets.js](https://github.com/uNetworking/uWebSockets.js)
