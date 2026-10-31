# 0005 — Buffer ownership: provided rings, borrowed payloads, no GC pointers

Status: Accepted
Date: 2026-10-22
Deciders: sairash

## Context

Memory per idle connection is the project's central constraint. Most WebSocket
servers allocate a read buffer per connection and keep it for the connection's
lifetime. At 1M connections a 4 KiB buffer is 4 GiB before any application
state.

The two backends have different natural buffer models: io_uring can hand a
provided buffer to a multishot receive and report its id in the completion;
epoll must read into a buffer the loop owns.

## Decision

1. **Idle connections hold no read buffer.**
   - io_uring: buffers live in a provided-buffer ring; a connection takes one
     only while a recv is in flight.
   - epoll: the loop allocates a staging buffer only when data is ready and
     returns it when the message completes.
2. **A provided buffer has exactly one owner**: the operation referencing it.
   It may not be recycled until that operation completes. A short send keeps the
   buffer and resubmits the remainder; it does not copy.
3. **Handlers receive borrowed payloads.** The slice is valid only for the call.
   Retaining it requires an explicit copy, which is documented on `Message`.
4. **No Go pointers cross the kernel boundary.** `user_data` carries
   `(op, slot, generation)` integers. Pointers in kernel-visible memory are
   invisible to the GC and become use-after-free bugs when the object moves or
   is collected.
5. **Shared fan-out frames are immutable and reference-counted.** One encode,
   many sends; release when the last send completes.
6. **Arena sizing is a formula, not a guess.** For io_uring:
   `bufferCount ≥ loops × maxOutstandingPublishesPerLoop + connectBurst`, and
   buffer size is the p99 message + envelope + frame overhead, measured. Exact
   numbers are frozen in ADR-0011 after the spike.

## Amendment — measured 2026-10-22

`IORING_REGISTER_PBUF_RING` returns `EINVAL` on the first test host (Ubuntu
24.04, kernel 6.8.0-147, arm64) in every documented form. See
`docs/benchmarks/2026-10-22-m0-linux-verification.md` for the matrix and the
raw-C control experiment.

The spike therefore uses **classic provided buffers**
(`IORING_OP_PROVIDE_BUFFERS` + `IOSQE_BUFFER_SELECT`) and reports
`buffer_ring=false` in its probe.

This matters because the idle-memory goal depends on the ring: with classic
buffers, each armed multishot receive holds a buffer, so an idle connection
does hold read memory. The io_uring backend therefore has two regimes:

| Buffer regime | Idle connection memory | Availability |
| ------------- | ---------------------- | ------------ |
| provided-buffer ring | no buffer while idle | kernel- and arch-dependent |
| classic provided buffers | one buffer per armed receive | everywhere since 5.6 |

The production backend must probe and report which regime it is in, and the
memory budget must be stated per regime. A single "≤ N KiB per idle
connection" claim for the uring backend would be false without knowing which
path is active.

## Consequences

- Idle memory is dominated by kernel socket slabs, not userspace buffers.
- A connection that pipelines faster than it drains holds at most one send
  buffer; the spike closes it rather than unbounded buffering. M2 replaces that
  with the bounded write queue.
- Callers who retain `Message.Payload` get a use-after-free only if they ignore
  the documented contract; the payload is not hidden behind an accessor that
  suggests ownership.
- Buffer exhaustion is a normal condition (`ENOBUFS`), not a fatal error: the
  loop re-arms after recycling.

## Alternatives rejected

- **One fixed buffer per connection.** Simple, but breaks the memory budget at
  exactly the scale the project targets.
- **Grow-on-demand slice per connection.** Reallocations and copies on the hot
  path, and the GC sees a live reference per connection.
- **`bytes.Buffer` per connection.** Same problem, plus an allocation per read.
- **Kernel-owned memory via `mmap` with Go pointers.** Not representable: the
  GC cannot track it.
- **Copy every payload into a per-message allocation.** Turns an ownership
  contract into an allocation per message, failing the zero-allocation gate.

## Amendment — io_uring-only baseline (2026-10-22)

ADR-0012 removes epoll and keeps io_uring as the only backend. On the measured
6.8/arm64 host the provided-buffer ring is unavailable, and re-publishing a
classic provided buffer with an explicit bid returned `EINVAL`. The M1 baseline
therefore selects buffers in userspace:

1. A multishot `POLL_ADD` per open connection, holding no data buffer while the
   connection is idle.
2. A one-shot `IORING_OP_RECV` into a buffer taken from a per-loop userspace
   arena only when poll reports readable. The buffer returns to the arena after
   parsing.

This keeps the "idle connection holds no read buffer" rule without the ring or
the kernel buffer group. Measured cost is 0.49 KiB steady-state process RSS per idle
connection at 10k connections. TX uses a per-loop arena with headroom for the
frame header and envelope, so a small unicast message is one
`IORING_OP_SEND` and no iovec array; larger messages fall back to a heap buffer
that is released after the send completes.
