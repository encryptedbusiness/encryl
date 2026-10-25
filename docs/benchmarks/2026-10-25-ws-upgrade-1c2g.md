# HTTP handler + WebSocket upgrade on one io_uring listener

Date: 2026-10-25
Commit: working tree on `v2`
Host: `lima-encryl-dev`, Apple Silicon, VM 8 vCPU / 15.6 GiB
Kernel: 6.8.0-147-generic
Memory budget: server cgroup `MemoryMax=2G`, `CPUQuota=100%`
Go: 1.27.2 linux/arm64
Client: `bench/loadgen`, one goroutine per connection, 10k/20k/8k upgrade rate
Command: see each section
TLS mode: plaintext `ws://`
Raw: console output in this report

This measures the combined path, not a WebSocket-only echo server:

- `/healthz` is a normal chi HTTP API route.
- `/ws` is a chi route that calls `srv.ServeHTTP`.
- The raw `httpuring` response writer implements `http.Hijacker`.
- Encryl duplicates the raw descriptor and owns the socket after `101`.
- Server: one reactor loop, one HTTP ring loop, `GOMAXPROCS=1`.
- Density runs use `-ping 0` so heartbeat traffic does not dominate the
  measurement.

## Upgrade path

The first combined implementation used `net/http` + `httpuring.Listen`.
That OOM-killed at 100k upgrades in the 2-GiB cgroup because net/http allocates
read/write buffers and a goroutine before hijacking.

The fixed path has no net/http server in it:

```text
io_uring accept
  -> httpuring HTTP/1.1 parse
  -> chi router /healthz
  -> chi router /ws
  -> encryl PrepareUpgrade
  -> httpuring.ResponseWriter.Hijack
  -> F_DUPFD_CLOEXEC
  -> encryl reactor writes 101 and owns the socket
```

`Hijack` is inline-only (`Workers=0`); a worker goroutine cannot safely detach
the ring-owned descriptor.

## Connection and upgrade results

All runs use the same combined `/healthz` + `/ws` listener.

| Requested | Ramp | Open | Failed | Upgrade success | Effective rate |
|---:|---:|---:|---:|---:|---:|
| 100,000 | 10,000/s | **99,968** | 32 | 99.97% | ~10k/s |
| 300,000 | 20,000/s | **266,473** | 33,527 | 88.8% | ~13k/s |
| 500,000 | 10,000/s | **486,896** | 13,104 | 97.4% | ~8k/s |
| 500,000 | 8,000/s | **490,078** | 9,922 | 98.0% | ~7k/s |

The safe recipe is **8k upgrades/s at ~500k connections**. At 100k, 10k/s is
fine. A 20k/s ramp overloads the single CPU and produces client handshake
timeouts; the server reports no protocol or file-table errors.

Final 500k-at-8k/s stats:

```text
ConnectionsAccepted: 490078
ConnectionsOpen:     490078
ConnectionsClosed:   0
ProtocolErrors:      0
AcceptErrors:        0
BufferExhausted:     0
CQOverflow:          0
```

## Memory per connection

| Open WS | Process RSS | cgroup | RSS/conn | cgroup/conn |
|---:|---:|---:|---:|---:|
| 100k | 112 MiB | 262 MiB | 1.12 MiB | 2.62 MiB |
| 300k | 318 MiB | 718 MiB | 1.19 MiB | 2.69 MiB |
| 490k | 443 MiB | 1.185 GiB | **0.90 MiB** | **2.42 MiB** |

The WebSocket-only 500k run used 431 MiB process RSS and 1.18 GiB cgroup.
The combined handler is effectively at parity: **443 MiB / 1.185 GiB for 490k
open**. The HTTP handler and upgrade state do not add a material steady-state
cost once hijacked.

Memory is not the 500k limit. At 490k the cgroup still has ~800 MiB headroom.
The limit is the single-CPU upgrade/accept rate.

## HTTP API impact

`/healthz` at c128 while the WebSocket fleet is held:

| Open WS | HTTP req/s | p50 | p99 | Notes |
|---:|---:|---:|---:|---|
| 0 | **270,090** | 0.370 ms | 16.36 ms | baseline |
| 100k | 243,320 | 0.398 ms | 14.77 ms | normal ping |
| 300k | 219,071 | 0.450 ms | 16.75 ms | normal ping |
| 490k | **271,406** | 0.371 ms | 22.93 ms | `-ping 0` |

At 490k idle sockets, API throughput is unchanged; p99 grows by ~6.6 ms from
CPU sharing with the reactor. With 30s pings at 483k sockets, the API drops to
**165,916 req/s with p99 603 ms** because 483k heartbeat frames hit the same
one-CPU budget. Idle density should disable pings; heartbeat intervals need to
account for this cost.

## Fixes made

- **`httpuring.ResponseWriter.Hijack`**: removes net/http buffers and the
  net/http connection goroutine from `/ws`. This fixed the 100k OOM.
- **Nonblocking accept + poll-driven recv**.
- **64 outstanding accepts** and **65,535 listen backlog**.
- **8,192 RX buffers** and **16 MiB TX arena** for upgrade bursts; buffers no
  longer exhaust at 500k.
- **`SO_REUSEPORT` loop support** remains available, but multiple locked loops
  on one vCPU made tail behavior worse; keep one loop per vCPU.
- **Empty handoff `bufio` reuse** for upgrades with no pipelined frame.

## Conclusion

- The combined handler does **not** materially reduce per-connection memory:
  ~0.9 MiB process / ~2.4 MiB cgroup per open WebSocket.
- It reduces the practical 2-GiB ceiling from **500k WebSocket-only** to about
  **490k with the same HTTP handler**, and that is limited by upgrade CPU, not
  memory.
- The API remains at full throughput at 490k idle sockets; p99 pays ~6 ms.
- Heartbeats are the expensive part: 483k pings collapse API p99 to 603 ms.
- The safe upgrade rate is **8k/s at 500k**, **10k/s at 100k**.
- No net/http server buffers are involved in the upgrade path anymore.
