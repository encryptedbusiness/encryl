# Development environment

## Base environment (current)

| Field | Value |
| ----- | ----- |
| Host OS | macOS 26.5.1 (Darwin 25.5.0), Apple Silicon |
| Virtualization | Lima 2.2.1, `vmType: vz` |
| Guest | Ubuntu 24.04 LTS, kernel 6.8.0-147-generic |
| Guest resources | 8 vCPU, 15.6 GiB usable, 40 GiB disk |
| Go | go1.27.2 linux/arm64 |
| Verified commits | `0e723ab..bdfbdfe` on `v2` |
| Log | [`docs/m0-execution-log.md`](m0-execution-log.md) |

NOTE: The M0 log was recorded on a 4 vCPU / 7.7 GiB guest. The current
baseline and `deploy/lima.yaml` use 8 vCPU / 16 GiB. A 12-vCPU run was tested
on the 10-core host; it oversubscribes the cores and is slower for CPU-bound
fan-out. The benchmark reports record the exact guest for each run.

## What runs where

The runtime is Linux-only and io_uring-only. macOS is a build and portable-test
host, not a runtime target.

| Work | macOS | Linux 6.6+ |
| ---- | ----- | ---------- |
| Config, public API, docs, protocol codecs | build + portable tests | full |
| Parser, envelope, room directory, slab, timer wheel | build + portable tests | full |
| io_uring reactor, accept, receive, send, shutdown | build only | full |
| `go test -race`, `checkptr`, fuzz | portable packages | full |
| io_uring spike | build only | run |
| kTLS spike (historical) | build only | run on request |
| v1 baseline | **does not compile** | run |
| Density and latency benchmarks | no | run |

v1 does not build on macOS at all: `epoll.go` uses `syscall.EpollEvent`, which
only exists on Linux. v2 does not run there either; non-Linux builds compile
against stubs, and `New` panics with a message that names the host.

## macOS (day-to-day)

Requirements: Go 1.26 or newer (the repository pins toolchain 1.27.2 in
`go.mod`; `golang.org/x/sys` requires 1.26), optionally `golangci-lint` v1.62.

```bash
go build ./...
go vet $(go list ./... | grep -v uring/giouring)
go test ./...          # Linux-only server tests skip
go test -race ./...
make docs-check
```

The two spike packages compile on macOS as empty packages, so `go build ./...`
stays meaningful. Their Linux-only code is behind `//go:build linux`.

## Linux target

### Option A — Lima VM (default)

```bash
brew install lima
limactl start --name=encryl-dev --tty=false deploy/lima.yaml   # first time
limactl start encryl-dev                                       # later sessions
make linux-shell                                              # shell in the guest
make preflight                                                # probe table
limactl stop encryl-dev                                        # when finished
```

The VM is stopped after a verification session and keeps its disk;
`limactl start encryl-dev` reuses the provisioned image. `make limactl-up` is the
one-line form of the first command.

Two ordering details that cost time when missed:

- `limactl` wants flags **before** the instance name:
  `limactl shell --workdir "$PWD" encryl-dev <command>`.
- The repository home directory is mounted at the same absolute path in the
  guest, so `--workdir "$PWD"` works without copying anything.

`deploy/lima.yaml` provisions Ubuntu 24.04 with:

- kernel 6.8 (above the 6.6 floor)
- Go 1.27.2
- connection-density sysctls from `docs/operations.md`
- `nofile` raised to 1048576

`scripts/linux-run.sh` wraps both this and the SSH path, so every `make`
target uses the same entry point.

### Option B — an existing Linux host

```bash
export ENCRYL_LINUX_HOST=user@host
make preflight
make test-uring
```

The host must have the repository at the same absolute path, Go installed, and
a kernel 6.6 or newer with io_uring enabled. Secure Boot kernels and some
container runtimes disable io_uring.

## Why not Docker or a hosted CI runner

| Environment | Problem |
| ----------- | ------- |
| Docker default seccomp | Denies `io_uring_setup`; the kernel attack surface is not filterable, so runtimes block it |
| gVisor / sandboxes | No io_uring support |
| GitHub-hosted runners | Kernel and sandbox vary; io_uring is commonly unavailable |
| Any kernel < 6.6 | Below the documented floor; unsupported |

If you must use a container, run it with a VM-backed runtime (Lima, Colima with
VZ, OrbStack with a VM), not a syscall-interception runtime. The Autobahn
client may run in Docker; the server under test must run on the io_uring host.

## Verifying the prerequisite

```bash
# Prints kernel version, opcode support, poll support, and restrictions.
go test -count=1 ./uring
```

The production reactor has its own probe and refuses to start if a required
opcode or flag is missing.

## Benchmarks

```bash
make bench-rooms CONNS=10000
make soak CONNS=10000 DURATION=60m
make bench-baseline CONNS=200000 DURATION=12s RATE=60000 SRC_IPS=16
```

`bench-baseline` checks out v1.0.2 into a temporary worktree, runs its example
server, and samples RSS, sockstat, and TCP slab **during** the soak. It writes
`docs/benchmarks/<date>-v1-baseline.md` and a raw CSV. It must run on Linux; the
Makefile dispatches through `scripts/linux-run.sh`.

Notes that matter for large runs:

- `SRC_IPS=N` spreads clients over `127.0.0.2..N` to avoid ephemeral-port
  exhaustion. 200k needs at least 16.
- `RATE` is the ramp, not a cap. The generator uses an absolute schedule, so
  rates above 1000/s actually take effect.
- The script raises `ulimit -n`; SSH sessions default to a soft limit of 1024.
- Kernel memory is sampled while connections are live. Sampling after teardown
  measures the wrong state.

## Troubleshooting

Kernel and tooling failures seen during M0 verification, with their real causes:

| Symptom | Cause | Fix |
| ------- | ----- | --- |
| `link: invalid reference to syscall.munmap` | Using upstream giouring instead of the vendored copy | Import `uring/giouring` |
| `undefined: syscall.EpollEvent` | Building v1 on macOS | Use the Linux target; v1 is Linux-only |
| `operation not permitted` on ring setup | seccomp blocks io_uring | Use a VM, not a container |
| `RegisterRestrictions: file descriptor in bad state` (`EBADFD`) | Restrictions require a disabled ring | Create with `IORING_SETUP_R_DISABLED`, restrict, then `RegisterEnableRings` |
| `SetupBufRing: permission denied` (`EPERM`) | Restrictions gate register opcodes too | Add the register op to the restriction allowlist |
| `submit: file exists` (`EEXIST`) | `SINGLE_ISSUER` ring used from a different OS thread | Create and drive the ring in the same locked thread |
| `SetupBufRing: invalid argument` (`EINVAL`) | `IORING_REGISTER_PBUF_RING` unsupported on this kernel/arch | Expected on 6.8/arm64; the baseline uses poll + classic buffers |
| Runs stop at 4096 connections | Loadgen dial semaphore held for the connection's lifetime | Fixed; update to the current `bench/loadgen` |
| Ramp never exceeds ~1000 conns/s | Per-iteration `time.After` granularity | Fixed; the generator uses an absolute schedule |
| connect storm exhausts ports | Ephemeral range too small | Use `SRC_IPS`/`-src-ips` and the sysctls from operations |
| `too many open files` | `nofile` limit | `ulimit -n 1048576`; see `deploy/lima-bootstrap.sh` |

Historical kTLS rows were removed with ADR-0013. The M0 execution log still
records them.
