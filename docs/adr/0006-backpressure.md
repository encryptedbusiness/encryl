# 0006 — Bounded write queues with four explicit policies

Status: Accepted
Date: 2026-10-22
Deciders: Sairash

## Context

v1 has no write queue. `Write` submits an independent send per call and treats
a partial send as success, so a slow peer silently corrupts its own stream while
the server keeps allocating. `Broadcast` writes to every subscriber with no
bound at all.

A server that cannot bound outbound memory per connection cannot bound memory at
all: one stalled reader can make the process grow until the OOM killer decides
the policy.

## Decision

1. Each connection has a write queue bounded by **both** bytes
   (`WriteQueueBytes`) and messages (`WriteQueueMessages`). Either limit
   triggers the policy.
2. `Send` is non-blocking and reports admission synchronously:
   `nil` means queued, `ErrBackpressure` means rejected, `ErrClosed` means gone.
3. Four policies, chosen explicitly:
   - `Reject` (default): return `ErrBackpressure`, keep the connection.
   - `DropNewest`: accept and discard the new message.
   - `DropOldest`: evict the oldest message, enqueue the new one.
   - `CloseSlowConsumer`: close with a slow-consumer code.
4. Partial sends are normal: the remainder stays queued and is retried when the
   socket is writable. They are never reported as success.
5. Fan-out uses per-recipient `Reject` regardless of the sender's policy. One
   slow recipient must not reinterpret the broadcaster's intent, and the
   broadcast itself never fails because one peer is slow.
6. Every outcome is counted: `backpressure_total`, `dropped_messages_total`,
   `slow_consumers_total`, plus write-queue histograms.

## Consequences

- Outbound memory is bounded by `connections × WriteQueueBytes` in the worst
  case. That number is now a capacity-planning input instead of a surprise.
- Callers must handle `ErrBackpressure`. That is the point: the alternative is a
  silent drop or an unbounded buffer.
- `DropOldest` needs a deque, not a channel. Channels cannot evict from the
  head.
- A zero-copy send retains its buffer until completion, so the byte budget must
  account for in-flight sends, not only queued ones.

## Alternatives rejected

- **Return `bool`.** Cannot distinguish "full" from "closed", which v1
  conflates.
- **Block the caller.** Turns a slow consumer into backpressure on the reactor.
- **Unbounded queue.** Moves the failure from a per-connection error to a
  process-wide OOM.
- **Close every slow peer unconditionally.** Too aggressive for telemetry and
  state-snapshot streams where dropping old data is correct.
- **One global queue.** One tenant's slow consumer would starve every
  connection.
