# Restructure regression report

Date: 2026-10-25
Commit: working tree on `v2`
Host: `lima-encryl-dev`, Apple Silicon, 8 vCPU
Kernel: 6.8.0-147-generic
Memory: 15.6 GiB
Go: 1.27.2 linux/arm64
Command: `go test ./...`; `go test -race ./...`; `make gate-e`
Sysctls: `somaxconn=65535`, `tcp_max_syn_backlog=65535`, `ip_local_port_range=1024-65535`
TLS mode: plaintext `ws://`
Raw: console output in this report

The package move and map-policy changes preserve the existing hot-path and
idle-memory results. The final gate run is appended after the implementation
verification. A number without a command does not count.

## Final gates

| Gate | Command | Result |
| ---- | ------- | ------ |
| Build | `go build ./...` | pass |
| Vet | `go vet $(go list ./... \| grep -v uring/giouring)` | pass |
| Staticcheck | `staticcheck $(go list ./... \| grep -v uring/giouring)` | pass |
| Tests | `go test ./...` | pass |
| Race | `go test -race ./...` | pass |
| Format | `gofmt -l .` | empty |
| Hot-path allocation | `COUNT=3 ./scripts/qualify-messages.sh` | pass; 0 B/op, 0 allocs/op |
| Message rate | `DURATION=10s ./scripts/qualify-rate.sh` | 97,001 messages, 0 failed, 9,697/s, p99 0 µs |
| 1M idle memory | `CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=20000 HOLD=10 SAMPLE=10 RAMP_TIMEOUT=300 ./scripts/memory.sh` | 1,000,000 open, 30 fds, 0 errors, **0.342 KiB/conn gate delta**, **0.376 KiB/conn total** (hold sample 256,388 KiB; an earlier run measured 0.241 / 0.275) |
| Room benchmark | `go test -run=NONE -bench=BenchmarkSnapshot1000 -benchmem -count=8 ./bench/room` | 4.92-5.15 µs/op, 0 B/op, 0 allocs/op; previous 5.035 µs/op |

The 1M run reported 137,012 accept errors during the ramp. All 1,000,000
connections were open at the hold sample, with zero protocol, file-table,
receive, send, or illegal-transition errors. The errors are accepted as a ramp
artifact, not a steady-state leak.


## Map-policy comparison

| Path | Before | After | Change |
| ---- | -----: | ----: | -----: |
| `BenchmarkSnapshot1000` | 5.035 µs/op | 4.857 µs/op | ~3.5% faster |
| `BenchmarkRegistryDispatch` | 0.743 ns/op | 0.723 ns/op | ~2.7% faster |
| `BenchmarkPlannerBuild` | 54.6 µs/op | 56.6 µs/op | noise range; 0 B/op |
| `BenchmarkAddRemove` | 3.9 ns/op | 3.47 ns/op | noise range |

The room directory and rejection counters are not on the idle per-connection
path: an idle connection allocates neither a room entry nor a rejection entry.
The map change therefore does not add bytes to the connection record. Current
struct sizes remain `Slot=160 B`, `Conn=88 B`, `Accounting=32 B`, `Frame=48 B`.

## Current hot-path suite

| Benchmark | Result |
| --------- | ------ |
| `BenchmarkServerEcho100k` | 27.55 µs/op, 0 B/op, 0 allocs/op |
| `BenchmarkStringEcho100k` | 27.46 µs/op, 0 B/op, 0 allocs/op |
| `BenchmarkTopicEcho100k` | 27.34 µs/op, 0 B/op, 0 allocs/op |
| `BenchmarkProtoSendEcho100k` | 28.07 µs/op, 0 B/op, 0 allocs/op |
| `BenchmarkParseSingleFrame` | 16.48 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkCommandRing` | 14.34 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkSlabAcquireRelease` | 10.83 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkQueuePushPop` | 1.12 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkAccountingAdmit` | 7.92 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkFrameAcquireRelease` | 20.48 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkPendingBindTake` | 9.11 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkDecodePublish` | 33.87 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkDecodeBatch16` | 530.7 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkEncodeEventPush` | 20.14 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkEncodeMessagePush` | 16.56 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkRawEcho` | 26.09 µs/op, 6 B/op, 0 allocs/op (documented exception) |
