# 0007 — Rooms sharded by loop with encode-once fan-out

Status: Accepted; amended by ADR-0015
Date: 2026-10-22
Deciders: sairash

> NOTE: ADR-0015 amends this decision. Membership stays in the global sharded
> directory rather than a per-loop index, and the reference-counted fan-out,
> encode-once block, close-time removal, and generation checks described here
> are implemented. Self-Rooms are added and cost zero storage.

## Context

v1 rooms are `sync.Map` sharded by a hash of the room name, holding
`*Subscriber`. `Emit` iterates the room, calls `Write` per member, allocates an
error channel sized to the room, and discards the results. Empty rooms are never
reclaimed. Membership is not removed on disconnect.

The requirement is a fan-out that is correct under concurrency and does not
allocate per recipient.

## Decision

1. **Membership is owned by the connection's loop.** A room is a set of
   `ConnID`s plus a per-loop index. Join and leave happen on the owning loop,
   so there is no cross-core lock on membership.
2. **The frame is encoded once** into an immutable, reference-counted buffer.
   The reference count is incremented per submitted send and decremented on
   completion.
3. **Cross-loop fan-out is batched**: one command per destination loop carrying
   the shared buffer and a list of destination slots, not one command per
   recipient.
4. **Membership is removed during the owner-loop close transition**, before the
   descriptor is closed. Empty rooms are reclaimed immediately.
5. **A close that races a fan-out is safe**: the destination loop sees a stale
   generation and drops the reference.
6. **`Emit` accepts one room per call.** Broadcasting to several rooms is
   several calls with the same encoded frame. This keeps the API honest about
   the work being done and allows per-room policy later.

## Consequences

- Fan-out cost is O(recipients) pointer handoffs and O(rooms) encodes, not
  O(recipients) encodes.
- Room iteration never blocks the sender's loop: the destination loops do the
  sends.
- `GetRoomMembers` cannot return connections, because handles do not expose
  state. Room introspection becomes counters (`Stats`), which is what
  operational code actually needs.
- Reference counting is a new invariant to test. It gets a leak test: after a
  fan-out storm and a close storm, the pool must be empty.

## Alternatives rejected

- **Global room map with a mutex.** Every join and leave contends, and fan-out
  serializes across cores.
- **Per-recipient encode.** The obvious implementation; it turns one message
  into N allocations and fails the fan-out allocation gate.
- **Copy the frame into every queue.** Bounded memory becomes N × frame size,
  which defeats the queue budget.
- **Broadcast by iterating all connections.** O(all connections) for a room of
  ten; v1 did this on connect, and it does not survive 1M connections.
