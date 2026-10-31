# Roadmap

This is the whole program, including milestones that are not implemented yet.
M0 is complete. M1 is implemented; its validation runs remain open. The roadmap
M2 is not the "M2" of the vertical-slice track: that track uses its own M2
label for the write queue, Self-Rooms, ACKs, and middleware. Each milestone
below has entry criteria, deliverables, and an exit gate that can fail.

Tracker IDs from the original plan are in parentheses.

---

## M0 — Specification, baselines, backend spikes
**(`spec-baseline`, `prove-kernel-stack`) — complete**

### Deliverables

- v1 frozen at `v1.0.2`; the dead `uring-io-rewrite` branch archived as
  `archive/uring-rewrite` and never merged.
- `docs/` written for contributors: architecture, protocol, threat model,
  operations, dev environment, migration, benchmark methodology, ADRs 0001–0011.
- A reproducible v1 baseline (`docs/benchmarks/*-v1-baseline.md`) produced by
  `bench/loadgen`, replacing the unsourced README table.
- Two spikes: `uring` (multishot accept/recv, provided buffers,
  restrictions, `SEND_ZC`). The kTLS spike was removed with the M0 evidence; the log keeps the result.
- A vendored, patched `giouring` under `uring/giouring` with the Go
  1.27 link failure fixed and documented.
- CI skeleton: Linux tests, kernel-spike job, govulncheck, license and SBOM
  checks, nightly benchmark job.

### Exit gate

Functional verification is complete (2026-10-22): both spikes run on Ubuntu
24.04 / kernel 6.8 / arm64, the v1 baseline is reproducible, and the failures
found are recorded. See `docs/benchmarks/2026-10-22-m0-linux-verification.md`.

Two gates were withdrawn by the architecture change recorded in ADR-0012 and
ADR-0013:

1. ~~io_uring vs epoll throughput and syscalls.~~ Withdrawn. io_uring is now the
   only backend, so there is no second implementation to compare against.
   Performance is measured against the v1 baseline instead.
2. ~~kTLS WSS memory budget.~~ Withdrawn. TLS terminates upstream; the library
   has no TLS path and makes no WSS claim.

Still relevant from M0:

- The io_uring probe, restrictions, and buffer findings remain the backend's
  starting point (ADR-0005, ADR-0011, ADR-0012).
- The v1 reference is ~11.3 KiB per idle connection; v2 targets ≤1 KiB process
  RSS for plaintext WebSocket connections.

---

## M1 — io_uring reactor, lifecycle, RFC 6455, and usable transport
**(`build-reactor` + `build-protocol` transport half) — implemented**

The M1 code path is complete and measured. The remaining checks are validation
runs: the full Autobahn suite (`scripts/autobahn.sh`, needs an amd64 host) and
the 60-minute idle soak (`make soak`). A 60-minute 1M hold with traffic already
ran; see the M3 report.

Bebop is the only client event codec (ADR-0023). It shares the event core and is
not an M1 exit requirement. Codec and end-to-end measurements are in
[`benchmarks/2026-10-31-bebop-codec.md`](benchmarks/2026-10-31-bebop-codec.md).

Baseline reports:
[`docs/benchmarks/2026-10-23-m1-reactor.md`](benchmarks/2026-10-23-m1-reactor.md)
and [`docs/benchmarks/2026-10-23-m1-scale.md`](benchmarks/2026-10-23-m1-scale.md).

> **Scale finding (2026-10-23).** Two tcpkali shards reached **1,000,000 idle
> plaintext connections** with zero protocol/recv/send/eventfd errors. The
> bounded per-connection write queue, Self-Rooms, ACKs, and middleware landed
> in the vertical-slice change (ADR-0014 through ADR-0017). The 1M echo shape
> now has **exact in/out parity** (1,886,007 / 1,886,007), zero write-queue
> drops, zero backpressure, and the steady-state process RSS is
> **0.392 KiB per idle connection**. See
> [`docs/benchmarks/2026-10-23-m2-vertical-slices.md`](benchmarks/2026-10-23-m2-vertical-slices.md).

### Entry criteria

- M0 functional verification passed.
- A Linux 6.6+ target is reachable (`make linux-shell`).
- ADR-0012 (io_uring-only) and ADR-0013 (TLS upstream) are accepted.

### Deliverables

- `reactor` and `uring/netio`: one single-issuer io_uring ring per loop, pinned with
  `runtime.LockOSThread`, `SO_REUSEPORT` listeners, batched one-shot
  `accept4`, multishot poll per open connection, and one-shot recv into a
  per-loop userspace RX arena. **Idle connections hold no read buffer.**
- Connection slab with global `(slot, generation)` IDs, an explicit state
  machine (`Accepted → Handshaking → Open → Draining → Closing → Closed`;
  `Authenticating` reserved for M2), in-flight operation accounting, and
  fd-close-exactly-once via `IORING_OP_CLOSE`.
- Hierarchical timer wheel for handshake, idle, ping, close, and drain
  deadlines. One intrusive node per deadline kind, never a `time.Timer` per
  connection.
- Buffer policy: per-loop RX and TX arenas; a connection owns a buffer only
  while an operation is in flight; every recycle is owner-checked and the
  arena balance is asserted.
- Strict RFC 6455: mask enforcement, fragmentation/continuation, control-frame
  limits, close handshake and codes, multiple frames per recv, partial headers
  and payloads, maximum sizes. Text frames are rejected at the header with
  `1003`; the application protocol is binary-only.
- HTTP/1.1 upgrade parser with strict header limits; `Sec-WebSocket-Protocol:
  encryl.v2` negotiation; Origin checks.
- The `encryl.v2` 16-byte envelope decoder and dispatcher, golden wire
  vectors, ACK/ERROR generation, and CONTROL handling. AUTH is accepted as a
  no-op under the trusted-proxy assumption.
- **Landed early in the vertical-slice track (its own M2 label):** bounded
  per-connection write queues with
  `Reject`, `DropNewest`, `DropOldest`, and `CloseSlowConsumer`; admission is
  synchronous. Encode-once, refcounted fan-out with Self-Rooms, ACKs, and
  middleware. See ADR-0014 through ADR-0017 and the M2 benchmark report.
- Room registry: one global sharded directory, `Join`/`Leave`/`Emit`,
  empty-room reclamation, and `MaxRoomsPerConn`. Membership is not per-loop;
  see ADR-0015.
- Graceful shutdown: stop accepting, send `1001`, drain within
  `ShutdownTimeout`, cancel by exact `user_data`, close descriptors exactly
  once, join every loop.
- Tests: parser table tests, Autobahn, differential fuzz against `gobwas/ws`,
  disconnect at every state, partial reads/writes, RST/half-close, buffer
  balance, forced CQ overflow.

### Exit gate

Measured (2026-10-23, Linux 6.8/arm64):

- 10k plaintext WebSocket connections: 0 failed, **0.49 KiB steady-state
  process RSS per connection** (0.59 KiB peak) over a 10-second run. The
  60-minute soak and the 1 msg/s ×
  10k variant are the remaining runs.
- 1M idle plaintext connections reached on the 8 vCPU/16 GiB scale VM with
  zero protocol/recv/send/eventfd errors; this is a density observation, not
  the 60-minute M3 gate. The bounded write queue closed the old `SendBusy`
  gap: the 1M echo shape now has exact parity and zero drops. See the M2
  vertical-slice and M3 reports.
- Zero steady-state allocations: public echo and raw echo are **0 B/op, 0
  allocs/op** at 100k messages.
- No parser allocations for a message that fits in one buffer.
- One shared frame allocation per fan-out, never per recipient.
- Shutdown with outstanding reads and writes passes; the exact-fd-count check
  is part of the Linux integration suite.

Pending validation:

- Autobahn against the declared whitelist (text cases are expected rejects).
  The harness is ready, but the amd64-only image hangs under QEMU on the arm64
  guest. See [`2026-10-23-m3-validation.md`](benchmarks/2026-10-23-m3-validation.md).
- The full 60-minute idle soak and the 1 msg/s × 10k variant. A 60-minute
  1M hold with one broadcast every 10 seconds ran with zero drops and no fd
  leak; it is a different shape from the idle growth gate.
- 10k-recipient room fan-out under `-race` (the 1k snapshot path is measured;
  the full fan-out run is not a race run).

---

## M2 — Authentication, hardening, security, clients
**(`harden-security`, `complete-tests-ci`, `clients-docs`)**

The write-path and event-API work landed early in the vertical-slice track.
That track uses its own "M2" label. It is not this milestone. Self-Rooms, the
bounded queue, ACKs, middleware, `On`/`Emit`, and the chat example exist. This
milestone is otherwise not started.

Status (2026-10-23): **not started** for this milestone's scope. The native
`AUTH` envelope is a no-op and the server treats the peer as pre-authenticated for
native connections. The Bebop codec added a `Config.TokenVerifier` hook and an
identity on Bebop connections. The named-event API
(`On`/`OnRequest`/`OnConnect`, raw borrowed payloads) landed ahead of this
milestone; see `docs/events.md`. Per-IP limits, native AUTH, and generated
clients remain absent.

### Deliverables

- AUTH verification: first-envelope `AUTH` with a short deadline, an
  application-supplied verifier, per-event authorization hook, and an identity
  available to the token verifier.
- Proxy identity: PROXY protocol or a trusted-header integration so
  authorization and per-IP limits see the client address, not the proxy.
- Defaults: Origin allowlist, per-IP connection caps, message/byte token
  buckets, frame/message/fragment limits, heartbeat. The per-IP and rate
  config fields were removed until their enforcement lands; add them back with
  the code.
- Bounded worker pool for handlers that may block, with a documented
  non-blocking contract.
- Structured, redacted logs; constant-time credential comparison.
- Go and TypeScript clients; the in-tree `examples/chat` named-event/room
  example and the Bebop schema (`protocol/bebop.bop`) exist. The generated
  client and TypeScript codegen remain.
- Full test matrix: unit, race, fuzz, interoperability, fault injection,
  security, benchmark.

### Exit gate

- Every item in `docs/protocol.md` has a golden vector and a test.
- Security tests fail closed: CSWSH, auth bypass, slowloris, floods, slow
  consumers, oversized/fragmented messages.
- `go test -race`, `checkptr`, and the fuzz corpus are clean.
- Autobahn, browser, and Go-client interop all pass.

---

## M3 — Density
**(`scale-release`, first half)**

### Deliverables

- Measurement-driven memory work, one change at a time, `benchstat` + profiles
  attached to each step.
- Staged gates: 100k → 250k → 500k → 750k → 1M idle connections.
- Provided-buffer ring evaluation (where the kernel supports it), provided
  buffer sizing, ring-count tuning, sparse fixed-file table evaluation, and
  `MSG_RING` versus eventfd wake comparison.
- Each stage publishes: app RSS, Go heap/stacks, kernel socket slabs, poll
  request cost, CPU, and failure counts.

### Exit gate

- 1M connections held for 60 minutes on the documented host with no swap, no
  OOM, no fd leak, heartbeat sampling, and bounded reactor lag.
- Memory growth after stabilization < 1%.

Status (2026-10-23): **partially measured.**

- 1M idle connections reached with 0.392 KiB/connection.
- A 60-minute 1M hold with 360 broadcasts delivered 360,018,000 frames with
  zero drops and zero fd growth. RSS stayed between 1.03 and 1.75 GiB with no
  monotonic growth. See
  [`2026-10-23-m3-validation.md`](benchmarks/2026-10-23-m3-validation.md).
- Practical workloads measured: 1:1 unicast at 100k, group fan-out at 100k,
  ACKs at 50k, churn, and an EMQX comparison. See
  [`2026-10-23-m3-practical.md`](benchmarks/2026-10-23-m3-practical.md).
- Open: the staged 100k/250k/500k/750k reports, provided-buffer and ring
  tuning, heartbeat sampling, a reactor-lag metric, and the exact idle
  60-minute growth below 1 percent.

---

## M4 — Release
**(`scale-release`, second half)**

### Deliverables

- `v2.0.0` only after M3 and a security review.
- Release-candidate soak, reproducible benchmark report with confidence
  intervals, operations guide verified against the RC.
- Migration guide exercised on a real v1 application.
- No density wording beyond what the M3 gate measured.

---

## Standing rules for every milestone

1. **No number without a command.** Every claim in docs links to a benchmark
   file with host, kernel, commit, and raw data.
2. **One optimization at a time.** Change, measure, keep or revert.
3. **No silent degradation.** Unsupported backends, kernel features, and
   deployment assumptions fail loudly or are explicitly configured; they never
   fall back invisibly.
4. **Record architecture changes before writing code against them.** An ADR
   supersedes the old decision; docs and tests are updated in the same change.
   ADR-0012 and ADR-0013 are the model.
