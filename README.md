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
The target is a room with many listeners and few speakers. A million-member
room may see ten messages a second; the other 999,990 connections must cost
almost nothing. That constraint drives the slab, the per-loop arenas, and the
bounded write queue.

## Status

v1.1.0. Bebop is the only client event codec; the native
`encryl.v2` envelope stays for the built-in path. The public echo path
is 0 B/op and 0 allocs/op.

## Results at a glance

- 1M idle connections at **0.275 KiB total RSS/connection**; the gate delta is
  0.241 KiB.
- 1M echo shape with **exact in/out parity** (1,886,007 / 1,886,007), zero
  write-queue drops, zero backpressure.
- 60-minute 1M hold: **360,018,000** broadcast frames, zero drops, no fd growth.
- 1M-member fan-out at one broadcast per second is a capacity limit on the
  8-vCPU host (about 32.5% delivered); the five-second interval delivers 100%.
- Autobahn cannot run in the arm64 guest because the suite image is amd64-only.

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
  request ID, then the opaque payload. It stays for the built-in path.
- `encryl.bebop.v1` (or `?format=bebop`) is the only client event codec. A
  connection with no subprotocol negotiates Bebop. The format uses Bebop's
  fixed-width little-endian values with a one-byte presence bitmap, so absent
  fields cost nothing. Pushes use a one-byte frame type and skip the reply ID.

One WebSocket binary message carries one or more varint-length-delimited
messages. Client commands get a reply with the same ID; pushes carry their own
frame type. The codec is hand-written and zero-allocation. See
[`docs/protocol.md`](docs/protocol.md) and
[`ADR-0023`](docs/adr/0023-bebop-default.md).

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

## Bebop codec

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, 8 vCPU. Full data:
[`docs/benchmarks/`](docs/benchmarks/) and
[`ADR-0023`](docs/adr/0023-bebop-default.md).

| Path | Result |
| ---- | ------ |
| Decode publish, 64B | **10.6 ns**, 0 B/op, 0 allocs |
| Decode 16-message batch | **162.6 ns**, 0 B/op, 0 allocs |
| Encode event push, 64B | **17.9 ns**, 0 B/op, 0 allocs |
| Encode Self-Room push, 64B | **15.8 ns**, 0 B/op, 0 allocs |
| Fan-out to 1000 recipients | **1696 ns**, 0 B/op, 0 allocs |
| Event-push wire size | **84 B** |

Bebop decodes 3.3x faster than the previous codec and encodes pushes 15-19%
faster, with 2.7-6.4% more bytes on the wire.

## Message path

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

## Scale

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
| `PrepareUpgrade` | 0 allocs/op for the valid request matrix |
| Attached hold | 6 minutes, 0 failures (not the 60-minute gate) |

The attached path is codec-independent; the Bebop codec table above covers the
message path.

## HTTP benchmark

Ubuntu 24.04 / kernel 6.8 / arm64 / Go 1.27.2, one vCPU / 2 GiB cgroup, wrk
at 128 connections. Report:
[`docs/benchmarks/2026-10-25-http-1c2g.md`](docs/benchmarks/2026-10-25-http-1c2g.md).

| Transport | `/json` req/s | `/plaintext` req/s | `/json` p50 | `/json` p99 |
| --------- | -----------: | -----------------: | ----------: | ----------: |
| Fiber v3 / fasthttp | 269,777 | 270,815 | 0.471 ms | 1.02 ms |
| httpuring raw HTTP/1.1 loop | **396,080** | **466,199** | **0.275 ms** | 5.99 ms |
| chi on httpuring, inline | 337,196 | — | 0.34 ms | 3.53 ms |
| httpuring + fasthttp | 104,838 | 105,174 | 1.20 ms | 1.74 ms |
| standard net/http | 142,473 | 143,001 | 0.91 ms | 2.15 ms |
| httpuring + net/http | 67,325 | 66,910 | 1.88 ms | 3.03 ms |

The raw loop wins throughput and p50; Fiber wins the rare tail because its
scheduler prevents one event loop from head-of-line blocking every connection.
JSON is not the bottleneck: a custom stack-buffer writer is 12.8 ns/op, 0 B/op,
0 allocs.

## Limits

- Linux 6.6+ and io_uring only. Containers that deny `io_uring_setup` fail at startup.
- Plaintext `ws://` only; TLS terminates upstream.
- Bebop is the only client wire codec. There is no JSON wire format.
- A 1M-member fan-out at one broadcast per second exceeds the 8-vCPU test host;
  it is a capacity limit, not a correctness failure.
- Autobahn has not run in the arm64 guest; the suite image is amd64-only.

The full gate is:

```sh
make fmt vet test test-race checkptr docs-check
make cross
make fuzz
```

`test-race` and `checkptr` run on the Linux VM. `make fuzz` covers the frame,
upgrade, envelope, and Bebop decoders. Allocation gates in `event` and
`connslab` fail the build if the hot path allocates.

## Repository layout

```text
encryl.go, api.go   package documentation and stable public aliases
internal/app/       facade: Server, Conn, config, dispatch, attach, lifecycle,
                    events, emitters, stats
event/              envelope, registry, middleware, ACKs
room/               directory, Self-Room resolver, fan-out planner
connslab/           slab, state machine, frames, write queue
ws/, httpx/         RFC 6455 and HTTP upgrade parsers
protocol/           Bebop codec, session, event names
reactor/            io_uring loop, commands, timers, send path
uring/, netio/      ring, tags, probe, listener, arenas
httpuring/          HTTP/1.1 server and net.Listener adapter over uring
timerwheel/         hierarchical deadlines
adapters/           fasthttp and Fiber v3 modules
bench/              codec, room, reactor, HTTP, and workload benchmarks
examples/           echo, chat, HTTP, mux, uringhttp, Fiber
scripts/            Linux runner, memory/scale gates, practical runs
deploy/             Lima VM and host bootstrap
docs/               architecture, protocol, ADRs, threat model, benchmarks
```

## Documentation

| Document | Contents |
| -------- | -------- |
| [architecture](docs/architecture.md) | ownership, buffers, timers, rooms, shutdown |
| [protocol](docs/protocol.md) | native envelope, Bebop, close codes |
| [events](docs/events.md) | named events, topics, borrowed payloads |
| [frameworks](docs/frameworks.md) | net/http, fasthttp, Fiber ownership rules |
| [threat model](docs/threat-model.md) | trust boundaries, trusted proxy, io_uring |
| [operations](docs/operations.md) | tuning, metrics, runbooks |
| [benchmarks](docs/benchmarks/README.md) | method and evidence rules |
| [production guide](docs/high-performance-backend.md) | deployment, backpressure, SQLite/geohash |
| [ADRs](docs/adr) | decisions that constrain the code |
| [contributing](docs/CONTRIBUTING.md) | build tags, allocation gates, review checklist |

## Production guide

For single-CPU and multi-CPU deployment patterns, authentication, backpressure,
SQLite/geohash design, and tuning, see
[`docs/high-performance-backend.md`](docs/high-performance-backend.md).

## License

MIT.
