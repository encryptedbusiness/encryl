# M0 execution log — Linux kernel verification

What was actually run, what broke, what was changed, and what the numbers are.
This is the narrative companion to the reports under `docs/benchmarks/`; the
decision records are `docs/adr/0001`–`0011`.

## Environment

| Field | Value |
| ----- | ----- |
| Host OS | macOS 26.5.1, Darwin 25.5.0, Apple Silicon (`arm64`) |
| Virtualization | Lima 2.2.1, `vmType: vz` (Apple Virtualization.framework) |
| Guest | Ubuntu 24.04 LTS cloud image, `lima-encryl-dev` |
| Guest kernel | 6.8.0-147-generic |
| Guest CPU / memory | 4 vCPU ARMv8 virtual CPU / 7.7 GiB usable |
| Go | go1.27.2 linux/arm64 |
| kTLS | `tls` module loaded, `/proc/net/tls_stat` present |
| Sysctls | `net.core.somaxconn=65535`, `ip_local_port_range=10240 65535` |
| Descriptor limits | soft 1024 / hard 1048576 in an SSH session; baseline script raises it |
| Code verified | `752e3ac..07f2ebe` on `v2` |

The dev host is not the target hardware. All kernel-facing work runs in the VM;
the macOS job only builds and unit-tests the portable code.

## 1. Provisioning

```bash
brew install lima                     # 2.2.1
limactl start --name=encryl-dev --tty=false deploy/lima.yaml
```

The image download and first boot happen during provisioning; no manual guest
setup was required. Provisioning installs curl/git/build tools, downloads Go
1.27.2, loads the `tls` module, applies the sysctls from
`docs/operations.md`, and raises `nofile`.

Observed during boot:

- A warning about a `/tmp/lima` mount referring to a non-existent host
  directory. The mount was unnecessary and has been removed from
  `deploy/lima.yaml`.
- Lima prints its SSH port; nothing needed configuring.

Verification:

```console
$ limactl shell encryl-dev -- bash -lc 'uname -r; nproc; go version; lsmod | grep tls'
6.8.0-147-generic
4
go version go1.27.2 linux/arm64
tls                   159744  0
```

## 2. io_uring spike

### 2.1 Probe

```console
$ ./scripts/linux-run.sh go run ./internal/spike/uring/cmd/uring-spike -probe
probe: kernel=6.8.0-147-generic (>=6.6) entries=1024 restrictions=true buffer_ring=false
  accept=true recv=true send=true close=true provide_buffers=true send_zc=true
```

The first run failed with `EBADFD` from `IORING_REGISTER_RESTRICTIONS`. That
started a chain of five defects, all confirmed with either the binding or a raw
C control program.

### 2.2 Defects found and fixed

| # | Symptom | Root cause | Fix |
| - | ------- | ---------- | --- |
| 1 | `RegisterRestrictions` → `EBADFD` | Restrictions require `IORING_SETUP_R_DISABLED` and `IORING_REGISTER_ENABLE_RINGS`; an enabled ring rejects them | Ring is created disabled, restricted, then enabled |
| 2 | `SetupBufRing` → `EPERM` | Restrictions also gate `io_uring_register`; the register opcode was not allowlisted | Added `IORING_REGISTER_PBUF_RING`, `UNREGISTER_PBUF_RING`, files, eventfd, sync-cancel to the allowlist |
| 3 | `submit` → `EEXIST` | `IORING_SETUP_SINGLE_ISSUER` binds the ring to the creating task; `New` and `Run` ran on different OS threads | Ring creation, restriction, enablement, buffer publication, and submission all happen in `Run` on one locked thread |
| 4 | `SetupBufRing` → `EINVAL` | `IORING_REGISTER_PBUF_RING` rejects every documented form on this host: direct and kernel-allocated, entries 1–32768, alignments 4 KiB–16 MiB, root and unprivileged. A raw C syscall using the kernel's own header fails identically; `ftrace` shows `io_register_pbuf_ring` returning in ~1.6 µs with no child calls | Probe reports `buffer_ring=false`; the spike uses classic `IORING_OP_PROVIDE_BUFFERS` + `IOSQE_BUFFER_SELECT` |
| 5 | Recv/`ENOBUFS` busy loop under an empty pool | Classic buffers have no ring; re-arming a multishot receive immediately returns `ENOBUFS` again | A parked-connection queue resumes one connection whenever a buffer is recycled |

The buffer-ring failure is not a binding bug: the same syscall in C, with
`<linux/io_uring.h>` from the running kernel, returns `EINVAL` too. Details and
the test matrix are in `docs/benchmarks/2026-10-22-m0-linux-verification.md`.

### 2.3 Echo results

Single client:

```console
reply=[hello-uring]
accepted=2 closed=2 echoed=1 bytes_in=11 bytes_out=11
stale=0 cq_overflow=0 bufs_exhausted=0 recv_err=0 send_err=0 partial=0
submissions=7 completions=9 duration=6.002244613s
```

200 concurrent clients against 64 buffers, to force recycling:

```console
accepted=200 closed=200 echoed=200 bytes_in=1400 bytes_out=1400
stale=0 cq_overflow=0 bufs_exhausted=0 recv_err=0 send_err=0 partial=0
submissions=801 completions=1200 duration=10.029245872s
```

Every message echoed exactly once. No stale generation was observed because the
test opens and closes cleanly; stale-completion rejection is covered by the
portable slot-table tests and remains an M1 integration test.

## 3. kTLS spike

### 3.1 First result and the wrong model

The first run reported `AppRecordsConsumed = 1` even for a client that waited
for the server, and `2` for a pipelining client. Instrumenting the record
counter explained it: the records seen during the handshake are
`[22, 20, 23]` — ClientHello, dummy ChangeCipherSpec, and the client's
**Finished**, which TLS 1.3 wraps in an outer `application_data` record.

Two corrections followed:

1. The counter now distinguishes the **application key epoch** from the
   handshake epoch. The Finished is consumed but does not advance the kernel's
   application sequence. A clean client therefore reports 0 and arms RX at
   sequence 0.
2. A drain of already-decrypted plaintext was implemented, then removed: it
   cannot fix the kernel sequence, and it introduces a partial-record failure
   mode.

### 3.2 Defects found and fixed

| # | Symptom | Root cause | Fix |
| - | ------- | ---------- | --- |
| 6 | RX fails with `EBADMSG` after a client pipelines data | crypto/tls consumes the pipelined application record during the handshake; arming `TLS_RX` afterwards fails at every starting sequence tested (0, 1, 2, 3), although a control test proves the kernel honors `rec_seq` | Keep TX offload, leave RX in userspace, return `RXFallback=ErrPipelinedAppData`; never silently downgrade |
| 7 | A read deadline in the past blocked forever | `SO_RCVTIMEO` with a zero timeval means "no timeout" on Linux; the expired-deadline path produced zero | Expired deadlines map to 1 µs |

### 3.3 Results

```console
=== RUN TestHandshakeModes/userspace  suite=0x1301 recordTypes=[22 20 23] rxRecords=0
=== RUN TestHandshakeModes/tx         suite=0x1301 recordTypes=[22 20 23] rxRecords=0
=== RUN TestHandshakeModes/txrx       suite=0x1301 recordTypes=[22 20 23] rxRecords=0
--- PASS: TestHandshakeModes
=== RUN TestPipelinedAppData
    rxRecords=1 fallback=application data arrived before RX takeover; RX stays in userspace
--- PASS: TestPipelinedAppData
```

After the suite:

```console
$ grep -E 'Tls(Rx|Tx)Sw' /proc/net/tls_stat
TlsRxSw   17
TlsTxSw   27
```

Both directions were genuinely offloaded when the handshake was clean, not just
reported as such.

## 4. v1 baseline

### 4.1 Generator defects found while trying to run it

| # | Symptom | Root cause | Fix |
| - | ------- | ---------- | --- |
| 8 | A completed run hung forever | The duration only stopped spawning; nothing cancelled the context, so the final `<-ctx.Done()` blocked | `context.WithTimeout` over the signal context |
| 9 | Runs stopped at exactly 4096 connections | The dial semaphore was released only when the connection closed | Release it immediately after the dial attempt; it bounds concurrent dials, not live connections |
| 10 | Ramp never exceeded ~1000 conns/s regardless of `-rate` | Sleeping `interval` per iteration is capped by ~1 ms timer granularity | Absolute schedule: accumulate `next`, sleep only when ahead, catch up in bursts |
| 11 | Kernel memory sampled after teardown | `sockstat`/slab were read after the load generator exited | A 1 s sampler runs during the soak; the report uses peak/live values |
| 12 | `make bench-baseline` ran on macOS and refused to start | The target did not dispatch through the Linux wrapper | Dispatches via `scripts/linux-run.sh`, like the spikes |
| 13 | `limactl shell` passed `--workdir` to bash | Flags were placed after the instance name | Flags precede the instance: `limactl shell --tty=false --workdir $PWD encryl-dev <cmd>` |

### 4.2 Results

Idle WebSockets, v1.0.2 example server, `GOMAXPROCS=1`, no TLS:

| Target | Peak active | Failed | Server RSS peak | Process RSS / conn | TCP slab / conn |
| ------ | ----------- | ------ | --------------- | ------------------ | --------------- |
| 20k | 18,593 | 0 | 163.44 MiB | 9.00 KiB | 2.29 KiB |
| 50k | 50,000 | 0 | 430.11 MiB | 8.81 KiB | 2.37 KiB |
| 100k | 100,000 | 0 | 924.63 MiB | 9.47 KiB | 2.37 KiB |
| 200k | 200,000 | 0 | 1751.39 MiB | 8.97 KiB | 2.37 KiB |

TCP slab object size is 2368 B. Cost is linear at roughly **11.3 KiB per idle
WebSocket connection**, split as ~9 KiB process and ~2.4 KiB kernel.

The historical README claim — 201,624 connections at 1676.64 MB, i.e.
8.51 KiB/connection — is within 5% of the measured per-connection figure. Its
order of magnitude was plausible; its reproducibility was not. It has been
replaced by `docs/benchmarks/2026-10-22-v1-scaling.md`.

Full reports: `2026-10-22-v1-baseline.md` (200k canonical), `-20k`, `-50k`,
`-100k`, and the scaling summary. Raw CSVs are under `docs/benchmarks/raw/`
(gitignored).

## 5. What is committed where

| Commit | Contents |
| ------ | -------- |
| `84d54fc` | v2 module skeleton, public API, config, errors, backend contract |
| `6d72575` | M0 specification: roadmap, architecture, protocol, threat model, operations, migration, 11 ADRs |
| `fd9ef4e` | io_uring and kTLS spikes, `internal/ktls`, vendored patched giouring, vectors |
| `8be5700` | load generator, Lima config, scripts, CI skeleton |
| `752e3ac` | lint configuration |
| `fd9ef4e` | spike fixes from this verification run |
| `07f2ebe` | load generator and baseline-script fixes, benchmark reports, ADR amendments |

Nothing has been pushed.

## 6. Reproduce

```bash
make limactl-up
make preflight
make spike-uring
make spike-ktls
make bench-baseline CONNS=200000 DURATION=12s RATE=60000 SRC_IPS=16
limactl stop encryl-dev
```

The VM is stopped after a session; `limactl start encryl-dev` brings it back
without reprovisioning.

## 7. Remaining M0 gates

These are performance and cross-kernel questions, not functional ones:

1. **io_uring vs epoll.** ADR-0001's threshold (±15% throughput, −20% syscalls
   at 100k) needs an epoll baseline. The epoll backend is M1 work, so the
   verdict stays pending and no io_uring performance claim is allowed.
2. **WSS memory.** kTLS works, but per-connection memory has not been measured
   at 10k/100k idle. That measurement needs the M1 server.
3. **Buffer-ring portability.** The ring is unavailable on 6.8/arm64. Whether
   it works on 6.12 LTS or x86-64 determines whether the uring backend can meet
   the idle-memory goal or must refuse to run without rings.
4. **Pipelining prevalence.** The RX fallback is correct but per connection.
   M2 must measure how often real clients trigger it under the protocol rule
   that events wait for the AUTH ACK.
5. **Kernel matrix.** Only 6.8 has been run. 6.12 LTS and the 7.x target remain.
