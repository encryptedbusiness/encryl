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

The attached HTTP path and fd handoff are measured. The server can run under
net/http, fasthttp, or Fiber without copying the socket payload.

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

## M0 verification

Ubuntu 24.04 / kernel 6.8.0-147 / arm64 / Go 1.27.2 in a Lima VM on Apple
Silicon. Full log:
[`docs/m0-execution-log.md`](docs/m0-execution-log.md).

| Check | Result |
| ----- | ------ |
| io_uring probe | all required opcodes and restrictions supported |
| io_uring echo | 200 clients, 64 buffers, 200/200 echoed, zero errors |
| kTLS userspace / TX / TX+RX | handshake and echo correct (historical; TLS is not part of the library) |
| v1.0.2 idle density | 200,000 connections, 0 failed, 8.97 KiB process + 2.37 KiB slab per connection |

The kTLS results are historical evidence. TLS terminates upstream; see
[`ADR-0013`](docs/adr/0013-tls-terminates-upstream.md).

## M2 measurements

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, 8 vCPU Lima VM. Method and raw
data: [`docs/benchmarks/2026-10-23-m2-vertical-slices.md`](docs/benchmarks/2026-10-23-m2-vertical-slices.md).

| Path | Result |
| ---- | ------ |
| Public server echo, 100k | 26,916 ns/op, **0 B/op, 0 allocs/op** |
| Raw reactor echo, 100k | 25,946 ns/op, **0 B/op, 0 allocs/op** |
| Registry dispatch | 5.55 ns, 0 allocs |
| Frame acquire/release | 21.0 ns, 0 allocs |
| Pending ACK bind/take | 9.0 ns, 0 allocs |
| Fan-out planner, 4096 refs | 50.4 µs, 0 allocs |
| Envelope decode | 1.59 ns, 0 allocs |
| Frame parse | 16.37 ns, 1343 MB/s, 0 allocs |
| 1M idle plaintext | 1,000,000 open, **0.275 KiB total RSS/conn** |
| 1M echo, 50k msg/s shape | 1,886,007 in / 1,886,007 out, zero drops |
| 8k Self-Room echo | 546,374 msg/s, exact parity |
| 10k room fan-out, 1/s | 195,692 frames in 20 s |
| Struct sizes | Slot 160 B, Conn 88 B, Accounting 32 B, Frame 48 B |

## M3 measurements

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, 8 vCPU / 15.6 GiB Lima VM.
Reports:
[`2026-10-23-m3-validation.md`](docs/benchmarks/2026-10-23-m3-validation.md) and
[`2026-10-23-m3-practical.md`](docs/benchmarks/2026-10-23-m3-practical.md).

| Path | Result |
| ---- | ------ |
| True-rate echo, 400k | 19,279,981 / 19,279,981, zero drops, 47,968 msg/s |
| 1M hold, 60 minutes, 1 broadcast / 10 s | 360,018,000 frames, 0 drops, fd flat, p99 176 ms |
| 1M fan-out, 1 broadcast / 5 s | 59,265,161 / 59,265,161, 0 drops, p99 97 ms |
| 1M fan-out, 1 broadcast / 1 s | 32.5% delivered on 8 vCPU; capacity limit, not a correctness failure |
| 1:1 unicast, 100k | 1,100,000 / 1,100,000, p99 3 ms |
| Delivery receipts (ACK), 50k | 550,000 / 550,000, p99 2 ms |
| Group chat, 100k | 5,729,942 deliveries, p99 3 ms |
| Churn | 4,839,786 cycles + 14,647,222 group deliveries, 0 failures |
| EMQX 5.8.6, same fan-out shape | 6,000,000 / 6,000,000, 0 drops; 544.7 MiB vs 59.8 MB at 10k |

## Protobuf codec

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, 8 vCPU. Report:
[`docs/benchmarks/2026-10-23-protobuf-protocol.md`](docs/benchmarks/2026-10-23-protobuf-protocol.md).

| Path | Result |
| ---- | ------ |
| Decode publish, 64B payload | 40.0 ns, **0 B/op, 0 allocs/op** |
| Decode 16-message batch | 746.6 ns, 0 B/op, 0 allocs/op |
| Encode event push, 64B | 36.2 ns, 0 B/op, 0 allocs/op |
| Encode Self-Room message | 25.6 ns, 0 B/op, 0 allocs/op |
| End-to-end protobuf echo, 100k | 27.1 µs/op, 0 B/op, 0 allocs/op |
| Native echo, same harness | 26.9 µs/op, 0 B/op, 0 allocs/op |
| Decoder fuzz | 5.25M execs in 10 s, no crash |

## Named events

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, 8 vCPU. Report:
[`docs/benchmarks/2026-10-23-named-events.md`](docs/benchmarks/2026-10-23-named-events.md).

| Path | Result |
| ---- | ------ |
| `EventIDForName` for `chat` / `chat.message` / `room:presence:update` | 1.1 / 3.4 / 5.3 ns, 0 B/op, 0 allocs |
| Named echo, 100k | 26.8 µs/op, 0 B/op, 0 allocs |
| Topic echo, no hash, 100k | 26.8 µs/op, 0 B/op, 0 allocs |
| Numeric echo, same harness | 27.0 µs/op, 0 B/op, 0 allocs |

## Attached HTTP and fd handoff

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, 8 vCPU. Report:
[`docs/benchmarks/2026-10-24-fd-handoff.md`](docs/benchmarks/2026-10-24-fd-handoff.md).

| Path | Result |
| ---- | ------ |
| `BenchmarkServerEcho100k` | 27,731 ns/op, 0 B/op, 0 allocs |
| `BenchmarkStringEcho100k` | 27,619 ns/op, 0 B/op, 0 allocs |
| `BenchmarkTopicEcho100k` | 27,377 ns/op, 0 B/op, 0 allocs |
| `BenchmarkProtoSendEcho100k` | 28,027 ns/op, 0 B/op, 0 allocs |
| `PrepareUpgrade` | 0 allocs/op for the valid request matrix |
| Attached hold | 6 minutes, 0 failures (not the 60-minute gate) |

## Documentation

| Document | Contents |
| -------- | -------- |
| [architecture](docs/architecture.md) | ownership, buffers, timers, rooms, shutdown |
| [protocol](docs/protocol.md) | native envelope, command protocol, close codes |
| [threat model](docs/threat-model.md) | trust boundaries, trusted proxy, io_uring |
| [operations](docs/operations.md) | tuning, metrics, runbooks |
| [benchmarks](docs/benchmarks/README.md) | method and evidence rules |
| [ADRs](docs/adr) | decisions that constrain the code |
| [contributing](docs/CONTRIBUTING.md) | build tags, allocation gates, review checklist |

## Development

```sh
make fmt vet test test-race checkptr docs-check
make cross
make fuzz
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
