# Contributing

## Ground rules

1. **No number without a command.** Any performance claim in a doc, PR, or
   commit links to a benchmark file with host, kernel, commit, and raw data.
2. **One optimization per change.** Measure, keep or revert. Do not bundle.
3. **No silent fallback.** An unsupported platform, kernel feature, or
   deployment assumption must produce a documented error or an explicit
   configuration choice.
4. **No public internals.** Maps, channels, descriptors, and mutable connection
   state stay unexported.
5. **Record architecture changes before coding against them.** An ADR
   supersedes the old decision; docs and tests change in the same commit.

## Layout

```
encryl.go        package documentation only
encryl.go       package documentation
api.go               public aliases and constructors
internal/app/        facade implementation and its tests
event/       envelope, registry, middleware, pending ACKs
room/        directory, Self-Room resolver, fan-out planner
connslab/  slab, state machine, frames, bounded write queue
ws/                  RFC 6455 parser
httpx/               HTTP upgrade parser
protocol/    protobuf codec, session, event names, transport seam
reactor/     io_uring loop, commands, send path
uring / netio/    ring, tags, probe, listener, RX/TX arenas
httpuring/   net.Listener/net.Conn stream adapter for net/http
timerwheel/  hierarchical deadlines
uring/       vendored, patched liburing port
bench/loadgen/        reproducible load generator
bench/fanout/         room fan-out harness for Gate C
bench/workload/       unicast, group, ACK, and churn harness
bench/http/           TFB-shaped JSON/plaintext comparison harness
proto/                protobuf schema for external clients
examples/echo/        minimal echo server for soak/Autobahn
examples/chat/        single-listener browser chat: rooms, Topic sends, ACKs
examples/http/        attached net/http server: ServeHTTP and Start
examples/nethttp/     explicit net.Listener + http.Server.Serve + reactor
examples/mux/         path-routed net/http mux with REST broadcast API
examples/uringhttp/   net/http + ServeMux on httpuring's io_uring listener
examples/uringhttp_echo/        raw HTTP echo on httpuring.Server
examples/uringhttp_chi/         chi router + middleware on httpuring (nested module)
examples/uringhttp_websocket/   chi HTTP API + WebSocket upgrade on one listener
examples/fiber/       attached Fiber v3 server; separate module
scripts/              linux-run, memory, gate-*, practical, stress,
                      hold-messages, churn-probe, emqx-compare, autobahn,
                      docs-check
docs/                 architecture, protocol, ADRs, benchmarks
deploy/               Lima VM + host bootstrap
```

The tree is five vertical slices (`event`, `room`, `connection`, `transport`,
`protocol`) over an engine (`reactor`, `uring`, `netio`, `timerwheel`). See ADR-0014.
The dependency graph is acyclic: the facade uses slices, slices use the engine,
and `reactor` drives the `connection` and `transport` contracts directly on the
completion path. `event`, `transport`, and `protocol` do not import another
by the library.

## Build tags

| Tag | Applies to |
| --- | ---------- |
| `linux` | the facade, reactor, uring, netio, and every integration test |

There are no `!linux` stubs. Linux-only packages have no non-Linux files, so an
import of one fails at compile time on macOS.
Pure packages — parsers, envelope, room directory, slab, timer wheel — live in
files without build tags so their tests run wherever they compile. If you add a
Linux-only feature, split the pure state machine out.

## Checks before pushing

```bash
make fmt vet test test-race checkptr cross docs-check
```

On the Linux target:

```bash
make test-uring
make autobahn
```

`go vet` and `golangci-lint` exclude `uring/giouring` because it
contains deliberate `unsafe.Pointer` arithmetic from upstream. That package is
covered by the kernel job instead.

## Linux verification workflow

Everything io_uring-facing runs on the Linux target, not on macOS.

```bash
limactl start encryl-dev          # or: make limactl-up (first time)
make preflight                   # kernel, io_uring opcodes, poll support, limits
make test-uring                  # reactor, transport, rooms, shutdown
make autobahn                    # RFC 6455 conformance
make bench-rooms CONNS=10000
make soak CONNS=10000 DURATION=60m
limactl stop encryl-dev
```

When a run finds something, record it:

1. Add a row to `docs/m0-execution-log.md` (or the M1 log, once it exists) with
   symptom, root cause, and fix. A defect without a reproduction is a rumour.
2. Add or update a dated report under `docs/benchmarks/`. Every report needs
   host, kernel, commit, command, and raw data; `make docs-check` enforces it.
3. If the finding changes a decision, amend the relevant ADR (do not rewrite
   history silently) and update the roadmap if a gate moves.
4. If the finding changes behaviour, add a test that fails without the fix.

## Tests

- Table tests for state machines and parsers.
- Fuzz targets for anything that consumes untrusted bytes: user_data codec,
  frame parser, upgrade parser, envelope decoder.
- Differential tests against `gobwas/ws` where a second opinion is useful. It is
  a test-only dependency, never imported by the library.
- Every disconnect path: during handshake, idle, close, and drain.
- Buffer balance and reference-count conservation under `-race`.
- Race and `checkptr` runs are mandatory for anything touching descriptors or
  generations.

## Allocation gates

Steady-state behaviour is enforced, not aspirational:

| Path | Budget |
| ---- | ------ |
| Idle connection, per second | 0 allocations |
| Complete message fitting one read buffer | 0 parser allocations |
| Public `Send` after buffer-pool warm-up | 0 allocations |
| Public server echo, 100k messages | 0 B/op, 0 allocs/op (measured) |
| Fan-out message | shared frame, never per recipient; snapshot is one allocation per fan-out |

Run `go test -run=NONE -bench=. -benchmem` on the Linux target before changing a
hot path. Use `testing.AllocsPerRun`, heap profiles, and escape analysis in the
PR that touches the path.

## ADR process

Every decision that constrains future code gets an ADR in `docs/adr/`:

```
# NNNN — Title
Status: Proposed | Accepted | Superseded | Rejected
Date: YYYY-MM-DD
Deciders: names

## Context
## Decision
## Consequences
## Alternatives rejected
```

`make docs-check` enforces the header fields and rejects `TBD`. To supersede an
ADR, add a new one and change the old status to `Superseded by NNNN`.

## Vendored code

`uring/giouring` is a pinned copy with a documented patch. If you
update it:

1. Copy the new source.
2. Re-add `//go:build linux` to every file.
3. Re-apply the `linked.go` mmap/munmap patch (see `NOTICE.md`).
4. Run the kernel job.

Do not add it back to `go.mod`.

## Commit style

- Imperative subject, ≤ 72 characters.
- Body explains *why*, and names the benchmark or ADR that justifies a
  performance or architecture change.
- Do not commit generated binaries, profiles, or `docs/benchmarks/raw/`.

## Review checklist

- [ ] Does it add an unbounded allocation reachable by a peer?
- [ ] Does it put a secret in a URL, log line, or error string?
- [ ] Can it block the owning loop?
- [ ] Does it widen the io_uring opcode allowlist?
- [ ] Does it add a release path that can leak a buffer or a fan-out refcount?
- [ ] Does it expose internal state publicly?
- [ ] Does it weaken a default (Origin, limits, policy, trusted-proxy
      assumption)?
- [ ] Is there a test that fails without the change?
