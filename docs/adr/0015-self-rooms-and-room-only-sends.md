# 0015 — Self-Rooms and room-only sending

Status: Accepted
Date: 2026-10-23
Deciders: sairash
Amends: ADR-0007 (rooms sharded by loop with encode-once fan-out)

## Context

ADR-0007 described per-loop membership and a reference-counted fan-out block.
The code had neither: membership lived in one global directory with locks, the
shared frame was a plain byte slice, and four send paths existed
(`Conn.Send`, a direct loop frame path, `Server.Emit`, `Server.Broadcast`).

The 1M message run showed the cost: a fixed-rate client filled the one
in-flight send slot and every later echo was counted as a `SendBusy` drop.
There was no per-connection queue and no single place to apply a write policy.

The branch `uring-io-rewrite` had the right idea when it called
`JoinRoom(id, s)` with the session ID, but `newRoom` had no caller, the join
always failed, and the error was ignored. The idea survived; the mechanism did
not.

## Decision

### 1. Every send goes through a room

A connection is always a member of its **Self-Room**. The Self-Room is
addressed by `ConnID` and is never inserted into the directory. A unicast send
is an Emit whose target set is one Self-Room.

The resolver maps a public `{Slot, Gen}` to `{Loop, localSlot, Gen}` by
integer division with the configured per-loop slab size:

```go
loop := id.Slot / perLoop
local := id.Slot % perLoop
```

Storage cost is **zero bytes per connection**. A real map entry would cost
roughly 100 bytes per connection, or about 100 MiB at 1M. The owning loop
validates the generation before the send; a stale ID is dropped there.

### 2. One Emitter serves unicast, rooms, and broadcast

`Emitter` is a value type with fluent `To`, `ToConns`, `Except`, and `Volatile`
selectors. It encodes the frame once and dispatches:

```text
Emit
  ├── Self-Room / ToConns → refs → planner → per-loop command
  ├── named rooms         → sharded snapshot → planner → per-loop command
  └── all                 → one broadcast command per loop
```

The direct-Loop `sendEnvelope` path is deleted. Automatic ACKs and ERROR
envelopes use the same queue and admission gate as application events.

### 3. The frame is reference-counted and shared

`connection.Frame` holds the complete wire frame and an atomic reference count.
One frame is created per Emit. References are retained per destination loop and
per queued connection. The last release returns the buffer and the frame to
their pools. Per-recipient encoding is prohibited.

### 4. The write queue is bounded and synchronous

`connection.Accounting` is the admission gate. It counts queued and in-flight
messages and bytes under one mutex, because the two limits must move together.
`Conn.Emit` reserves budget before it returns:

| Policy | Reaction to a full queue |
| ------ | ------------------------ |
| `Reject` | return `ErrBackpressure`; connection stays open |
| `DropNewest` | discard the new frame, count it, return nil |
| `DropOldest` | evict the oldest queued frame, count it, enqueue |
| `CloseSlowConsumer` | return `ErrSlowConsumer` and close with `1013` |

The loop owns the intrusive queue of `Node` values. An empty queue costs two
pointers in the slot; an idle connection holds no node and no buffer. Control
frames (close, pong, the 101 response) are not charged to the write budget.

### 5. Membership remains global-sharded

ADR-0007 proposed per-loop membership. The implementation keeps the sharded
directory (64 shards by room name) because fan-out is a read of one shard and a
join is a write of one shard, while a per-loop index would duplicate membership
and complicate cross-loop moves. What ADR-0007 actually required — encode once,
batch per loop, remove membership on close, validate generations — is
implemented.

## Consequences

- The 1M echo run has exact parity, zero write-queue drops, and zero
  backpressure. See
  `docs/benchmarks/2026-10-23-m2-vertical-slices.md`.
- Memory is lower than a map-per-Self-Room design by design: 0.392 KiB/conn
  steady at 1M.
- A caller that ignored `Emit` errors must now handle `ErrBackpressure`. The
  default `Reject` policy makes the failure visible instead of silent.
- `Volatile` is a frame flag. A volatile frame takes the free slot or is
  dropped; it is never queued. This matches the socket.io volatile semantic.

## Rejected alternatives

- **One map entry per Self-Room.** It is the obvious implementation and costs
  about 100 MiB at 1M; the resolver makes it unnecessary.
- **A queue per destination loop instead of per connection.** It cannot bound
  one slow consumer without bounding every connection on the loop.
- **Returning admission errors after the fact.** A counter is not a delivery
  guarantee; `Emit` reserves synchronously.
- **Per-recipient frame encoding.** One message becomes N allocations and
  defeats the shared-block design.
