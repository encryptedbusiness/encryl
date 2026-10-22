# Benchmarks

Every number in this directory is reproducible from a command in the tree. If a
claim has no command, host, kernel, and commit, it does not belong here.

## A result is not a result without these

Every report starts with this table:

| Field | Value |
| ----- | ----- |
| Host | `hostname`, CPU model, core count |
| Kernel | `uname -r` |
| Memory | `MemTotal` |
| Commit | `git rev-parse --short HEAD` |
| Date | ISO date |
| Command | exact invocation, including flags |
| Raw | path under `raw/` |
| Sysctls | `somaxconn`, `tcp_max_syn_backlog`, `ip_local_port_range`, `ulimit -n` |
| TLS mode | `userspace`, `tx`, or `txrx`, and the offload rate |
| Go | `go version` |

The preflight output from `make preflight` is pasted verbatim.

## Workloads

| Name | Command | What it measures |
| ---- | ------- | ---------------- |
| idle | `loadgen -mode=idle -conns N` | density, memory per connection, CPU at rest |
| connect storm | `loadgen -mode=idle -rate R -duration D` | accept path, handshake throughput |
| echo | `loadgen -mode=echo -msg-size B` | round-trip latency, message throughput, allocations |
| fan-out | `loadgen -mode=read` with a server-side publisher | delivery latency per recipient, encode-once |
| 1:1 unicast | `practical.sh WORKLOAD=unicast` | direct delivery, p99, parity |
| group chat | `practical.sh WORKLOAD=group` | room fan-out, p99, delivery parity |
| ACK receipts | `practical.sh WORKLOAD=ack` | request/receipt round trip |
| churn | `stress.sh` or `churn-probe.sh` | close path, membership cleanup, fd stability |
| slow consumer | one client that stops reading | backpressure policy and memory bound |

Current v1 results use the `idle` workload only; see
`2026-10-22-v1-scaling.md`. Message workloads need a v2 server and are M1+.

The protobuf protocol report is `2026-10-23-protobuf-protocol.md`: codec
microbenchmarks against `centrifugal/protocol` on the same VM, the allocation-
free protobuf echo path, and the decoder fuzz run. The protocol is opt-in and
not wire-compatible with Centrifuge.

The named-event report is `2026-10-23-named-events.md`: event-name hash cost,
the named-vs-numeric echo comparison, and the zero-allocation string path.

The attached-mode report is `2026-10-24-fd-handoff.md`: the net/http, fasthttp,
and Fiber handoff tests, the microbenchmark comparison, and a shortened
6-minute 1M hold (the 60-minute gate remains open).

The combined HTTP-handler + WebSocket-upgrade report is
`2026-10-25-ws-upgrade-1c2g.md`: 100k–500k upgrade ramps, memory per open
socket, API impact, and the net/http-free hijack path. The slow-client report
is `2026-10-25-slow-clients-1c2g.md`: HTTP header/idle/write timeouts, output
backpressure, raw-HTTP connection caps, and 10k non-reading WebSocket clients.

The fixed-file report is `2026-10-25-fixed-files.md`: 500,000 idle sockets with
28 process file descriptors, the close path, and the fixed-rate message and
allocation gates. Its follow-up records the idle-handle elision and the strict
`0.4 KiB/connection` gate result.

M1 has two reports: `2026-10-23-m1-reactor.md` (hot path and 10k idle) and
`2026-10-23-m1-scale.md` (1M idle with tcpkali, message-path findings, and the
one-in-flight send limit). The vertical-slice change supersedes the message
finding: `2026-10-23-m2-vertical-slices.md` records exact 1M in/out parity,
zero write-queue drops, 0.392 KiB/connection, and 494k msg/s at 5k connections.
The `SendBusy` counter was retired in Phase 6; the M2 report keeps the values
from the build that measured them.

The M2 harness additions are `scripts/memory.sh` (ramp, wait for the server's
own `ConnectionsOpen`, force GC, sample RSS) and `bench/fanout` (one room,
periodic broadcast; pair it with `loadgen -mode=read`).

The M3 reports are `2026-10-23-m3-validation.md` (true-rate Gate B, 1M fan-out,
hold) and `2026-10-23-m3-practical.md` (12-vCPU comparison, 1:1, group, ACK,
churn, and the EMQX comparison). The M3 harness additions are:

| Harness | Purpose |
| ------- | ------- |
| `bench/loadgen -msg-rate` | per-connection message pacing for a true aggregate rate |
| `bench/fanout` | sequence and timestamp payload, server-side p99 from Emit to the last write completion |
| `bench/workload` | server and client roles for unicast, group, ACK, and churn |
| `scripts/practical.sh` | one practical workload, with peak server RSS |
| `scripts/stress.sh` | stable group traffic plus continuous connection churn |
| `scripts/hold-messages.sh` | 1M-member hold with a broadcast every interval, RSS and fd samples |
| `scripts/gate-b-loadgen.sh` | true-rate Gate B driver |
| `scripts/gate-c.sh` | 1M fan-out with tcpkali readers and SIGUSR1 start |
| `scripts/emqx-compare.sh` | EMQX 5.8.6 workload comparison |

Rules:

- Run at least 3 repetitions and report the spread, not the best run.
- Report p50, p95, p99, p99.9 — never only the mean.
- Pin the generator and server to different cores.
- For >100k connections, run several loadgen processes with `-shard/-shards`
  and `-src-ips`; a single generator becomes the bottleneck first.
- State whether swap was enabled (`swapoff -a` for density runs).

## The load generator

`bench/loadgen` is the supported request/response and message generator for
numbers in this directory. It was written to replace the v1 driver, which had
data races, no percentiles, and no server in the tree, and then fixed three
more times during M0 verification (see `docs/m0-execution-log.md`).

For **idle density above 100k**, `scripts/scale-sharded.sh` uses `tcpkali`
instead: the Go generator's goroutine-per-connection model needs ~10 KiB per
client connection, so it becomes the memory bottleneck before the server does.
tcpkali is a density generator, not a request/response client; do not use its
fixed-rate `-r` mode to claim message correctness. See
`2026-10-23-m1-scale.md` for the 1M methodology and the M1 one-slot send
finding.

| Flag | Meaning |
| ---- | ------- |
| `-addr`, `-path`, `-scheme` | target, e.g. `127.0.0.1:8080`, `/ws`, `ws` or `wss` |
| `-conns` | total connections for this process/shard |
| `-rate` | ramp rate per second; `0` means as fast as possible |
| `-mode` | `idle`, `echo`, or `read` |
| `-msg-size` | payload size for `echo` |
| `-duration` | run time; the context is cancelled at the deadline |
| `-src-ips` | spread clients over `127.0.0.2..N` |
| `-shard`, `-shards` | split one large run across processes |
| `-report` | write the summary to a CSV |
| `-insecure` | skip TLS verification |
| `-ping` | interval for `idle` mode |
| `-timeout` | dial timeout |

Behaviour that matters when interpreting results:

- **One goroutine per connection.** This mirrors a browser client and keeps the
  generator from being a hidden bottleneck, but it means the generator itself
  needs memory: budget roughly a goroutine stack plus buffers per connection.
- **The dial semaphore bounds concurrent dials, not live connections.** It is
  released as soon as the dial returns.
- **Pacing is absolute.** The generator accumulates a target schedule and only
  sleeps when it is ahead; per-iteration sleeps would cap the ramp at one
  connection per timer tick (~1000/s).
- **The deadline cancels everything.** A completed run returns instead of
  waiting forever on a context that is never cancelled.
- **Latency is a 1 ms-resolution histogram** up to 60 s, reported as
  p50/p95/p99/p99.9/max with the sample count.
- **`echo` speaks raw WebSocket frames, not the v1 JSON protocol.** Against
  v1 it will connect and then time out waiting for a reply. v1 numbers use
  `idle` only; message-level comparison needs the M2 adapter.

High-connection recipe:

```bash
ulimit -n 1048576
# one process, 200k connections over 16 source addresses
./bench/loadgen -addr=127.0.0.1:8080 -path=/ws -mode=idle \
  -conns=200000 -rate=60000 -duration=12s -src-ips=16 \
  -report=docs/benchmarks/raw/200k.csv
# or split across processes
for s in 0 1 2 3; do
  ./bench/loadgen -conns=250000 -shard=$s -shards=4 -src-ips=16 ... &
done
```

Idle density with tcpkali (the M1 1M run):

```bash
CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 MONITOR=300 DURATION=420s \
  ./scripts/scale-sharded.sh
```

## Memory accounting

Report the components separately. A single "RSS per connection" number hides
where the memory went.

```bash
# Process
awk '/VmRSS/ {print $2}' /proc/$PID/status

# Kernel sockets
grep -E '^TCP:' /proc/net/sockstat

# Slabs
grep -E '^(TCP|tls_context|kmalloc-)' /proc/slabinfo

# Go
go tool pprof -top -sample_index=inuse_space http://127.0.0.1:6060/debug/pprof/heap
```

## Latency

`bench/loadgen` records a 1 ms-resolution histogram up to 60 s. For microbenchmarks
use `testing.B` and compare with `benchstat`:

```bash
go test -run=NONE -bench=. -benchmem -count=6 ./... > new.txt
benchstat baseline.txt new.txt
```

The CI regression gate is 5% on hot-path microbenchmarks. End-to-end SLOs are
set only after M1 establishes a measured baseline.

## Comparability

**v1 and v2 message throughput are not directly comparable.** v1 speaks a JSON
event envelope; v2 speaks a binary envelope. Comparing them without an adapter
measures the codecs, not the servers. Connection density, accept rate, and RSS
*are* comparable and are what the v1 baseline reports.

**kTLS numbers must state the mode.** `userspace`, `tx`, and `txrx` are
different systems. A number without the mode is meaningless.

**io_uring numbers must state the epoll baseline from the same host and run.**
The spike's go/no-go threshold is pre-registered in `docs/adr/0001-io-backend.md`
so the result cannot be rationalized after the fact.

## File naming

```
YYYY-MM-DD-<subject>.md      report
raw/YYYY-MM-DD-<subject>.csv raw counters
```

Examples: `2026-10-22-v1-baseline.md`, `2026-11-03-spike-uring.md`,
`2026-11-05-spike-ktls.md`.

## What not to do

- Do not publish a single run.
- Do not compare across kernels, CPU models, or sysctl sets.
- Do not quote a "connections per server" number without the memory budget
  that made it possible.
- Do not claim 1M WSS before `docs/roadmap.md` M3's gate passes.
- M2 has measured **1M idle plaintext `ws://`** connections and **exact in/out
  parity at the 1M echo shape**. M3 published a **60-minute 1M hold with
  traffic** in `2026-10-23-m3-validation.md`. Neither is 1M WSS.
- Do not quote the 1M message rate as 50k msg/s. tcpkali's fractional
  per-connection rate batched sends to about 8.1k msg/s. The correctness claim
  is zero write-queue drops and exact parity.


The 2-GiB room report is `2026-10-25-2gb-rooms.md`: 500k live connections across
5000 rooms, tcpkali density, one CPU, 437 MiB server RSS, and p99.99 fan-out
latency. The earlier 1-GiB report remains as the OOM-boundary record.


For the consolidated result tables, see [`../../BENCHMARKS.md`](../../BENCHMARKS.md).

For a simple-English description of all benchmark groups and checks, see
[`../benchmark-overview-ste.md`](../benchmark-overview-ste.md).
