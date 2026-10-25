# Migrating from v0 to v1

v1 is a new module path: `github.com/encryptedbusiness/encryl`. v0 stays frozen at
`v0.9.0`; it will not receive fixes. The change is intentionally breaking — v0's
public surface exposes channels, pointers, and maps that v1 cannot keep while
guaranteeing bounded memory.

## TL;DR

```go
// v0
socket, _ := encryl.StartUp(1)
socket.On["chat"] = func(sub *encryl.Subscriber, data []byte) {
    socket.Emit("chat", sub, data, "general")
}
http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    _, _, sub, _ := socket.UpgradeConnection(r, w)
    socket.On["connected"](sub, nil)
})
http.ListenAndServe(":8080", nil)
defer socket.Close()

// v1
cfg := encryl.DefaultConfig()
cfg.Addr = ":8080" // plaintext; TLS terminates at the proxy
cfg.AllowedOrigins = []string{"https://app.example.com"}
srv, _ := encryl.New(cfg)
srv.OnConnect(func(c *encryl.Conn) error {
    return c.Join("general")
})
srv.On("chat", func(c *encryl.Conn, data []byte) {
    // data is borrowed. Every send is a room send; To excludes the caller.
    _ = c.To("general").Emit("chat", data)
})
defer srv.Shutdown(context.Background())
srv.ListenAndServe(context.Background())
```

## Symbol mapping

### Server

| v0 | v1 | Notes |
| -- | -- | ----- |
| `StartUp(numCPU int) (*Socket, error)` | `New(Config) (*Server, error)` | `Loops` replaces the CPU argument |
| `StartUpWithConfig(Config)` | `New(Config)` | Same shape, different fields |
| `Socket.Close() error` | `Server.Shutdown(ctx) error` | Bounded by `ShutdownTimeout`; idempotent |
| — | `Server.ListenAndServe(ctx) error` | The server now owns the listener |
| `Socket.UpgradeConnection(r, w)` | **removed** | Use `ListenAndServe`; per-request upgrade is no longer the integration point |
| `Socket.Emit(event, exclude, data, rooms...)` | `Server.To(room).Emit("event", payload)` or `Conn.To(room).Emit` | One encoded frame shared by all targets |
| `Socket.EmitDirect(sub, event, data)` | `Conn.Emit("event", payload)` | Admission is synchronous and returns an error |
| `Socket.Broadcast(event, data)` | `Server.Emit("event", payload)` | Encoded once, shared |
| — | `Server.Emit("event", payload)` | Every connection, including the caller |
| — | `Conn.Ask("event", payload, timeout, cb)` | Bounded pending-ACK slab; timeout on the timer wheel |
| — | `Conn.Reply(m, payload)` | `ACK` with a payload; suppresses the automatic empty ACK |
| — | `Server.Use(mw)` | Per-event middleware; register before the first `On` |
| — | `Server.UseConnect(mw)` | After the upgrade; reject closes with `1008` |
| `Socket.AddToRoom(sub, room)` | `Conn.Join(room)` | |
| `Socket.RemoveFromRoom(sub, room)` | `Conn.Leave(room)` | |
| `Socket.Remove(sub)` | `Conn.Close(code, reason)` / `Conn.Disconnect()` | `Disconnect` is `Close(1000, "")` |
| `Socket.GetRoomMembers(room)` | **removed** | Returns no pointers; use `Conn.Rooms(fn)` for the caller's names |
| `Socket.GetRoomCount(room)` | `Server.RoomSize(room)` | A count, not a member list |
| `Socket.On map[string]HandlerFunc` | `Server.On(name, HandlerFunc)` / `OnRequest` | String names; the wire uses an internal hash |
| `Socket.On["connected"]` | `Server.OnConnect` | Connection hook, not a reserved event |
| `Socket.On["disconnect"]` | `Server.OnDisconnect` | Connection hook, not a reserved event |

### Subscriber / Conn

| v0 | v1 | Notes |
| -- | -- | ----- |
| `Subscriber.ID string` | `Conn.ID() ConnID` | `{Slot, Gen}`, not a string; never used for authorization |
| `Subscriber.Send(payload) bool` | `Conn.Emit("event", payload) error` | `bool` could not distinguish backpressure from a closed connection |
| `Subscriber.IsClosed() bool` | `Emit` returns `ErrClosed` | Removes the check-then-act race |
| `Subscriber.Outbound chan []byte` | **removed** | Internal bounded queue; use `Emit` |
| `Subscriber.Metadata sync.Map` | **removed** | Planned server-side metadata store keyed by `ConnID` (M2) |
| `Subscriber.Rooms sync.Map` | **removed** | Membership is server-owned |
| `HandlerFunc(subs *Subscriber, data []byte)` | `EventHandler(c *Conn, m Message) error` | Return an error to send an `ERROR` envelope |

### Configuration

| v0 | v1 | Notes |
| -- | -- | ----- |
| `Config.NumWorkers` | — | Dispatch pool sizing is internal (M2) |
| `Config.NumShards` | — | Derived from `Loops`; no separate knob |
| `Config.EventChanSize` | `WriteQueueMessages` | The queue is per connection, not global |
| — | `Loops` | I/O topology; io_uring is the only backend |
| — | `MaxFrameSize`, `MaxMessageSize`, `MaxFragments` | Protocol limits |
| — | `HandshakeTimeout`, `IdleTimeout`, `PingInterval`, `CloseTimeout`, `ShutdownTimeout` | Lifecycle deadlines |
| — | `WriteQueueBytes`, `WriteQueueMessages`, `Backpressure` | Bounded writes |
| — | `ReadBufferSize`, `BufferCount`, `TxBytes`, `MaxConnections` | Per-loop arenas and slab ceiling |
| — | `MaxRoomsPerConn` | Room abuse limit. Per-IP and per-connection rate config fields are removed until they are enforced |
| — | `AllowedOrigins` | CSWSH protection; load-bearing with TLS upstream |

There is no `TLSConfig`, `TLSMode`, `Backend`, or `AuthTimeout` in v1. TLS is
terminated by the proxy in front of the process; authentication is an M2 item.

### Events

| v0 | v1 |
| -- | -- |
| `socket.On["connected"]` | `Handler.OnOpen` |
| `socket.On["disconnect"]` | `Handler.OnClose` with `CloseInfo` |
| JSON envelope `{event, data}` from `encryl.js` | Binary `encryl.v1` envelope (`docs/protocol.md`) |
| Any string event name | Numeric `uint32` event ID; names become generated constants |

## Behavioural changes to plan for

1. **TLS terminates upstream.** v1 speaks plaintext `ws://` only and does not
   terminate, offload, or negotiate TLS. Put it behind a TLS-terminating load
   balancer or reverse proxy, and do not expose the plaintext listener to an
   untrusted network.
2. **No per-request upgrade.** If you terminate the WebSocket yourself behind a
   framework, v1 is not a drop-in. The listener is owned by the server so that
   accept, the parser, and the reactor share one descriptor lifecycle.
3. **Handlers must not block.** A blocking handler stalls every connection on
   its core. Use the worker pool for database or RPC work.
4. **Payloads are borrowed.** Copy anything you retain; the slice is invalid
   after the handler returns.
5. **`Emit` is admission, not delivery.** A `nil` return means the message is
   queued. A full queue under `BackpressureReject` returns `ErrBackpressure`.
   Delivery failures surface through `OnClose` and `Stats`.
6. **Event names stay strings at the call site.** They hash to the numeric
   wire ID once per send; there is no per-message string parse or map lookup.
7. **Room membership is not visible as pointers.** Code that iterated
   `GetRoomMembers` to send messages should call `Emit` instead; that is what
   makes encode-once fan-out possible.
8. **Every send is a room send.** A unicast `Conn.Emit` targets the connection's
   implicit Self-Room. The Self-Room costs zero storage and is not listed by
   the room directory. `Server.Emit` is the all-connection shorthand; a room send is the scoped form.
9. **Acknowledgements are bounded.** `Conn.Ask` allocates one entry from
   `MaxPendingAcks`; a full slab returns `ErrBackpressure`. The timeout is
   delivered to the callback as `ErrAckTimeout`. The numeric low-level form is
   `EmitWithAckID`.

## Migration steps

1. Pin v0.9.0 and record its current p99 and RSS with `bench/loadgen`
   (idle and connect modes) before changing anything.
2. Inventory every `On[...]` handler and name it (`srv.On("chat", ...)`).
3. Move connection setup from HTTP handlers to `OnConnect`.
4. Replace `EmitDirect`/`Send` with `Conn.Emit` and handle `ErrBackpressure`.
5. Replace room iteration with `Emit`.
6. Configure the TLS-terminating proxy and set the Origin allowlist on the
   server.
7. Re-run the load generator against v1 and compare only like-for-like
   workloads (see `docs/benchmarks/README.md` for why v0 message throughput is
   not directly comparable).
