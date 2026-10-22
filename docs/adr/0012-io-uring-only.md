# 0012 — io_uring is the only I/O backend

Status: Accepted; module split updated by ADR-0014
Date: 2026-10-22
Deciders: Sairash

Supersedes [0001](0001-io-backend.md).

## Context

ADR-0001 made epoll the portable default and kept io_uring as an opt-in
backend, gated on a measured comparison. M0 proved the io_uring feature set the
server needs (multishot accept, multishot recv, classic provided buffers,
`SEND_ZC`, restrictions) on Ubuntu 24.04 / kernel 6.8 / arm64. It also found
that `IORING_REGISTER_PBUF_RING` is unavailable on that host.

The product direction has since changed. The server is a room/event transport
for high-density synchronization workloads: many mostly-idle connections, small
events, and encode-once fan-out to room members. In that workload the
portable-default argument is worth less than a single, tightly controlled
completion path with no readiness abstraction in between.

Maintaining two backends also costs twice the correctness work for the
invariants that matter here: buffer ownership, cancel-before-close, stale
completions, and exactly-once descriptor close.

## Decision

1. **io_uring is the only backend.** `Backend`, `BackendAuto`,
   `BackendEpoll`, and `BackendUring` are removed from the public API.
2. The backend lives in `internal/ureactor`; the backend-neutral
   `internal/ioloop` contract is deleted. There is one concrete completion
   path, dispatched by decoded `(op, slot, generation)`.
3. The runtime is **Linux-only**. Non-Linux builds compile against stubs;
   `New` and `ListenAndServe` return `ErrUnsupported`.
4. The kernel floor remains Linux 6.6. The startup probe fails closed if a
   required opcode or flag is missing.
5. The baseline buffer strategy is **multishot poll plus one-shot recv into a
   per-loop userspace RX arena**. A connection holds no read buffer while idle.
   The provided-buffer ring and classic provided buffers remain optional later
   optimizations; classic re-publication with an explicit bid returned `EINVAL`
   on the measured 6.8/arm64 host.
6. `IORING_REGISTER_RESTRICTIONS` allowlists every opcode the server uses.
7. The uring-vs-epoll performance gate in ADR-0011 is withdrawn. It cannot be
   evaluated without a second backend, and the architecture no longer wants
   one. Performance claims are measured against the v1 baseline instead.

## Consequences

- The server cannot run on macOS, the BSDs, or in containers whose seccomp
  profile denies `io_uring_setup`. This is accepted and must be documented for
  operators.
- One completion model, one cancel model, one buffer model. The invariant tests
  target a single implementation.
- The io_uring trust statement is now the default posture, not an opt-in
  caveat: the process must be trusted, privileges should be dropped after ring
  setup, and restrictions are mandatory.
- The M0 spike code remains as historical evidence; it is not the production
  backend.
- A future portability requirement would need a new ADR and a new backend; it
  is not a configuration flag.

## Alternatives rejected

- **Keep epoll as the default.** It doubles the invariant surface for a
  workload that is already Linux-deployed and io_uring-shaped, and it makes the
  completion path conditional everywhere.
- **Automatic backend selection.** It hides which path a deployment uses,
  which is exactly the silent difference the project forbids.
- **io_uring with an epoll fallback in containers.** A fallback would silently
  change the memory and latency behavior measured in the benchmarks. Refusing
  to start is the honest behavior.
- **kqueue for macOS.** It would be a third backend with its own buffer and
  cancellation model, for a platform the project does not target.
