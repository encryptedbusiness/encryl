# 0004 — Slot + generation handles and a single-owner lifecycle

Status: Accepted
Date: 2026-10-22
Deciders: sairash

## Context

v1 exposes `*Subscriber` pointers to application code and stores them in maps
and channels. A pointer that outlives its connection either keeps the connection
alive (a leak) or dereferences recycled memory (a use-after-free). Both failure
modes are reachable from ordinary application code: keep a `*Subscriber` in a
map, let the client disconnect, and send to it later.

v2 also needs no per-connection goroutine, channel, or timer, which rules out
the usual "close a channel and let the goroutine notice" pattern.

## Decision

1. Connections live in a per-loop slab. A handle is `ConnID{Slot, Gen}`.
2. Releasing a slot increments its generation. Every operation validates
   `(slot, gen)` before touching state; a mismatch returns `ErrClosed`.
3. The public `Conn` is a value handle `{server, slot, gen}`. It is safe to copy,
   hold, and share across goroutines.
4. Exactly one loop owns a connection. All state transitions happen on that
   loop; there are no locks around connection state.
5. State machine:

   ```
   Accepted → Handshaking → Authenticating → Open → Draining → Closing → Closed
   ```

   Transitions are a table. An illegal transition is counted and treated as a
   close, never a panic: a panic in the reactor kills every connection on that
   core.
6. The descriptor is closed exactly once, by the owner, after the close
   completion is processed. A double close is detected by the slot state and
   returned as an error rather than closing a reused descriptor.
7. Completion callbacks carry `(op, slot, generation)` in `user_data`. A
   completion for a released slot is counted as `StaleCompletions` and dropped.

## Consequences

- Application code can hold handles indefinitely without leaking or crashing;
  late sends return `ErrClosed`.
- No `sync.Map` per connection, no channel per connection, no pointer in
  `user_data` (which would be invisible to the GC and unsafe).
- Handles are not capabilities: `ConnID` is not a secret and must never be used
  for authorization. Identity is a separate, authenticated value.
- Slot reuse is testable: a dedicated test closes and reuses a slot, then
  delivers a forged stale completion and asserts it is rejected.

## Alternatives rejected

- **Pointers with reference counting.** Reintroduces cross-core atomics and
  lifetime bugs; the GC cannot help once the object is pooled.
- **Interfaces holding the connection.** Each interface value allocates and
  hides the generation check.
- **One goroutine and one channel per connection.** The exact model v2 is
  replacing; it costs a stack, a channel, and a timer per connection.
- **Index-only handles without generations.** The ABA problem: a recycled slot
  silently receives another connection's messages.
