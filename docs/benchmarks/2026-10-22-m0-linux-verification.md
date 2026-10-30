# M0 Linux verification

Functional verification of the two kernel spikes on the first Linux target
available for this project. Performance gates are **not** covered here; they
need the epoll baseline that M1 provides.

| Field | Value |
| ----- | ----- |
| Host | `lima-encryl-dev` (Lima 2.2.1, Apple Virtualization.framework) on macOS 26.5 / Apple Silicon |
| Kernel | 6.8.0-147-generic (Ubuntu 24.04 LTS cloud image) |
| CPU | ARMv8 virtual CPU, 4 vCPU |
| Memory | 7.7 GiB |
| Go | go1.27.2 linux/arm64 |
| Commit | `e62f4b8` |
| Date | 2026-10-22 |
| Command | `make spike-uring`, `make spike-ktls`, `make preflight` |
| Raw | outputs reproduced inline; no CSV for functional runs |
| Sysctls | `net.core.somaxconn=65535`, `ulimit -n=1024` (functional runs; density runs raise it) |
| kTLS | module `tls` loaded, `/proc/net/tls_stat` present |

Environment preflight:

```
kernel=6.8.0-147-generic (>=6.6) entries=1024 restrictions=true buffer_ring=false
  accept=true recv=true send=true send_zc=true close=true provide_buffers=true
```

## io_uring

### Single-connection echo

```
accepted=2 closed=2 echoed=1 bytes_in=11 bytes_out=11
stale=0 cq_overflow=0 bufs_exhausted=0 recv_err=0 send_err=0 partial=0
submissions=7 completions=9 duration=6.002244613s
```

(`accepted=2` because the readiness probe opens and closes one connection.)

### 200 concurrent clients, 64 provided buffers

Deliberately small pool to exercise exhaustion and the parked-connection queue:

```
accepted=200 closed=200 echoed=200 bytes_in=1400 bytes_out=1400
stale=0 cq_overflow=0 bufs_exhausted=0 recv_err=0 send_err=0 partial=0
submissions=801 completions=1200 duration=10.029245872s
```

Every message was echoed exactly once; no stale completions, no CQ overflow, no
descriptor errors.

### Findings

1. **`IORING_REGISTER_RESTRICTIONS` requires a disabled ring.** On an enabled
   ring the kernel returns `EBADFD`. The ring must be created with
   `IORING_SETUP_R_DISABLED`, restricted, then enabled with
   `IORING_REGISTER_ENABLE_RINGS`.
2. **Restrictions gate `io_uring_register` too.** After the allowlist is active,
   `IORING_REGISTER_PBUF_RING` failed with `EPERM` until the register opcode was
   added to the restriction list.
3. **`IORING_SETUP_SINGLE_ISSUER` binds the ring to the creating task.** A ring
   created in one function and submitted from a different locked OS thread
   fails every submission with `EEXIST`. Ring creation, restriction,
   enablement, buffer publication, and submission now happen on one thread.
4. **`IORING_REGISTER_PBUF_RING` returns `EINVAL` on this host.** Verified for
   direct addressing and kernel-allocated (MMAP) modes, entries 1 through
   32768, 4 KiB/64 KiB/2 MiB/4 MiB/16 MiB alignments, and as root and
   unprivileged. A raw C syscall using the kernel's own
   `<linux/io_uring.h>` fails identically, so this is the kernel, not the
   binding. `ftrace` shows `io_register_pbuf_ring` returning in ~1.6 µs with no
   child calls, i.e. an early validation exit.
5. **Classic provided buffers work.** The spike uses
   `IORING_OP_PROVIDE_BUFFERS` with `IOSQE_BUFFER_SELECT`. Exhaustion needs
   accounting: without a parked-connection queue, an empty pool produces a
   recv/`ENOBUFS` busy loop.

## kTLS

```
=== RUN   TestHandshakeModes/userspace   suite=0x1301 recordTypes=[22 20 23] rxRecords=0
=== RUN   TestHandshakeModes/tx          suite=0x1301 recordTypes=[22 20 23] rxRecords=0
=== RUN   TestHandshakeModes/txrx        suite=0x1301 recordTypes=[22 20 23] rxRecords=0
--- PASS: TestHandshakeModes
=== RUN   TestPipelinedAppData
    rxRecords=1 fallback=... RX stays in userspace recordTypes=[22 20 23 23]
--- PASS: TestPipelinedAppData
```

`/proc/net/tls_stat` after the suite:

```
TlsTxSw   27
TlsRxSw   17
```

Both directions were actually offloaded, not merely reported.

### Findings

1. **The handshake consumes the client's Finished as an outer type-23 record.**
   TLS 1.3 encrypts handshake messages in `application_data` records, so a
   naive counter reports one application record even for a perfectly
   synchronized client. The counter must subtract the Finished (handshake key
   epoch) to get the application-epoch count.
2. **A clean client arms RX at sequence 0 and works in all three modes.**
   Userspace, TX-only, and TX+RX all delivered a byte-exact echo.
3. **Pipelined application data makes RX takeover impossible on this kernel.**
   A client that writes immediately after its Finished causes crypto/tls to
   consume that application record. Arming `TLS_RX` afterwards returns
   `EBADMSG` at every starting sequence tested (0, 1, 2, 3), even though a
   separate experiment confirmed the kernel *does* honor `rec_seq`. There is no
   recoverable sequence to resume from.
4. **The fallback is explicit and lossless.** `Handshake` keeps TX offload,
   leaves RX in userspace, returns `ErrPipelinedAppData` in
   `Result.RXFallback`, and hands back a working `net.Conn`. The test reads both
   the pipelined and the following record through it.
5. **An expired `SO_RCVTIMEO` must not be zero.** A zero timeval means "block
   forever" on Linux, so an expired deadline was previously translated into an
   infinite wait. Expired deadlines now map to 1 µs.

## v1 baseline on the same host

The frozen v1.0.2 server was measured with the same generator on the same VM.
Full data: `2026-10-22-v1-scaling.md`; canonical run:
`2026-10-22-v1-baseline.md`.

| Target | Peak active | Failed | Process RSS / conn | TCP slab / conn |
| ------ | ----------- | ------ | ------------------ | --------------- |
| 20k | 18,593 | 0 | 9.00 KiB | 2.29 KiB |
| 50k | 50,000 | 0 | 8.81 KiB | 2.37 KiB |
| 100k | 100,000 | 0 | 9.47 KiB | 2.37 KiB |
| 200k | 200,000 | 0 | 8.97 KiB | 2.37 KiB |

v1 costs ~11.3 KiB per idle WebSocket connection. That is the number v2's
≤1 KiB (WS) and ≤4 KiB (WSS) hypotheses have to beat, and the reason the
per-connection goroutine/buffer model cannot reach 1M on a 15 GiB host.

## What this does not establish

- **No performance comparison.** The io_uring go/no-go threshold in ADR-0001
  requires an epoll baseline from the same host; the epoll backend is M1 work.
- **No memory-per-connection numbers.** 10k/100k idle soaks and slab accounting
  are M1/M3 work.
- **No 1M density claim.** Nothing here supports or refutes it.
- **One kernel only.** 6.12 LTS and the 7.x target remain untested.
- **No browser or OpenSSL interop run.** The tests use Go clients on both ends.

## Consequence for the architecture

The io_uring backend's idle-memory advantage depends on provided-buffer rings:
without them, each armed receive holds a buffer, which is the model the project
set out to avoid. Since rings are unavailable on the first test kernel, the
io_uring backend must probe at startup and, if rings are missing, either fall
back to classic buffers (accepting a buffer per idle connection) or report
itself unsupported. That decision belongs in M1, with these numbers attached.
