# 0011 — M0 verdicts and frozen budgets

Status: Proposed
Date: 2026-10-22
Deciders: Sairash

This ADR is the M0 exit gate. It stays `Proposed` until every row has a
measured value. Functional verification is complete; the performance and
density rows still require the io_uring reactor that M1 provides.

> **Amendment 2026-10-22 — architecture pivot.** ADR-0012 makes io_uring the
> only backend and ADR-0013 moves TLS termination upstream. As a result:
>
> - Q1 (io_uring vs epoll) is **withdrawn**: there is no epoll implementation
>   and no comparison to make.
> - Q2 (kTLS WSS memory) is **withdrawn**: the library has no TLS path and
>   makes no WSS claim.
> - Q3's pipelining finding remains historical evidence for kTLS; it no longer
>   constrains the library.
> - Q4's binding findings and Q5 (fixed-file table) remain relevant to the
>   io_uring-only reactor.
> - The density budget below is a **plaintext** budget. The WSS row is
>   removed, not deferred.

## Environment used

| Field | Value |
| ----- | ----- |
| Host | Lima 2.2.1 (Apple Virtualization.framework), 4 vCPU, 7.7 GiB |
| Kernel | 6.8.0-147-generic (Ubuntu 24.04) |
| Go | 1.27.2 linux/arm64 |
| Commit | `e62f4b8` |
| Report | `docs/benchmarks/2026-10-22-m0-linux-verification.md` |

## Q1 — Is the io_uring backend worth keeping?

Threshold, fixed in advance in ADR-0001: at 100k active connections, io_uring
must deliver ≥15% higher message throughput at equal or better p99 **and** ≥20%
fewer syscalls per delivered message than epoll on the same host and run.

Functional result: **pass**. The spike holds connections, echoes every message,
and survives a deliberately undersized buffer pool with zero errors.

| Metric | epoll | io_uring | Verdict |
| ------ | ----- | -------- | ------- |
| msg/s at 100k | pending | pending | M1 |
| p99 end-to-end | pending | pending | M1 |
| syscalls per message | pending | pending | M1 |
| RSS per idle connection | pending | pending | M1 |

Performance verdict: **withdrawn** by ADR-0012. The io_uring backend is kept as
the only backend on architectural grounds. Its performance is measured against
the v1 baseline, not against an epoll implementation that does not exist.

## Q2 — Does kTLS fit the WSS memory budget?

Functional result: **pass**. Userspace, TX-only, and TX+RX handshakes all work
for all three TLS 1.3 suites. `/proc/net/tls_stat` shows real offload
(`TlsTxSw=27`, `TlsRxSw=17`), not just a successful `setsockopt`.

| TLS mode | Process RSS / idle conn | Total host memory / conn | Offload rate |
| -------- | ----------------------- | ------------------------ | ------------ |
| userspace | pending | pending | 0 |
| tx | pending | pending | pending |
| txrx | pending | pending | pending |

Memory verdict: **withdrawn** by ADR-0013. TLS terminates at the load balancer;
the library is plaintext-only. The kTLS spike remains as M0 evidence.

Constraint discovered: RX offload cannot be armed after crypto/tls has consumed
an application record (see Q3). Any offload rate measured under pipelining
traffic must report the fallback share, not assume 100%.

## Q3 — Does crypto/tls consume pipelined application data?

**Answered: yes, and RX cannot recover.**

- A synchronized client consumes exactly one outer type-23 record during the
  handshake: the client's Finished, which belongs to the handshake key epoch.
  RX arms at sequence 0 and works.
- A client that writes immediately after its Finished causes crypto/tls to
  consume that application record too. Arming `TLS_RX` afterwards returns
  `EBADMSG` for every starting sequence tested (0, 1, 2, 3).
- A separate experiment confirmed the kernel *does* honor `rec_seq`, so this is
  not an ignored field; there is simply no recoverable position.

Decision: keep TX offload, leave RX in userspace, and report
`ErrPipelinedAppData` in `Result.RXFallback`. The stream is intact. M2 must add
the policy: protocol-conforming clients wait for the server's AUTH ACK before
sending events; non-conforming clients get userspace RX or are closed,
depending on `TLSMode`.

## Q4 — Does `giouring` support the required ABI?

**Answered: yes, after a patch and with three operational requirements.**

- Multishot accept, multishot recv, provided buffers, `REGISTER_FILES`,
  `REGISTER_RESTRICTIONS`, `REGISTER_SYNC_CANCEL`, and `SEND_ZC` are present.
- Upstream does not link on Go 1.27 (`//go:linkname syscall.munmap`). Vendored
  and patched under `internal/uring/giouring`.
- `IORING_REGISTER_RESTRICTIONS` requires `IORING_SETUP_R_DISABLED` followed by
  `RegisterEnableRings`; an enabled ring returns `EBADFD`.
- Restrictions also gate register operations. `IORING_REGISTER_PBUF_RING` was
  denied with `EPERM` until the register opcode was allowlisted.
- `IORING_SETUP_SINGLE_ISSUER` binds the ring to the creating task. Creation,
  restriction, enablement, buffer publication, and submission must happen on
  one locked OS thread; otherwise every submit returns `EEXIST`.
- `IORING_REGISTER_PBUF_RING` returns `EINVAL` on this host for every
  documented form (direct and kernel-allocated, entries 1–32768, multiple
  alignments, root and unprivileged). A raw C syscall with the kernel's own
  header fails identically. The spike uses classic provided buffers.

## Q5 — Is a sparse fixed-file table a win?

**Pending.** Needs a 100k-connection measurement of `RegisterFiles` cost versus
fd-lookup savings.

## Budgets to freeze

The hypotheses stand. One reference point is now measured: **v1.0.2 costs
~8.97 KiB process RSS + ~2.37 KiB TCP slab per idle WebSocket connection at
200k connections** (4-vCPU VM, kernel 6.8). v2's plaintext hypothesis is
≤1 KiB process RSS, i.e. roughly a 9× reduction, and the WSS hypothesis is
≤4 KiB. See `docs/benchmarks/2026-10-22-v1-scaling.md`.

| Budget | Hypothesis | Method | Status |
| ------ | ---------- | ------ | ------ |
| Process RSS per idle WS connection | ≤ 1 KiB | `VmRSS` slope at 10k | **0.49 KiB steady / 0.59 peak** |
| Kernel poll request per idle conn | measured | `slabinfo` / RSS slope | not isolated |
| Total host memory per idle conn | ≤ 13 KiB | RSS + `sockstat` + slab deltas | pending |
| Allocations per complete message | 0 | `AllocsPerRun`, benchmem | **0 B/op, 0 allocs** |
| Allocations per fan-out message | shared | `AllocsPerRun` around `Emit` | snapshot: 1 alloc/1k members; frame: 1 |
| Syscalls per delivered message | measured | `perf stat` / `strace -c` | pending |
| p99 end-to-end, 64 B unicast, 10k | measured | `loadgen -mode=echo` | pending |
| Reactor lag p99 under load | measured | loop instrumentation | pending |

Measured 2026-10-23 on Linux 6.8/arm64; see
`docs/benchmarks/2026-10-23-m1-reactor.md`. Removed by ADR-0013: process and
total memory per idle **WSS** connection. The library no longer terminates TLS.

## New questions raised by M0

1. Do provided-buffer rings work on 6.12 LTS or on x86-64? If they work
   elsewhere, the io_uring-only reactor can add the ring as a measured
   optimization. The baseline is poll-plus-classic-buffers, so the server does
   not depend on the answer.
2. What is the per-connection kernel cost of one multishot poll request? It is
   the price of holding an idle connection without a userspace read buffer and
   must be measured in M1.
3. Does `MSG_RING` beat eventfd as a cross-loop wake mechanism in this
   workload? Measure both in M3.

## What "done" means

1. Every pending row has a number and a raw-data link.
2. ADR-0012 records the io_uring-only decision and its consequences.
3. ADR-0013 records the TLS-upstream decision and removes the WSS claim.
4. The budget table has numbers, commands, and CI hook names.
5. `make docs-check`, the Linux test job, and the kernel job are green.

The original "M1 does not start until this gate closes" rule is superseded by
the architecture pivot: the M0 comparison gate was withdrawn, and M1 supplies
the numbers for the remaining budgets.
