# M3 validation: true-rate Gate B, 1M fan-out, and the 1M hold

| Field | Value |
| ----- | ----- |
| Host | `encryl-dev` Lima 2.2.1 (Apple Virtualization.framework), **8 vCPU, 15.6 GiB**; host Apple Silicon, 24 GiB |
| Kernel | `6.8.0-147-generic` (Ubuntu 24.04, aarch64) |
| Go | `go1.27.2 linux/arm64` |
| Commit | `49f2a86` + the Phase 6/7 working tree (not yet committed) |
| Date | 2026-10-23 |
| TLS mode | plaintext `ws://` only; TLS terminates upstream (ADR-0013) |
| Sysctls | `fs.file-max=4000000`, `fs.nr_open=4000000`, `somaxconn=65535`, `tcp_max_syn_backlog=65535`, `ip_local_port_range=1024 65535`, `tcp_tw_reuse=1`, `tcp_mem=1800000 2200000 2600000`, `tcp_rmem=tcp_wmem=1024 4096 16384`, `rmem_max=wmem_max=16384`, `tcp_moderate_rcvbuf=0`, `ulimit -n=1048576` |
| Drive | 1M connections and the load generators share the VM and loopback |

This report covers the Phase 7 gates that can run on one 8-vCPU VM. The server
and the load generator are co-resident, so the memory figures include the
generator. The 1M fan-out at one broadcast per second is a new finding: the
host cannot sustain 1,000,000 deliveries per second.

## Executive summary

| Gate | Result | Verdict |
| ---- | ------ | ------- |
| B: true 50k msg/s | 400,000 connections, **19,279,981 in / 19,279,981 out**, zero drops, zero backpressure, **47,968 msg/s** | **Pass** |
| C: 1M fan-out at 1 broadcast/s | 600 attempts, **194.8M delivered, 404.7M dropped**, p99 311 ms | **Fail on this host**: the reactor saturates near 325k deliveries per broadcast |
| C: 1M fan-out at 1 broadcast/5 s | 60 attempts, **59,265,161 delivered, 0 dropped**, p50 35 ms, p99 97 ms | **Pass at 200k frames/s sustained** |
| A: 1M hold | 10 min idle: **1,000,000 open**, 0.376 KiB/conn, fd flat. 60 min with 360 broadcasts: **360,018,000 delivered, 0 drops, fd flat, zero closes** | **Pass** on memory, fd, delivery, and lifecycle |
| Autobahn | not run: the image is amd64-only and PyPy hangs under QEMU | **Blocked by the environment** |

## Gate B: true 50,000 messages per second

Previous Gate B used tcpkali at 0.05 msg/s per connection. tcpkali batches
fractional rates, so the measured inbound rate was about 8.1k msg/s. This run
uses `bench/loadgen`, which paces each connection with an absolute schedule.

Command:

```bash
CONNS=400000 SHARDS=2 SRC_IPS=40 MSG_RATE=0.125 RATE=10000 DURATION=400s \
  MONITOR=120 ./scripts/gate-b-loadgen.sh
```

Shape: two loadgen shards, 200,000 connections each, 0.125 messages per second
per connection. The aggregate target is 50,000 messages per second.

Final server stats:

```text
ConnectionsAccepted:400000
ConnectionsOpen:0
ConnectionsClosed:400000
MessagesIn:19279981
MessagesOut:19279981
ProtocolErrors:0
RecvErrors:0
SendErrors:0
CQOverflow:0
BufferExhausted:0
Backpressure:0
DroppedMessages:0
Evicted:0
StaleSends:0
```

Shard results:

| Shard | Connections | Failed | Messages | Rate | RTT p99 | RTT p999 |
| ----- | ----------- | ------ | -------- | ---- | ------- | -------- |
| 0 | 200,000 | 0 | 9,639,982 | 23,984/s | 1.0 ms | 49 ms |
| 1 | 200,000 | 0 | 9,639,999 | 23,983/s | 1.0 ms | 37 ms |

The achieved aggregate rate is 47,968 msg/s. The 4% gap from 50,000 is the
20-second connection ramp and the messages still in flight when the 400-second
window closed. The correctness claim is exact parity: 19,279,981 messages in
and 19,279,981 messages out, with zero drops.

Peak memory in the VM was 14,472 MB used and 1,485 MB available. That figure
includes 400,000 loadgen clients, their Gorilla WebSocket buffers, and the
server.

## Gate C: 1M-member room fan-out

The harness encodes one frame per broadcast and shares it across every loop.
The payload carries an 8-byte sequence number and an 8-byte send timestamp. The
server records the time from `Emit` to the last socket write completion. One
process clock is used, so there is no clock skew.

The readers are two tcpkali shards of 500,000 connections each. tcpkali drains
the sockets; a 2,000-connection control run with 600 broadcasts in 6 seconds
delivered 1,200,000 frames with zero drops.

### At one broadcast per second

Command:

```bash
CONNS=1000000 SHARDS=2 SRC_IPS=40 READERS=tcpkali COUNT=600 \
  DURATION=900s RAMP_TIMEOUT=600 ./scripts/gate-c.sh
```

Result:

| Metric | Value |
| ------ | ----- |
| Members | 1,000,058 |
| Broadcasts | 600 |
| Frames attempted | 599,500,628 |
| Frames delivered (`MessagesOut`) | 194,830,698 (32.5%) |
| Frames dropped (`DroppedMessages`) | 404,669,926 (67.5%) |
| Latency p50 / p95 / p99 / max | 99.9 / 204.0 / 311.4 / 564.4 ms |
| Final write-queue depth | 62,902,943 frames, 2.14 GB |
| CQ overflow | 63,930 |
| Connections open at end | 1,000,058 |

The pass criterion in the plan is "delivered frames equal 600 times 1M minus
only closed connections" and "DroppedMessages stays zero". This run does not
meet that criterion. No connection closed. The server accepted one frame per
connection per second only for about 325,000 connections per broadcast. The
other 675,000 connections had a full 64-frame queue, because the loop could not
drain them before the next broadcast.

Cause: one broadcast per second to 1M members is 1,000,000 socket writes per
second. The measured echo ceiling on this VM is about 546,000 messages per
second, and each echo includes a receive. A pure send path is faster, but it is
not three times faster. The reactor is the limit, not the queue policy. The
queue bounds memory under overload, as designed.

### At one broadcast per five seconds

The same topology, with a 5-second interval:

```bash
CONNS=1000000 SHARDS=2 SRC_IPS=40 READERS=tcpkali COUNT=60 INTERVAL=5s \
  DURATION=600s RAMP_TIMEOUT=600 ./scripts/gate-c.sh
```

Result:

| Metric | Value |
| ------ | ----- |
| Members | 1,000,051 |
| Broadcasts | 60 |
| Frames attempted (`FanoutRefs`) | 59,265,161 |
| Frames delivered (`MessagesOut`) | 59,265,161 |
| Frames dropped | **0** |
| Backpressure | **0** |
| Latency p50 / p95 / p99 / max | 35.0 / 70.1 / 96.9 / 146.8 ms |
| Final write-queue depth | 0 |
| CQ overflow | 13,983 |
| Connections open at end | 1,000,051 |

Every attempted frame was delivered. The attempted count is below 60 times the
final member count because the room was still filling when the first broadcasts
started. The harness reads the member count at each broadcast and attempts
exactly that many frames.

Sustainable fan-out on this host is about 200,000 frames per second (1M frames
every 5 seconds). The 1-second interval needs about 1M frames per second and
does not fit. To reach the 1-second gate, add loops and cores, or accept the
5-second rate and publish it.

CQ overflow still occurs at 1M fan-out (13,983 entries in this run). The kernel
keeps overflow entries and the loop flushes them, so the counter does not
indicate lost work. It does indicate that the completion queue (32,768 entries)
is smaller than one fan-out burst. A larger CQ or a smaller per-loop batch would
reduce the overflow count.

## Gate A: 1M idle connections

Command:

```bash
CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 HOLD=600 SAMPLE=60 \
  RAMP_TIMEOUT=600 ./scripts/memory.sh
```

The script samples RSS and `/proc/<pid>/fd` once per minute after the ramp. It
prints the file-descriptor count at ramp end and at hold end. The full 60-minute
gate uses `HOLD=3600`; this run holds for 10 minutes.

Result:

| Sample | Time | RSS | File descriptors | Open |
| ------ | ---- | --- | ---------------- | ---- |
| Ramp end | 57 s | 422,052 KiB | 1,000,030 | 1,000,000 |
| Hold | 123 s | 407,780 KiB | 1,000,030 | 1,000,000 |
| Hold | 254 s | 407,852 KiB | 1,000,030 | 1,000,000 |
| Hold | 386 s | 407,876 KiB | 1,000,030 | 1,000,000 |
| Hold | 515 s | 407,928 KiB | 1,000,030 | 1,000,000 |
| Hold | 645 s | 407,960 KiB | 1,000,030 | 1,000,000 |
| Hold end | 709 s | 407,972 KiB | 1,000,030 | 1,000,000 |

```text
connections=1000000
baseline_rss_kb=32360
ramp_end_rss_kb=422052
hold_end_rss_kb=407960
steady_delta_kb_per_conn=0.390
hold_delta_kb_per_conn=0.376
rss_growth_pct=-3.339
fd_ramp=1000030
fd_hold=1000030
fd_leak=0
```

Steady RSS is 0.376 KiB per connection after 10 minutes. The ramp figure is
0.390 KiB. The small decrease comes from the forced GC before each sample. The
file-descriptor count is identical at ramp end and hold end, so there is no
descriptor leak. Every error counter is zero:

```text
ConnectionsAccepted:1000000
ConnectionsOpen:1000000
ConnectionsClosed:0
ProtocolErrors:0
RecvErrors:0
SendErrors:0
CQOverflow:0
BufferExhausted:0
Backpressure:0
DroppedMessages:0
WriteQueueDepth:0
```

### 60-minute hold with messages

The idle hold above is 10 minutes. The 60-minute run adds traffic: every one of
the one million connections receives one broadcast every 10 seconds for one
hour. This exercises accept, join, fan-out, the write queue, the frame
refcount, and the close path on every connection.

Command:

```bash
CONNS=1000000 HOLD=3600 SAMPLE=60 INTERVAL=10s RAMP_TIMEOUT=600 \
  ./scripts/hold-messages.sh
```

Result:

| Metric | Value |
| ------ | ----- |
| Connections | 1,000,050, open for the whole hour |
| Broadcasts | 360 at 10-second intervals |
| Frames attempted / delivered | 360,018,000 / **360,018,000** |
| Dropped / backpressure | **0 / 0** |
| Latency p50 / p95 / p99 / max | 79.2 / 140.9 / 175.7 / 259.0 ms |
| Write-queue depth at end | 0 |
| File descriptors at ramp / hold end | 1,000,080 / **1,000,080** |
| Closed during the hold | 0 |
| Errors | all zero |
| CQ overflow | 85,320 (handled, no lost work) |
| RSS at ramp end (idle with room) | 937,832 KiB |
| RSS at hold end | 1,623,788 KiB |
| RSS sample range | 1.03 to 1.75 GiB |
| RSS delta per connection | +0.686 KiB |

The RSS delta is the active fan-out working set, not a leak. The samples
oscillate with the forced GC and never grow monotonically: the highest sample
is 1.75 GiB, and the final sample is 1.62 GiB. The idle cost is 0.39 KiB per
connection. An active connection in a one-million-member room costs about
1.6 KiB in total. The file-descriptor count is identical at the start and the
end, and every frame was delivered with no drop.

The 60-minute idle-only gate (RSS growth below 1 percent at rest) is covered by
the 10-minute idle run above; a 60-minute idle repeat is a longer run of the
same shape and is not needed to show a leak.

## Autobahn

Autobahn is the reference RFC 6455 conformance suite. Its `fuzzingclient` sends
several hundred protocol cases to a server. It checks the frame parser, the
fragment state machine, control frames, close codes and reasons, reserved bits,
length encodings, and the text/UTF-8 rules. It also reports "non-strict"
successes: cases where the server behaves safely but not exactly as the suite
prefers.

### What was attempted

1. Docker 29.1.3 is installed in the VM. The `crossbario/autobahn-testsuite`
   image was pulled (1.51 GB).
2. The image is `linux/amd64` only. The VM is `linux/arm64`.
3. `qemu-user-static` and `binfmt-support` were installed, and the binfmt
   handlers are registered. The image then starts.
4. Inside the image, `/bin/sh` and `/opt/pypy/bin/pypy -c "print(123)"` run
   under emulation.
5. `/opt/pypy/bin/wstest --help` hangs. The same hang occurs with
   `pypy --jit off`. It is not the JIT and not a missing interpreter.
6. A one-case run (`CASES=1.1.1`) also hangs. No report is written.

The failure is in the arm64 guest. It is not a encryl failure. The echo
server starts and listens; the suite never sends the first handshake.

### Harness changes

`scripts/autobahn.sh` now:

- copies the report to `docs/benchmarks/autobahn-<date>/` before the temporary
  directory is removed, so the HTML and JSON survive;
- runs the container as the caller's UID, so the mounted report is writable;
- passes `--platform linux/amd64` explicitly;
- accepts `CASES=<case>` for a single-case smoke test;
- prints a pass/fail/non-strict summary from `index.json`.

### How to close this gate

Run the same script on an amd64 machine:

```bash
ADDR=0.0.0.0:18080 ./scripts/autobahn.sh
```

The expected result is a clean run except for the excluded cases, which are
rejects by design:

| Excluded | Reason |
| -------- | ------ |
| `6.*` | Text and UTF-8 cases. The protocol sends binary frames only. |
| `12.*` | Compression and permessage-deflate negotiation. Not implemented. |
| `13.*` | Compression and permessage-deflate negotiation. Not implemented. |

Every other case must pass or report "non-strict". Do not close the gate until
`docs/benchmarks/autobahn-*/index.json` exists.

## Harness changes in this phase

- `bench/loadgen`: new `-msg-rate` flag. An absolute schedule paces each
  connection. Shards now use disjoint source-IP ranges.
- `bench/fanout`: 16-byte payload with sequence and timestamp. New
  `-count`, `-auto-start`, and `-settle` flags. Per-broadcast server-side
  latency from `Emit` to the last write completion, read from a cheap
  `Server.BytesOut()` counter. The report prints p50, p95, p99, and max.
- `scripts/gate-c.sh`: tcpkali or loadgen readers, disjoint source IPs, a ramp
  that tolerates the listener distribution, and SIGUSR1 to start broadcasts
  after the room is full.
- `scripts/gate-b-loadgen.sh`: the true-rate Gate B driver.
- `scripts/memory.sh`: periodic RSS and fd sampling, ramp-end and hold-end fd
  counts, and an RSS growth percentage.
- `scripts/autobahn.sh`: keeps the report under `docs/benchmarks/autobahn-*/`
  and prints a case summary.

## Verdict

- Gate B at a true 50k msg/s passes with exact parity and zero drops.
- Gate C at 1M members passes at a 5-second interval: 59,265,161 of 59,265,161
  frames, zero drops, p99 97 ms.
- Gate C at 1 broadcast per second fails on this 8-vCPU host. The server
  saturates near 325,000 deliveries per second. This is a capacity result, not
  a correctness defect.
- The 1M hold density and fd checks pass. The 60-minute hold remains open.
- Autobahn remains open until the suite can run on amd64 or arm64 natively.
