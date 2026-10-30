# FD handoff: attached net/http, fasthttp, and Fiber

Date: 2026-10-24
Commit: working tree (`5d22909` plus the fd-handoff changes)
Author: Sairash

## Environment

| Field | Value |
| ----- | ----- |
| Host | `lima-encryl-dev`, Apple Silicon, 8 vCPU |
| Kernel | 6.8.0-147-generic |
| Memory | 15.6 GiB |
| Go | 1.27.2 linux/arm64 |
| Sysctls | `somaxconn=65535`, `tcp_max_syn_backlog=65535`, `ip_local_port_range=1024-65535`, hard `ulimit -n=1048576` |
| TLS mode | plaintext `ws://` (TLS terminates upstream) |
| Raw | console output in this report; `/tmp/encryl-bench-final.txt` |

## Scope

This run covers the new attached mode:

- `Server.Start` without a listener.
- `Server.ServeHTTP` for net/http.
- `adapters/fasthttp` and `adapters/fiberv3`.
- `F_DUPFD_CLOEXEC` handoff, direct `101` from the reactor, and pipelined
  first-frame delivery.

The 60-minute hold gate was replaced by a **6-minute hold** (`HOLD=360`) as
requested. The result below is not a substitute for the published 60-minute
claim; it is a shorter stability check on the same build.

## Correctness gates

All of these passed in the Linux VM:

```sh
go test ./...
go test -race ./...
go test -gcflags=all=-d=checkptr=2 ./...
cd adapters && go test ./...
cd adapters && go test -race ./...
```

Coverage added by this work:

- net/http attach: `TestServeHTTPAttached`
- pipelined HTTP request + WebSocket frame: `TestServeHTTPPipelinedFrame`
- fasthttp hijack: `TestFasthttpAttached`
- Fiber v3 route: `TestFiberV3Attached`
- reject before hijack: `TestServeHTTPRejectsBeforeHijack`
- attach lifecycle: `TestStartShutdownAttached`
- FD flags and close ownership: `TestDuplicateConnFD`
- TLS rejection: `TestDuplicateConnFDRejectsTLS`
- handshake parity and allocation: `TestPrepareUpgradeParity`,
  `TestPrepareUpgradeZeroAlloc`

`PrepareUpgrade` is **0 allocs/op** for the valid request matrix. The descriptor
helper is bounded by the standard `net.Conn.SyscallConn` raw-conn wrapper; the
handoff itself adds no allocation, and `Slot` remains **192 B**.

## Microbenchmarks

Command:

```sh
go test -run=NONE -bench=. -benchmem -benchtime=1s ./...
```

Single sweep on the same VM, after the hold completed:

| Benchmark | ns/op | B/op | allocs/op |
| --------- | ----: | ---: | --------: |
| `BenchmarkServerEcho100k` | 27,731 | 0 | 0 |
| `BenchmarkStringEcho100k` | 27,619 | 0 | 0 |
| `BenchmarkTopicEcho100k` | 27,377 | 0 | 0 |
| `BenchmarkProtoSendEcho100k` | 28,027 | 0 | 0 |
| `BenchmarkRawEcho` (100k) | 26,524 | 3 | 0 |
| `BenchmarkEventIDForName/chat` | 1.342 | 0 | 0 |
| `BenchmarkEventIDForName/chat.message` | 3.556 | 0 | 0 |
| `BenchmarkEventIDForName/room:presence:update` | 5.361 | 0 | 0 |
| `BenchmarkRegistryDispatch` | 0.724 | 0 | 0 |
| `BenchmarkPendingBindTake` | 9.474 | 0 | 0 |
| `BenchmarkFrameAcquireRelease` | 21.56 | 0 | 0 |
| `BenchmarkQueuePushPop` | 0.7018 | 0 | 0 |
| `BenchmarkCommandRing` | 13.86 | 0 | 0 |
| `BenchmarkSlabAcquireRelease` | 11.40 | 0 | 0 |
| `BenchmarkDecode` | 1.634 | 0 | 0 |
| `BenchmarkEncodeMessage` | 0.5331 | 0 | 0 |
| `BenchmarkDecodePublish` | 35.45 | 0 | 0 |
| `BenchmarkDecodeBatch16` | 552.1 | 0 | 0 |
| `BenchmarkEncodeEventPush` | 21.36 | 0 | 0 |
| `BenchmarkEncodeMessagePush` | 17.93 | 0 | 0 |
| `BenchmarkSnapshot1000` | 5,035 | 0 | 0 |
| `BenchmarkPlannerBuild` | 49,767 | 4 | 0 |
| `BenchmarkAddRemove` | 3.800 | 0 | 0 |
| `BenchmarkParseSingleFrame` | 16.78 | 0 | 0 |

### Comparison with recorded values

| Benchmark | Recorded | This run | Delta |
| --------- | -------: | -------: | ----: |
| Server echo | 26,916 | 27,731 | +3.0% |
| String echo | 26,807 | 27,619 | +3.0% |
| Topic echo | 26,804 | 27,377 | +2.1% |
| Protobuf echo | 27,116 | 28,027 | +3.4% |
| Raw echo | 25,946 | 26,524 | +2.2% |
| Hash `chat` | 1.13 | 1.342 | +18.8% (0.21 ns absolute) |
| Hash `chat.message` | 3.44 | 3.556 | +3.4% |
| Hash `room:presence:update` | 5.26 | 5.361 | +1.9% |
| Frame acquire/release | 21.04 | 21.56 | +2.5% |
| Slab acquire/release | 11.47 | 11.40 | -0.6% |
| Frame parse | 16.37 | 16.78 | +2.5% |
| Protocol decode publish | 40.0 | 35.45 | -11.4% |
| Protocol decode batch 16 | 746.6 | 552.1 | -26.1% |
| Protocol encode event | 36.2 | 21.36 | -41.0% |
| Protocol encode self-room | 25.6 | 17.93 | -30.0% |
| Command ring | 12.33 | 13.86 | +12.4% |

The message path has no allocation regression: every public echo path is still
**0 B/op, 0 allocs/op**. Echo timings are 2–3% slower than the recorded values,
which is within the run-to-run spread of this 8-vCPU VM.

Two changes need context rather than a bare delta:

- **Command ring.** The recorded 12.33 ns predates the `SelfExcept` and
  `Encoding` fields that are already present in this working tree. The attach
  handoff is now on a separate 256-entry `AttachRing`, so it adds zero bytes to
  `Command`. A six-run recheck of the unchanged ring gave 13.86–15.05 ns; the
  regression is in the mutex-ring microbenchmark, not the send/fan-out path.
- **Hash `chat`.** 1.342 ns vs 1.13 ns is a 0.21 ns absolute change on a
  1.3 ns operation. The longer names remain 3.6/5.4 ns, and the hash still
  happens once per emit rather than once per recipient.

## 6-minute 1M hold

Command, with the 60-minute gate shortened to 6 minutes:

```sh
make gate-a CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 \
  HOLD=360 SAMPLE=60 RAMP_TIMEOUT=600
```

| Metric | Value |
| ------ | ----- |
| Ramp | 1,000,000 open at t=59s; target 990,000 |
| Accepted / open / closed | 1,000,000 / 1,000,000 / 0 |
| Protocol / recv / send / illegal / eventfd errors | 0 / 0 / 0 / 0 / 0 |
| Baseline RSS | 33,372 KiB |
| Ramp-end RSS | 551,688 KiB (**0.518 KiB/conn**) |
| Hold-end RSS | 444,872 KiB (**0.412 KiB/conn**) |
| RSS change during hold | -19.36% (GC reclaimed ramp slack) |
| FD ramp / hold / delta | 1,000,028 / 1,000,030 / **+2** |
| Messages in / out | 0 / 0 (idle hold) |

The recorded report values are 0.392 KiB/conn steady and 0.444 KiB/conn
post-ramp. This run is 5.1% higher steady and 16.7% higher at ramp end. That is
expected: the recorded numbers predate the named-event and protobuf user-data
fields; the README already marks a fresh 1M run as pending. The hold itself is
flat (444,704 → 444,880 → 444,872 KiB), with zero closed connections and zero
per-connection descriptors added during the hold. The +2 FD delta is a fixed
process descriptor drift between the two samples, not a per-connection leak;
the connection count stays exactly 1,000,000.

## Result

- Attached net/http, fasthttp, and Fiber v3 paths work with the same event API
  and the same **0 B/op, 0 allocs/op** message path.
- The handoff duplicates the fd with one `F_DUPFD_CLOEXEC` call and copies no
  socket payload. Only bytes already buffered by the framework are copied.
- Rejections happen before the fd is taken; a pipelined first frame is delivered
  after the reactor writes the 101.
- No message-path allocation regression. Echo benchmarks are within ~3% of the
  recorded values; the remaining deltas are explained above.
- The shortened 1M hold is stable. The full 60-minute gate remains open.
