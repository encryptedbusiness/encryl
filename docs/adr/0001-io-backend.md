# 0001 — io_uring is an optional backend, epoll is the default

Status: Superseded by [0012](0012-io-uring-only.md)
Date: 2026-10-22
Deciders: sairash

> **Superseded.** The project moved to an io_uring-only architecture. epoll was
> never implemented. Keep this ADR for the M0 evidence and the reasoning that
> was valid at the time; 0012 records the decision that replaced it.

## Context

The original v2 plan assumed a per-core io_uring reactor as the only I/O path.
Replacing `godzie44/go-uring` with a modern binding is still necessary (the
pinned version lacks multishot recv, provided buffers, fixed files, `SEND_ZC`,
and restrictions), but "we need a better binding" and "io_uring must be the only
backend" are different claims.

What io_uring genuinely provides:

- Multishot recv with provided-buffer rings: one armed receive per connection,
  no buffer held while idle, no re-arm syscall per event.
- Batched completion processing: one `io_uring_enter` drains many connections.
- `SEND_ZC` for fan-out without a copy per recipient.
- Single-owner rings per core: no cross-core atomics.

What it does not provide, contrary to the plan's framing:

- Higher idle-connection capacity. epoll holds 1M idle sockets today; idle
  memory is bounded by kernel socket slabs, not by the poll mechanism.
- Better streaming throughput. Measured results show a naive io_uring port is
  5–10% *slower* than epoll on large-packet streaming, because submit:complete
  is ~1:1 and ring management is pure overhead. Gains appear on ping-pong and
  batched workloads.
- Portability. io_uring is Linux-only, unavailable on macOS/BSD/Windows, and
  frequently blocked in production:
  - Google disabled it in ChromeOS, blocked it with seccomp on Android, disabled
    it on production servers, and investigated disabling it in GKE Autopilot.
  - 60% of the Linux kernel exploits Google received in the year before that
    disclosure used io_uring. Google's position is that it is safe only for
    trusted components.
  - Docker/containerd default seccomp profiles and gVisor commonly deny
    `io_uring_setup`.
- Go-runtime compatibility. It requires `runtime.LockOSThread`, CPU affinity,
  and a strict ban on Go pointers in `user_data`, because the garbage collector
  cannot see kernel-held values.

## Decision

`internal/ioloop` defines a backend-neutral, callback-based contract. Two
implementations exist:

1. **epoll** — the default, portable, and the only backend that runs on macOS.
2. **io_uring** — opt-in, `//go:build linux`, and gated on measurement.

`BackendAuto` resolves to epoll. io_uring is never selected implicitly.

### Pre-registered go/no-go threshold

Decided before measuring, so the result cannot be rationalized afterwards. Keep
the io_uring backend only if, at 100k active connections on the documented host,
it achieves **both**:

- ≥15% higher delivered-message throughput than epoll at an equal or better p99,
  **and**
- ≥20% fewer syscalls per delivered message.

Otherwise the backend is deleted, and this ADR records the numbers and the
decision to stop. Either outcome is a successful M0.

## Consequences

- The library compiles, tests, and develops on macOS, which is the only machine
  currently available.
- The project remains deployable in containers and sandboxes where io_uring is
  blocked.
- The uring work is evidence-driven and can be cancelled without blocking v2.
- Two backends mean two code paths to test. Mitigation: the backend contract is
  small, and every backend-neutral test runs against both on Linux.

## Alternatives rejected

- **io_uring only.** Unbuildable on the dev machine, undeployable under common
  seccomp profiles, and blocked entirely if the spike's threshold is missed.
- **epoll only.** Leaves the measured throughput and fan-out wins on the table
  and ignores the stated 100k-active-connection goal.
- **Hybrid with automatic selection.** Automatic selection hides which path a
  deployment uses, which is exactly the silent difference this project forbids.
  Selection is explicit.
