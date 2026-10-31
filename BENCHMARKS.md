# Benchmark Results

This is the consolidated benchmark report for the encryl repository. The
full harness is in `../encryl-benchmark/`.

Captured: 2026-10-25/26
Host: `lima-encryl-dev`, Apple Silicon, VM 8 vCPU / 15.6 GiB
Network: loopback
Database: `modernc.org/sqlite`, WAL, 25,000 seeded drivers
Server cgroup: one CPU, 2 GiB, `GOMAXPROCS=1`
Load generator: separate CPUs, one server at a time

## HTTP stacks

The two encryl HTTP rows use different transports.

### `encryl_nethttp`

- Transport: Go standard library `net/http`
- Server: `http.Server`
- Router: `http.ServeMux`
- `/ws`: encryl `ServeHTTP` + hijack/attach
- Use: common net/http control stack

### `encryl_httpuring`

- Transport: encryl `httpuring`
- Server: one io_uring HTTP/1.1 loop
- Router: `http.Handler` through the httpuring adapter
- Concurrency: `Workers=0`
- `/ws`: encryl hijack/attach
- Use: native high-throughput encryl HTTP path

Both use the same application, database, seed, and WebSocket reactor.
`httpuring` is plaintext HTTP/1.1. TLS terminates upstream.

## 1. HTTP raw and database

`wrk -t1 -c128 -d10s --latency`

| Server | `/raw` req/s | raw p99 | `/nearby` req/s | DB p99 |
|---|---:|---:|---:|---:|
| **encryl_httpuring** | **389,649** | **0.368 ms** | **41,401** | **5.18 ms** |
| encryl_nethttp | 173,522 | 1.61 ms | 29,334 | 8.82 ms |
| chi + net/http | 95,713 | 40.81 ms | 23,617 | 27.47 ms |
| Echo v4 | 97,666 | 41.94 ms | 23,563 | 27.05 ms |
| Gin | 100,685 | 42.46 ms | 23,585 | 27.87 ms |
| Fiber v3 | 155,990 | 49.88 ms | 25,799 | 28.83 ms |

`/raw` is static JSON with no database work.
`/nearby` is the same SQLite geohash range query in every server.

## 2. WebSocket p99.99

Isolated test: 200 connections × 500 messages/s, 10 seconds, zero failures.
The WebSocket servers use the same SQLite seed, native envelope, and modes.

| Server | inline | cache | raw |
|---|---:|---:|---:|
| **encryl** | 8.598 ms | **0.995 ms** | **1.654 ms** |
| gorilla/websocket | 14.372 ms | 4.693 ms | 2.126 ms |
| coder/websocket | 13.560 ms | 2.228 ms | 1.900 ms |
| gobwas/ws | 17.840 ms | 5.040 ms | 4.763 ms |
| fasthttp/websocket | 22.609 ms | 6.913 ms | 2.912 ms |
| encryl worker | 31.760 ms | — | — |

encryl cache and raw 3-run p99.99 medians:

- cache: **1.129 ms**
- raw: **1.098 ms**

Both are below the 1.8 ms gate.

## 3. Mixed HTTP + WebSocket

1,000 WebSocket clients at 50 messages/s, concurrent HTTP load.
Encryl_httpuring, nethttp, gorilla, coder, and gobwas serve HTTP from the
same process. fasthttp uses its native server.

| Server | HTTP raw | raw p99 | HTTP DB | DB p99 | WS msg/s | WS p99.99 | WS failed |
|---|---:|---:|---:|---:|---:|---:|---:|
| **encryl_httpuring** | **312,171** | 15.41 ms | **30,623** | 24.07 ms | 49,823 | 21.245 ms | 0 |
| encryl_nethttp | 92,260 | 18.79 ms | 16,698 | 25.93 ms | 49,807 | 56.358 ms | 0 |
| gorilla | 110,209 | 5.86 ms | 22,663 | 13.73 ms | 49,781 | 17.184 ms | 0 |
| coder | 95,929 | 8.38 ms | 21,143 | 17.11 ms | 49,778 | 20.899 ms | 0 |
| gobwas | 96,659 | 7.45 ms | 20,778 | 15.98 ms | 49,778 | 24.804 ms | 0 |
| **fasthttp** | 146,555 | **5.48 ms** | 22,610 | 13.77 ms | 49,783 | **16.482 ms** | 0 |

The first encryl mixed run had 407 WebSocket failures. The attached-mode
`attachRing` capacity was 256. `Server.Upgrade` returned backpressure, and the
HTTP handler closed the connection. The ring is now 16,384 entries. Both
encryl rows now complete 1,000,000 messages with zero failures.

## 4. WebSocket connection capacity

1 CPU, 2 GiB cgroup, idle WebSocket connections, 75-second run.

| Server | Peak open | Failed | Server RSS | Cgroup peak |
|---|---:|---:|---:|---:|
| **encryl** | **500,000** | **0** | 225.2 MiB | 1,101.8 MiB |
| gorilla | 113,672 | 386,328 | — | — |
| coder | 84,048 | 415,952 | — | — |
| gobwas | 94,628 | 405,372 | — | — |
| fasthttp | 132,918 | 367,082 | — | — |

The non-encryl servers ended during the ramp. The dash means the server
cgroup was gone after exit.

## 5. Ingest and data transfer at 100k connections

100,000 connections, one 64-byte location envelope per second per connection,
with a nearby response.

| Server | Peak open | Failed | Messages | Msg/s | Inbound MiB/s | Cgroup peak |
|---|---:|---:|---:|---:|---:|---:|
| **encryl** | **99,994** | **0** | **949,903** | **61,953** | **71.0** | 509.0 MiB |
| gorilla | 99,062 | 938 | 429,503 | 28,629 | 32.8 | — |
| coder | 49,997 | 22,762 | 260,392 | 17,355 | 19.9 | — |
| gobwas | 49,991 | 13,885 | 314,670 | 20,970 | 24.0 | — |
| fasthttp | 49,999 | 15,617 | 314,896 | 20,989 | 24.1 | — |

The non-encryl servers ended during the 100k ingest run.

## Summary: what encryl is best at

- **HTTP raw throughput:** `encryl_httpuring`, **389,649 req/s**.
- **HTTP database throughput:** `encryl_httpuring`, **41,401 req/s**.
- **WebSocket p99.99:** encryl, cache **0.995 ms**, raw **1.654 ms**.
- **Mixed HTTP raw:** `encryl_httpuring`, **312,171 req/s**.
- **Mixed HTTP database:** `encryl_httpuring`, **30,623 req/s**.
- **Connection density:** encryl, **500,000** idle WebSockets, zero failures.
- **100k-connection ingest:** encryl, **61,953 msg/s**, **71.0 MiB/s**,
  zero failures.
- **Mixed WebSocket p99.99:** fasthttp leads at **16.482 ms**; encryl_httpuring
  is second at **21.245 ms**.

## Protocol codec: Bebop

Ubuntu 24.04 / kernel 6.8.0-147 / arm64 / Go 1.27.2, 8 vCPU Lima VM.

| Path | Result |
| ---- | ------ |
| Decode publish, 64B | **10.6 ns** |
| Decode 16-message batch | **162.6 ns** |
| Encode event push, 64B | **17.9 ns** |
| Encode Self-Room push, 64B | **15.8 ns** |
| Fan-out to 1000 recipients | **1696 ns** |
| Event-push wire size | **84 B** |

Bebop is the only client event codec and uses 0 B/op and 0 allocs/op. Full
method and raw data:
[`docs/benchmarks/`](docs/benchmarks/) and ADR-0023.

## Limits

- Single host, loopback network.
- No TLS.
- No authentication.
- No multi-host routing.
- No production data volume.
- WebSocket p99.99 is client round-trip time.
- Capacity is idle connections only.
- Ingest uses one 64-byte message per connection per second.
- Results vary with host load; use medians and repeat runs.

## Reproduce

```bash
cd ../encryl-benchmark
./scripts/build.sh
./scripts/seed.sh

./scripts/run-http.sh encryl_httpuring 128 10s
./scripts/run-http.sh encryl_nethttp 128 10s

WS_RATE=50 ./scripts/run-mixed.sh encryl_httpuring
WS_RATE=50 ./scripts/run-mixed.sh encryl_nethttp
./scripts/run-p9999-all.sh
./scripts/run-capacity.sh encryl
./scripts/run-ingest.sh encryl
```

Full harness and captured logs:

- `../encryl-benchmark/README.md`
- `../encryl-benchmark/results/README.md`
- `../encryl-benchmark/results/p9999/README.md`
