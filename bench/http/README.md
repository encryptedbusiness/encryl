# TFB-shaped HTTP benchmark

Minimal FrameworkBenchmarks-style JSON and plaintext servers used to compare
the io_uring HTTP path with Fiber/fasthttp.

```sh
cd bench/http
go build ./cmd/uring ./cmd/uring-raw ./cmd/uring-chi ./cmd/fiber ./cmd/fasthttp-uring
```

Servers:

| Command | Transport | Port |
| ------- | --------- | ---- |
| `./uring` | net/http on `httpuring` | 18097 |
| `./uring -no-uring` | net/http on standard `net.Listen` | 18097 |
| `./fiber` | Fiber v3 with fasthttp | 18098 |
| `./fasthttp-uring` | fasthttp on `httpuring` | 18100 |
| `./uring-raw` | direct HTTP/1.1 loop on `httpuring` | 18103 |
| `./uring-chi -workers=0` | chi inline on the ring-native http.Handler adapter | 18105 |
| `./uring-chi -workers=1` | chi with the request/response ring worker | 18106 |
| `./fasthttp-uring -no-uring` | fasthttp on standard `net.Listen` | 18100 |

`/json` uses a generated-style easyjson writer into a stack buffer. The
microbenchmark is 0 B/op and 0 allocs/op; this removes JSON serialization from
the transport comparison. `/plaintext` writes a static body.

Run the server under a 1-vCPU/2-GiB cgroup and `GOMAXPROCS=1`:

```sh
sudo systemd-run --scope --unit=tfb-uring --collect \
  -p CPUQuota=100% -p MemoryMax=2G -p MemorySwapMax=0 \
  /bin/bash -c 'exec env GOMAXPROCS=1 ./uring -addr 127.0.0.1:18097'

wrk -t4 -c512 -d30s --latency -H 'Host: server' \
  -H 'Connection: keep-alive' http://127.0.0.1:18097/json
```

The full measured comparison is in
[`docs/benchmarks/2026-10-25-http-1c2g.md`](../../docs/benchmarks/2026-10-25-http-1c2g.md).
