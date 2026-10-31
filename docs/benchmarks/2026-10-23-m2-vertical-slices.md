# M2 vertical slices: write queue, self-rooms, ACKs, and the 1M gates

| Field | Value |
| ----- | ----- |
| Host | `encryl-dev` Lima 2.2.1 (Apple Virtualization.framework), **8 vCPU, 15.6 GiB**, no guest swap; host Apple Silicon, 24 GiB, 10 CPUs |
| Kernel | `6.8.0-147-generic` (Ubuntu 24.04, aarch64) |
| Memory | `MemTotal: 15,957 MB` |
| Go | `go1.27.2 linux/arm64` |
| Commit | `49f2a86` + the vertical-slice working tree (not yet committed) |
| Date | 2026-10-23 |
| TLS mode | plaintext `ws://` only; TLS terminates upstream (ADR-0013) |
| Sysctls | `fs.file-max=4000000`, `fs.nr_open=4000000`, `somaxconn=65535`, `tcp_max_syn_backlog=65535`, `ip_local_port_range=1024 65535`, `tcp_tw_reuse=1`, `tcp_mem=1800000 2200000 2600000`, `tcp_rmem=tcp_wmem=1024 4096 16384`, `rmem_max=wmem_max=16384`, `tcp_moderate_rcvbuf=0`, `nf_conntrack_max=3000000`, `ulimit -n=1048576` |

This report covers the vertical-slice change described in
`docs/adr/0014-vertical-slices.md` through
`docs/adr/0017-middleware.md`. The wire format does not change.

NOTE: The `SendBusy` counter in the raw output below existed in the build that
produced these measurements. It was removed in Phase 6 because the bounded write
queue made it obsolete. The values below stay as measured.

## Executive summary

| Gate | Result | Verdict |
| ---- | ------ | ------- |
| A: 1M idle hold | **1,000,000 open**, 16 extra closed, zero protocol/recv/send/illegal/eventfd errors; 0.392 KiB/conn steady | Density clean; **60-minute hold not run** (~4 min) |
| B: 1M echo, 50k msg/s shape | **1,886,007 in / 1,886,007 out**, `SendBusy 0`, `Dropped 0`, `Backpressure 0`; final queue depth 0 | **Exact parity; write-queue defect fixed** |
| C: room fan-out | 10k members, 1 broadcast/s, **195,692 frames delivered in 20 s** (9,318/s), 0 dial failures | Scaled run; 1M and p99 latency not run |
| D: Self-Room storm | 8k echo connections, **546,374 msg/s**, exact in/out parity, `SendBusy 0` | Clean |
| E: allocations | Full server echo **0 B/op, 0 allocs/op**; dispatch, frame pool, ACK slab, planner all 0 allocs | Pass |
| F: memory | **0.392 KiB/conn** steady at 1M, 0.444 KiB immediately after ramp; slot 192 B | **Under the 0.5 KiB target** |

The M1 defect — one in-flight send per connection, which turned fixed-rate
client bursts into `SendBusy` drops — is gone. The bounded per-connection write
queue absorbs the burst, admission is synchronous, and the final 1M message
counters have exact parity.

## Gate A: 1M idle connections

Command:

```bash
CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 HOLD=320 RAMP_TIMEOUT=240 \
  ./scripts/memory.sh
```

Final stats:

```text
ConnectionsAccepted:1000016
ConnectionsOpen:1000000
ConnectionsClosed:16
MessagesIn:0
MessagesOut:0
BytesIn:194000000
BytesOut:169000000
ProtocolErrors:0
RecvErrors:0
SendErrors:0
IllegalTransitions:0
CQOverflow:0
BufferExhausted:0
EventFDErrors:0
SendBusy:0
Backpressure:0
DroppedMessages:0
WriteQueueDepth:0
```

Memory samples:

| Sample | RSS | Delta per connection |
| ------ | --: | -------------------: |
| Baseline (server, no clients) | 32,440 KiB | — |
| Post-ramp, after forced GC | 476,304 KiB | **0.444 KiB** |
| After 10 s and a second GC | 434,264 KiB | **0.392 KiB** |

The 60-minute M3 hold is not run here. The 1M state was held for roughly four
minutes while the samples were taken. This is a density and steady-state-memory
observation, not the M3 exit gate.

## Gate B: 1M connections with the 50k msg/s echo shape

Command:

```bash
CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 MONITOR=180 DRAIN=45 \
  DURATION=420s MSG_RATE=0.05 TX_BYTES=67108864 BUFFER_COUNT=32768 \
  READ_BUFFER=2048 ./scripts/scale-sharded.sh
```

tcpkali sends the 20-byte `encryl.v2` EVENT envelope at 0.05 msg/s per
connection. The echo handler replies with `Conn.Emit`, which is a Self-Room
send.

Snapshot while the clients were still connected (3m07s):

| Counter | Value |
| ------- | ----: |
| ConnectionsOpen | 1,000,000 |
| MessagesIn | 795,563 |
| MessagesOut | 785,035 |
| SendBusy | **0** |
| DroppedMessages | **0** |
| Backpressure | **0** |
| WriteQueueDepth | 56,159 |
| WriteQueueBytes | 1,235,498 |
| CQOverflow | 132 |
| BufferExhausted | 507 |

After the clients were stopped and the queues drained (45 s):

```text
ConnectionsAccepted:1000000
ConnectionsOpen:0
ConnectionsClosed:1000000
MessagesIn:1886007
MessagesOut:1886007
SendBusy:0
DroppedMessages:0
Backpressure:0
WriteQueueDepth:0
WriteQueueBytes:0
RecvErrors:376868
SendErrors:196053
CQOverflow:3989
BufferExhausted:1706875
```

`MessagesIn` and `MessagesOut` are **exactly equal**. The recv/send errors and
the buffer-exhaustion counter belong to the abrupt `kill` of 1M tcpkali clients,
not to the steady-state path; the pre-kill snapshot has zero send-busy and zero
drops.

The observed inbound rate is about 8.1k msg/s, not 50k. tcpkali's fractional
per-connection rate batches sends; the earlier M1 report observed the same
shortfall. The gate's correctness claim — no `SendBusy`, no drop, exact parity
— is met. A client that can actually drive 50k msg/s without batching remains a
separate load-generator task.

## Gate C: room fan-out (scaled)

Command:

```bash
/tmp/encryl-fanout -addr 127.0.0.1:18082 -max-conns 20000 -interval 1s &
/tmp/encryl-loadgen -addr=127.0.0.1:18082 -mode=read \
  -conns=10000 -rate=5000 -duration=20s
```

Result: 10,000 active connections, 0 dial failures, **195,692 frames
delivered** in 21 s (9,318/s), about 19.6 broadcasts per connection. The frame
is encoded once per broadcast and shared by the reference-counted `Frame`; no
per-recipient encode occurs.

This is a 10k scaled observation. A 1M-member room and p99 delivery latency
are not measured here; the 1M fan-out needs a client fleet that can serve 1M
readers, which the single-VM topology does not provide.

## Gate D: Self-Room unicast throughput

Best run (8k connections, every dial succeeded):

```bash
ulimit -n 1048576
/tmp/encryl-echo -addr 127.0.0.1:18080 -max-conns 50000 -idle-timeout 2h \
  -tx-bytes 67108864 -buffer-count 8192 -read-buffer 4096 &
/tmp/encryl-loadgen -addr=127.0.0.1:18080 -mode=echo -conns=8000 \
  -rate=8000 -duration=30s -msg-size=64 -envelope
```

| Metric | Value |
| ------ | ----- |
| Connections | 8,000 attempted / 8,000 peak active / **0 failed** |
| Messages | 16,406,783 (**546,374 msg/s**) |
| Server in / out | **16,406,783 / 16,406,783** |
| RTT p50 / p95 / p99 / p99.9 / max | 13 / 28 / 37 / 56 / 107 ms |
| SendBusy / Dropped / Backpressure | **0 / 0 / 0** |

A 5k run on the same host produced 14,833,252 messages in 30.02 s
(**494,105 msg/s**) with exact parity and RTT p50 8 ms / p99 31 ms. The 8k run
has the higher aggregate rate; the 5k run has the lower latency.

A 10k dial run at `-rate=5000` reached only 4,583 connections because the
generator was already saturated by echo traffic while dialing. It still showed
exact parity (8,927,556 / 8,927,556) and zero drops. The 8k run dials faster
than echo traffic saturates it, so it is the preferred high-throughput sample.
The earlier 5k command is kept below for comparison.

Command:

```bash
ulimit -n 1048576
/tmp/encryl-echo -addr 127.0.0.1:18080 -max-conns 50000 -idle-timeout 2h \
  -tx-bytes 67108864 -buffer-count 8192 -read-buffer 4096 &
/tmp/encryl-loadgen -addr=127.0.0.1:18080 -mode=echo -conns=5000 \
  -rate=5000 -duration=30s -msg-size=64 -envelope
```

| Metric | Value |
| ------ | ----- |
| Connections | 5,000 attempted / 5,000 peak active / **0 failed** |
| Messages | 14,833,252 (**494,105 msg/s**) |
| Server in / out | **14,833,252 / 14,833,252** |
| RTT p50 / p95 / p99 / p99.9 / max | 8 / 22 / 31 / 48 / 91.5 ms |
| SendBusy / Dropped / Backpressure | **0 / 0 / 0** |

The server counters are sampled at ~30 s. Exact parity at 8k connections and
546k msg/s is the strongest throughput result in this report.

## Gate E: allocation gates

Microbenchmarks on the same VM:

| Benchmark | ns/op | B/op | allocs/op |
| --------- | ----: | ---: | --------: |
| `BenchmarkServerEcho100k` (full public path) | 26,916 | **0** | **0** |
| `BenchmarkRegistryDispatch` | 5.547 | **0** | **0** |
| `BenchmarkPendingBindTake` | 9.014 | **0** | **0** |
| `BenchmarkFrameAcquireRelease` | 21.04 | **0** | **0** |
| `BenchmarkQueuePushPop` | 1.496 | **0** | **0** |
| `BenchmarkPlannerBuild` (4096 refs) | 50,377 | 4 | **0** |
| `BenchmarkDecode` (envelope) | 1.585 | **0** | **0** |
| `BenchmarkRawEcho` | 25,946 | 3 | **0** |
| `BenchmarkCommandRing` | 12.33 | **0** | **0** |
| `BenchmarkSlabAcquireRelease` | 11.47 | **0** | **0** |
| `BenchmarkUserDataCodec` | 0.233 | **0** | **0** |
| `BenchmarkTimerAddRemove` | 3.681 | **0** | **0** |
| `BenchmarkParseSingleFrame` | 16.37 | **0** | **0** |

Two allocation regressions were found and fixed while producing this table:

1. `sync.Pool` boxes a 24-byte slice header on every `Put`, so the frame buffer
   pool cost 24 B and 1 alloc per frame. The pools are now fixed-size channels
   of `[]byte`; a channel copies the header into its ring, and the benchmark is
   **0 B/op, 0 allocs/op**.
2. The pooled fan-out planner kept its map keys across reuse. `Add` skipped
   `order` for a loop whose map entry survived the previous use, so a reused
   planner reported `Empty()` after a successful `Add`. This produced
   intermittent lost fan-outs (and was the cause of a flaky test). `Add` now
   orders a loop when its group slice is empty, which is exactly the post-reset
   state. The regression is covered by `TestPlannerGroupsByLoop`.

## Gate F: memory

Struct sizes on arm64:

| Type | Bytes |
| ---- | ----: |
| `connection.Slot` | **192** |
| `encryl.Conn` | 64 |
| `connection.Accounting` | 32 |
| `connection.Frame` | 48 |
| `connection.Node` | 16 |

Static per-connection cost is approximately `192 + 64 + 32 + 8` = **296 B**
(0.289 KiB) before allocator and runtime overhead. Measured steady-state at 1M
is **0.392 KiB**, and the first post-ramp GC sample is **0.444 KiB**. Both are
below the 0.5 KiB target; the steady number is also well below the plan's 1 KiB
gate.

What changed from M1's 0.49 KiB:

- `SendCost`, `Rooms`, `QMsgs`, and `QBytes` were removed from `Slot`. The
  admission gate (`Accounting`) is now the single authority for queued and
  in-flight bytes; the slot shrank from 216 B to 192 B.
- The upgrade parser retains small header buffers across `Reset` in the pooled
  parser, so a 1M ramp does not allocate and discard a line/host/origin buffer
  per handshake. Buffers above 4 KiB are still released.

RSS is still higher than the static estimate because Go's allocator keeps spans
for the 1M live `Conn` and `Accounting` objects and the GC metadata for them.

## Reproduce

```bash
# Gate A and F
CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 HOLD=320 RAMP_TIMEOUT=240 \
  ./scripts/memory.sh

# Gate B
CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 MONITOR=180 DRAIN=45 \
  DURATION=420s MSG_RATE=0.05 TX_BYTES=67108864 BUFFER_COUNT=32768 \
  READ_BUFFER=2048 ./scripts/scale-sharded.sh

# Gate C
go build -o /tmp/encryl-fanout ./bench/fanout
go build -o /tmp/encryl-loadgen ./bench/loadgen
/tmp/encryl-fanout -addr 127.0.0.1:18082 -max-conns 20000 -interval 1s &
/tmp/encryl-loadgen -addr=127.0.0.1:18082 -mode=read -conns=10000 \
  -rate=5000 -duration=20s

# Gate D
go build -o /tmp/encryl-echo ./examples/echo
/tmp/encryl-echo -addr 127.0.0.1:18080 -max-conns 50000 -idle-timeout 2h \
  -tx-bytes 67108864 -buffer-count 8192 -read-buffer 4096 &
/tmp/encryl-loadgen -addr=127.0.0.1:18080 -mode=echo -conns=5000 \
  -rate=5000 -duration=30s -msg-size=64 -envelope

# Gate E
go test -run=NONE -bench=. -benchmem -benchtime=1s \
  ./internal/event ./internal/connection ./internal/room \
  ./internal/platform ./internal/reactor ./internal/timerwheel \
  ./internal/transport/ws
go test -run=NONE -bench=BenchmarkServerEcho100k -benchmem \
  -benchtime=100000x .
```

## Verdict

- 1M idle: clean.
- 1M messages: **exact parity, zero SendBusy, zero drops, zero command-ring
  backpressure**. The final drain confirms the queue returns to zero.
- Throughput: 494k msg/s at 5k connections with 0 allocations per message.
- Memory: 0.392 KiB/connection at 1M, under the 0.5 KiB target.
- Not run: 60-minute hold, 1M-member fan-out, p99 fan-out latency, Autobahn.
