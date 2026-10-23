# 0014 — Vertical slices over a shared reactor kernel

Status: Accepted
Date: 2026-10-23
Deciders: Sairash
Supersedes: the horizontal layout described in `docs/architecture.md` before
this date

## Context

The v2 tree grew as horizontal layers. `encryl.go` was 933 lines and mixed
the public API, the handler map, dispatch, the room fan-out planner, the
connection table, and a buffer pool. `internal/ureactor/loop_linux.go` was 1410
lines and mixed the ring, listener, slab, state machine, parsers, timers, and
write path. (`internal/ureactor` is historical; the package is now split into
`internal/reactor` and `internal/platform`.) A change to ACK semantics or Self-Room resolution touched both
files and every test around them.

The plan also recorded documents that described mechanisms the code did not
have: per-loop room membership, a reference-counted fan-out block, and a
bounded write queue.

## Decision

Split the tree into four vertical slices and one shared engine.

**Slices own a capability end to end: API, state, counters, tests, and
benchmarks.**

| Slice | Package | Owns |
| ----- | ------- | ---- |
| event | `internal/event` | envelope codec, handler registry, middleware chains, pending-ACK slab |
| room | `internal/room` | sharded directory, Self-Room resolver, pooled fan-out planner |
| connection | `internal/connection` | slab, state machine, reference-counted frames, bounded write queue |
| transport | `internal/transport/http`, `internal/transport/ws` | upgrade parser, RFC 6455 parser |

**The facade is one file per concern.** `encryl.go` is package
documentation only. `server.go`, `stats.go`, `dispatch.go`, `conn.go`,
`events.go`, `emitter.go`, `config.go`, and `errors.go` own the public surface.
Dispatch is facade composition, not an event-slice entry point: the slice
exposes `Registry.Lookup`, and `dispatch.go` composes the lookup, the lifecycle
fallback, the error envelope, and the automatic ACK.

**The engine is the shared mechanism.**

| Package | Owns |
| ------- | ---- |
| `internal/reactor` | loop, completion dispatch, timers, commands, send path |
| `internal/platform` | ring, completion tags, probe, listener, RX/TX arenas |
| `internal/timerwheel` | hierarchical deadlines |
| `internal/uring/giouring` | vendored kernel binding |

## Dependency rule

The graph is acyclic and points toward the engine:

```
encryl (facade)
  ├── event
  ├── room ──────────► connection
  ├── connection ────► transport, timerwheel
  ├── transport
  └── reactor ───────► platform, connection, transport, timerwheel
```

Two composition-root exceptions are deliberate: the facade calls
`platform.Listen` to bind, and `reactor.NewLoop` to build the engine. Those are
the only facade-to-engine calls. The facade otherwise reaches the engine only
through the slices.

Slices may use the engine. The engine may use the slice contracts it must drive
on the completion path. The original plan said the kernel must not use a slice;
that rule is relaxed for `reactor` only, deliberately. `reactor` is a mechanism,
not a feature: it must call `Slot` methods and feed bytes to the parsers without
an interface dispatch per completion or per frame. An interface boundary there
would add a virtual call to every `recv`, `send`, and parsed message, and it
would still need the same concrete state. The acyclicity rule is kept.

`event` depends on nothing inside `internal/`; `transport` likewise. `connection`
depends on `transport` for the parser values embedded in a slot. `room` depends
on `connection` for the ref type. No slice imports the facade.

## Layout

```text
encryl.go   package documentation
server.go        Server, New, ListenAndServe, Shutdown
stats.go         Stats, Stats(), rejection counters
dispatch.go      handleOpen, handleClose, handleMessage, ACK timeout, error codes
conn.go          Conn, ConnID, Message, CloseInfo, lifecycle methods
events.go        On, Use, UseConnect, Handler, EventHandler, middleware types
emitter.go       Emitter, To, ToConns, Broadcast, RoomSize
config.go        validated configuration
errors.go        sentinel errors

internal/event       envelope.go, registry.go, ack.go, bench_test.go
internal/room        directory.go, snapshot.go, selfroom.go, fanout.go
internal/connection  slab.go, writeq.go (frames and BuildFrame), batch.go
internal/transport   http/upgrade.go, ws/ws.go, ws/frame.go
internal/reactor     api.go, command.go, loop_linux.go, accept_linux.go,
                     recv_linux.go, upgrade_linux.go, send_linux.go,
                     close_linux.go, timers_linux.go, commands_linux.go,
                     shutdown_linux.go, stub_other.go
internal/platform    op.go, ring_linux.go, probe_linux.go, listener_linux.go,
                     bufs.go, stub_other.go
internal/timerwheel  wheel.go
```

## Consequences

- A feature change touches one slice. An ACK rule changes `internal/event`; a
  queue policy changes `internal/connection`; a room-address rule changes
  `internal/room`.
- Allocation and memory gates run per slice, not against one monolith.
  `BenchmarkServerEcho100k` remains the end-to-end gate.
- The facade is small enough to read in one sitting, and the public surface
  lives in named files rather than one 933-line file. `buildFrame` lives in
  `internal/connection` as `BuildFrame`, so the facade imports no transport
  package.
- The engine still owns the hot path. That is the trade-off that keeps the
  measured numbers at zero allocations.

## Rejected alternatives

- **Keep the horizontal layout and only rename files.** The fan-out planner and
  the write path would still be split across the facade and the reactor.
- **Put an interface between the reactor and every slice.** It adds a dispatch
  per completion and per frame for no testability gain; the reactor is already
  tested through its own integration suite.
- **Move the reactor into `encryl`.** The facade would then own the ring,
  the slab, and the parsers, which is the problem this ADR solves.
