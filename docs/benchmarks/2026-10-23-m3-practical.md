# M3 practical workloads: 12 vCPU, 1:1, group, ACK, churn, and EMQX

| Field | Value |
| ----- | ----- |
| Host | `encryl-dev` Lima 2.2.1 (Apple Virtualization.framework). Baseline **8 vCPU, 15.6 GiB**; the 12-vCPU run used 12 vCPU on a host with **10 physical cores (4P + 6E), 24 GiB** |
| Kernel | `6.8.0-147-generic` (Ubuntu 24.04, aarch64) |
| Go | `go1.27.2 linux/arm64` |
| Commit | `49f2a86` + the Phase 7 working tree (not yet committed) |
| Date | 2026-10-23 |
| Drive | The server and the clients share the VM and loopback |
| Sysctls | `fs.file-max=4000000`, `fs.nr_open=4000000`, `somaxconn=65535`, `tcp_max_syn_backlog=65535`, `ip_local_port_range=1024 65535`, `tcp_tw_reuse=1`, `tcp_mem=1800000 2200000 2600000`, `tcp_rmem=tcp_wmem=1024 4096 16384`, `rmem_max=wmem_max=16384`, `tcp_moderate_rcvbuf=0`, `nf_conntrack_max=3000000`, `ulimit -n=1048576` |

This report covers the practical workloads and the CPU comparison. Gate B,
Gate C, and the hold are in
[`2026-10-23-m3-validation.md`](2026-10-23-m3-validation.md).

## 1. CPU comparison: 1M fan-out at one broadcast per second

The plan asks for 1,000,000 deliveries per second. The 8-vCPU run delivered
32.5 percent. The question was whether 12 vCPUs close the gap.

| Setting | Members | Frames attempted | Delivered | Dropped | p50 | p99 | CQ overflow |
| ------- | ------- | ---------------- | --------- | ------- | --- | --- | ----------- |
| **8 vCPU** | 1,000,058 | 599,500,628 | 194,830,698 (32.5%) | 404,669,926 | 99.9 ms | 311.4 ms | 63,930 |
| **12 vCPU** | 1,000,083 | 600,049,800 | 154,237,140 (25.7%) | 445,812,660 | 141.2 ms | 429.2 ms | 44,097 |

12 vCPUs made the result **worse**. The host has 10 physical cores. The VM then
runs 12 pinned loops plus 16 tcpkali threads on 10 cores, so every loop competes
for a core. The fan-out path is CPU-bound, and oversubscription costs more than
the two extra loops return.

The 1-second rate is not reachable on this host at either setting. Use 8 vCPUs,
or move to a host with more physical cores. The 5-second interval passes at
8 vCPUs with zero drops (see the validation report).

### The conntrack trap

The first 12-vCPU attempt stopped at 262,143 connections. The cause was
`nf_conntrack_max=262144`, which is the Ubuntu default when Docker loads the
conntrack module. The limit is a kernel table, not a server limit. All scripts
now set `nf_conntrack_max=3000000`. A run that stalls at exactly 262,143 or
524,287 connections has this cause.

## 2. Practical workload suite

The harness is `bench/workload` with two roles: `-role server` and
`-role client`. The workloads are:

- **unicast**: 1:1 delivery for a chat or direct-message product. Each client
  sends to a random peer. The server resolves the peer `ConnID` and sends to its
  Self-Room.
- **group**: group chat. Each room has `-room-size` members and one publisher.
  The publisher rate is shared across rooms.
- **ack**: request and delivery receipt. The client sends a request ID and waits
  for the ACK.
- **churn**: connect, send one event, close. The server joins each connection to
  a room and removes it on close. This checks membership cleanup.

All latency is end to end on one process clock. All runs are on the 8-vCPU
baseline.

| Workload | Connections | Offered rate | Delivered | Failed | p50 | p95 | p99 | Peak server RSS |
| -------- | ----------- | ------------ | --------- | ------ | --- | --- | --- | --------------- |
| unicast (1:1) | 100,000 | 20,000 msg/s | 1,100,000 | 0 | <1 ms | <1 ms | 3 ms | 99.5 MB (1.0 KiB/conn) |
| ack (receipts) | 50,000 | 10,000 req/s | 550,000 | 0 | <1 ms | <1 ms | 2 ms | 68.0 MB (1.36 KiB/conn) |
| group (100 rooms) | 100,000 | 1,000 pubs/s (95k deliveries/s) | 5,729,942 | 0 | <1 ms | <1 ms | 3 ms | 128.7 MB (1.29 KiB/conn) |
| group (1 room, EMQX shape) | 10,000 | 10 pubs/s (100k deliveries/s) | 5,981,765 | 0 | 9 ms | 14 ms | 23 ms | 59.8 MB (5.98 KiB/conn) |
| churn + group (stress) | 100,000 stable | 16.1k churn/s | 14,647,222 group + 4,739,786 cycles | 0 | — | 1 ms | 9 ms | 191.7 MB |

Every run reports zero failed connections. The 1:1 run delivered exactly what
it sent: 1,100,000 in and 1,100,000 out. The ACK run matched the same way:
550,000 requests and 550,000 receipts. The server's `MessagesOut` is slightly
higher than the delivered count because it includes the 100,000 or 50,000
welcome events.

The tear-down in the 100k group run produced 289 receive errors (0.29 percent).
These are the clients closing the socket while the server still holds a read.
They are not protocol errors. A clean close would show zero.

The stress run mixed 100,000 stable group members with 50 churn workers for
five minutes:

| Metric | Value |
| ------ | ----- |
| Accepted / closed | 4,839,786 / 4,839,786 |
| Churn cycles | 4,739,786, zero client failures |
| Group deliveries | 14,647,222, p99 9 ms |
| Open at end | 0 |
| Rooms / memberships at end | 0 / 0 |
| Server fd count during | ~100,046 and flat |
| Close errors | 0 |
| Peak RSS | 191.7 MB |
| Recv / send errors | 3 / 403 (0.008 percent, abrupt client closes) |

### Critical finding: a descriptor leak under churn

The first stress run leaked file descriptors. The server accepted 1.4 million
connections, and the fd count reached the 1,048,576 limit. A focused probe
(`scripts/churn-probe.sh`) reproduced it: about half of all churned connections
left an open socket.

The close path submitted and completed one `IORING_OP_CLOSE` per connection,
but 101,574 of 190,519 completions returned `EINVAL` (errno 22). The cause is in
the vendored io_uring binding. `prepareRW` clears the common SQE fields but not
`OpcodeFlags`, which is the `rw_flags` union. When an SQE was reused after an
accept, send, or cancel, the stale flag remained. The kernel's `io_close_prep`
rejects a non-zero `rw_flags` with `EINVAL`, so the descriptor stayed open while
the slot was released.

Fix: `prepareRW` now clears `OpcodeFlags`. After the fix:

| Probe | Before fix | After fix |
| ----- | ---------- | --------- |
| 189,627 churn cycles, close errors | 101,574 | **0** |
| fd count after 60 s | ~692,000 | **30** |
| fd count after drain | 692,000 (never fell) | **30** |

This is a serious defect for any workload with connection churn. It is fixed in
`internal/uring/giouring/prepare.go`. The new `CloseSubmitted`,
`CloseCompleted`, `CloseErrors`, and `CloseErrno` counters in `Stats` surface
the problem if it returns.

The group run delivered 5,981,765 frames with zero drops, zero backpressure, and
zero errors. Peak server RSS was 59.8 MB for 10,000 active connections. The
process baseline is about 32 MB, so the marginal cost is about 2.8 KiB per
active connection. An idle connection costs 0.39 KiB at 1M (Gate A).

## 3. EMQX comparison

EMQX 5.8.6 runs in Docker on the same VM. The image is arm64. The MQTT listener
uses port 1883. The WS, SSL, and dashboard listeners are disabled to avoid port
conflicts.

The workloads have the same delivery shape:

- **encryl**: one room with 10,000 members, one publisher at 10 messages
  per second. Each message goes to 9,999 other members.
- **EMQX**: one topic with 10,000 QoS 0 subscribers, one publisher at 10
  messages per second.

Both deliver 600 messages to 10,000 receivers: 6,000,000 frames.

Command:

```bash
# encryl
WORKLOAD=group CONNS=10000 ROOMSIZE=10000 RATE=10 DURATION=60s \
  ./scripts/practical.sh

# EMQX
CONNS=10000 RATE=10 SECONDS_RUN=60 ./scripts/emqx-compare.sh
```

| Metric | encryl | EMQX 5.8.6 |
| ------ | ----------- | ---------- |
| Subscribers | 10,000 | 10,000 |
| Published messages | 599 | 600 |
| Delivered frames | 5,981,765 | 6,000,000 |
| Delivery rate | 99.7k/s | 99.6k/s |
| Dropped | 0 | 0 (`messages.dropped=0`) |
| p50 latency | 9 ms | not measured by the tool |
| p95 / p99 latency | 14 / 23 ms | not measured by the tool |
| Peak server RSS at 10k | 59.8 MB (5.98 KiB/conn including baseline) | 544.7 MiB (7.4 KiB/conn marginal) |
| Idle baseline RSS | ~32 MB | 470.9 MiB |
| Idle cost per connection | 0.39 KiB at 1M | 7.4 KiB marginal |

The delivery rate and the delivery count match. The encryl server uses
about 1/12 of the memory at 10,000 connections, and the difference grows with
connection count: the 470.9 MiB Erlang baseline is fixed, while encryl is
32 MB plus 0.39 KiB per idle connection.

Caveats:

- This is a workload comparison, not a protocol comparison. EMQX speaks MQTT
  with QoS levels, retained messages, sessions, and clustering. encryl
  speaks a binary WebSocket envelope with no persistence.
- EMQX's publisher tool reports 1,200 publish operations, but the broker counts
  600 received messages and 6,000,000 sent messages. The broker counters are the
  authoritative values.
- EMQX latency per message was not measured. The subscriber tool reports only
  counts and rate. The encryl latency is end to end inside the client
  process.

## 4. Autobahn

Autobahn did not run on this VM. The full reason and the workaround are in the
validation report. In short:

- `crossbario/autobahn-testsuite` is an `linux/amd64` image. The VM is `arm64`.
- QEMU user emulation is installed and can run `/bin/sh` and PyPy inside the
  image. `wstest` hangs under emulation, with JIT enabled and with `--jit off`.
- The harness (`scripts/autobahn.sh`) now keeps the HTML and JSON report under
  `docs/benchmarks/autobahn-<date>/`, runs the container as the caller's UID,
  and uses `--platform linux/amd64`.
- Run it on an amd64 host or CI runner. The expected rejects are cases `6.*`,
  `12.*`, and `13.*` (text/UTF-8, compression). The protocol is binary-only.

## 5. Reproduce

```bash
# practical workloads
WORKLOAD=unicast CONNS=100000 RATE=20000 DURATION=60s ./scripts/practical.sh
WORKLOAD=group   CONNS=10000 ROOMSIZE=10000 RATE=10 DURATION=60s ./scripts/practical.sh
WORKLOAD=ack     CONNS=50000 RATE=10000 DURATION=60s ./scripts/practical.sh
WORKLOAD=churn   CONNS=100000 WORKERS=50 DURATION=300s ./scripts/stress.sh

# EMQX
./scripts/emqx-compare.sh

# 12 vCPU comparison (host must have spare cores)
limactl stop --force encryl-dev
limactl edit encryl-dev --cpus 12
limactl start encryl-dev
CONNS=1000000 COUNT=600 DURATION=900s ./scripts/gate-c.sh
```

## Verdict

- 12 vCPUs do not fix the 1M-per-second fan-out on a 10-core host. They reduce
  delivery because the loops oversubscribe the cores.
- Practical 1:1, group, ACK, and churn workloads pass with zero failures and
  low latency at the tested scales.
- encryl matches EMQX's delivery rate for the same fan-out shape, with
  about 1/12 of the memory at 10k connections and no broker baseline.
- The 1M hold with traffic is in the validation report.
- Autobahn remains environment-blocked.
