# Benchmark Overview (ASD-STE100)

## 1. Purpose

This document explains the performance tests.

It uses short sentences and simple words.

The tests compare WebSocket and HTTP servers.

The tests use the same application and the same database.

## 2. Terms

- **Test**: A controlled run that measures performance.
- **Request**: One HTTP call or one WebSocket message.
- **Throughput**: The number of requests in one second.
- **Latency**: The time to complete one request.
- **p50**: 50 percent of requests are equal to or faster than this time.
- **p99**: 99 percent of requests are equal to or faster than this time.
- **p99.99**: 99.99 percent of requests are equal to or faster than this time.
- **Load generator**: The client program that sends requests.
- **Server under test**: The HTTP or WebSocket server that receives requests.
- **SQLite**: The database engine used by the test application.
- **Geohash**: A short text key for a map area.

## 3. Test application

The test application is a ride-sharing service.

It has these parts:

- drivers with a latitude and a longitude;
- a geohash value for each driver;
- rides with a rider, a location, and a geohash value;
- a nearby-driver query;
- a WebSocket message that sends a location and receives nearby drivers.

The database schema is the same in all servers.

The seed data is the same in all servers.

The JSON output is the same in all servers.

## 4. Benchmark groups

### 4.1 HTTP raw output

- Route: `GET /raw`
- Database work: none
- Purpose: measure the HTTP server and JSON response path
- Main value: requests per second and p99 latency

### 4.2 HTTP database

- Route: `GET /nearby?lat=...&lon=...`
- Database work: one SQLite geohash range query
- Purpose: measure the HTTP server and database read path
- Main value: requests per second and p99 latency

The application also has `POST /rides`.
The current HTTP test does not load this write route.

### 4.3 WebSocket inline database

- Route: `GET /ws`
- Mode: `inline`
- Database work: SQLite query in the message handler
- Purpose: measure an end-to-end database path
- Warning: this is not the recommended production design

### 4.4 WebSocket cache

- Route: `GET /ws`
- Mode: `cache`
- Database work: no database read for a cache hit
- Data source: an in-memory geohash index
- Data load: SQLite loads the index at startup
- Purpose: measure the hot read path
- Warning: the current cache has no size limit

### 4.5 WebSocket raw

- Route: `GET /ws`
- Mode: `raw`
- Database work: none
- Response: one precomputed frame
- Purpose: measure the protocol and server path without application work
- Warning: this is a test mode, not a production mode

### 4.6 WebSocket worker

- Route: `GET /ws`
- Mode: `worker`
- Server: encryl only
- Database work: SQLite query in a worker pool
- Purpose: keep database work off the reactor loop
- Warning: the worker queue adds latency at high load

### 4.7 Mixed HTTP and WebSocket

- Routes: `GET /raw`, `GET /nearby`, and `GET /ws`
- Server: one HTTP listener and one WebSocket reactor
- Purpose: measure both protocols in one process
- Load: HTTP and WebSocket load at the same time
- Warning: the two loads share one server CPU

### 4.8 WebSocket connection capacity

- Route: `GET /ws`
- Load: idle WebSocket connections
- Purpose: measure the maximum number of open sockets
- Server limit: one CPU and a 2 GiB cgroup
- Warning: this test does not send messages

### 4.9 Ingest and data transfer

- Route: `GET /ws`
- Load: 100,000 WebSocket connections
- Message: one 64-byte location envelope per second per connection
- Response: nearby-driver data
- Purpose: measure message ingest and inbound data transfer
- Warning: this is not a production traffic model

### 4.10 HTTP stack variants for encryl

The test has two encryl HTTP rows.

`encryl_nethttp`:

- Uses the Go standard library `net/http`.
- Uses `http.Server` and `http.ServeMux`.
- Uses the standard net/http connection model.
- Uses encryl only for `/ws`.

`encryl_httpuring`:

- Uses the encryl `httpuring` server.
- Uses one io_uring HTTP/1.1 event loop.
- Uses `Workers=0`.
- Uses encryl for `/ws`.
- Does not use `net/http`.

Both rows use the same application and database.
`httpuring` is plaintext HTTP/1.1.
TLS must terminate upstream.

## 5. Test libraries

### HTTP

| Library | Server |
|---|---|
| encryl | httpuring |
| chi | net/http |
| Echo v4 | Echo |
| Gin | Gin |
| Fiber v3 | Fiber |

### WebSocket

| Library | Server |
|---|---|
| encryl | encryl reactor |
| gorilla/websocket | net/http |
| coder/websocket | net/http |
| gobwas/ws | net/http |
| fasthttp/websocket | fasthttp |

## 6. Test environment

The HTTP test uses these limits:

- Server and load generator: one CPU cgroup
- CPU quota: 100 percent
- Memory limit: 2 GiB
- `GOMAXPROCS=1`
- Loopback network

The WebSocket p99.99 test uses these limits:

- Server: CPU 0
- Server cgroup: one CPU, 2 GiB
- Server: `GOMAXPROCS=1`
- Load generator: CPUs 1 to 4
- Loopback network
- One server at a time
- SQLite WAL mode
- 25,000 seeded drivers

## 7. Test tools

- `scripts/build.sh`: builds all servers.
- `scripts/seed.sh`: creates the SQLite data.
- `scripts/run-http.sh`: runs one HTTP target.
- `scripts/run-ws.sh`: runs one WebSocket target.
- `scripts/run-p9999-isolated.sh`: runs one WebSocket target with CPU isolation.
- `scripts/run-p9999-all.sh`: runs the full WebSocket matrix.
- `wrk`: sends the HTTP load.
- `wsload`: sends the WebSocket load.

`wsload` sends the same native envelope to all WebSocket servers.
This keeps the wire format equal.

## 8. Load values

HTTP:

```text
wrk -t1 -c128 -d10s --latency
```

WebSocket latency test:

```text
200 connections
500 messages per second per connection
10 seconds
about 1,000,000 messages
```

## 9. What the tests record

### 9.1 Current WebSocket p99.99 run

The run records these values:

- offered connections;
- messages per second per connection;
- duration;
- completed messages;
- failed clients;
- requests per second;
- p50 latency;
- p99 latency;
- p99.9 latency;
- p99.99 latency;
- maximum latency;
- server CPU placement;
- load-generator CPU placement.

The run uses the same database, seed, and wire envelope for all servers.

### 9.2 Current HTTP run

The run records these values:

- requests per second;
- p99 latency;
- transfer rate;
- total requests;
- non-2xx responses.

The HTTP tool reports p99.
It does not report p99.99.

### 9.3 Separate server checks

The slow-client and upgrade reports also record these values:

- accepted connections;
- open connections;
- closed connections;
- protocol errors;
- accept errors;
- receive errors;
- send errors;
- buffer exhaustion;
- completion queue overflow;
- illegal state changes;
- write queue depth;
- write queue bytes;
- backpressure count;
- dropped messages;
- slow consumers;
- read pauses;
- process memory;
- cgroup memory;
- open file descriptors.

These values are not all recorded by the current p99.99 script.

### 9.4 Correctness checks

The current test uses the same application data and the same request shape.

These checks are not complete:

- The WebSocket client checks the message length.
- The WebSocket client does not compare every response payload.
- The HTTP test does not compare every response body.
- The tests use one seed and one database size.
- The tests do not test cache invalidation.

Before a production release, add payload checks and database result checks.

## 10. Pass and fail rules

The current encryl gate is:

- `cache` p99.99 is less than 1.8 ms.
- `raw` p99.99 is less than 1.8 ms.
- The value is the median of three runs.
- Failed clients are zero.
- The server and load generator use the same test data.
- The server and load generator use the same load.

The full WebSocket matrix has the same load rules.
The matrix shows the result for each library.

The gate does not replace these checks:

- protocol correctness;
- response payload correctness;
- backpressure behavior;
- slow-client behavior;
- memory limits;
- cache invalidation;
- authentication;
- TLS.

## 11. Current results

### HTTP

| Server | /raw requests per second | /raw p99 | /nearby requests per second | /nearby p99 |
|---|---:|---:|---:|---:|
| encryl_httpuring | 389,649 | 0.368 ms | 41,401 | 5.18 ms |
| encryl_nethttp | 173,522 | 1.61 ms | 29,334 | 8.82 ms |
| chi + net/http | 95,713 | 40.81 ms | 23,617 | 27.47 ms |
| Echo v4 | 97,666 | 41.94 ms | 23,563 | 27.05 ms |
| Gin | 100,685 | 42.46 ms | 23,585 | 27.87 ms |
| Fiber v3 | 155,990 | 49.88 ms | 25,799 | 28.83 ms |

The HTTP test uses `wrk`.
`wrk` reports p99.
It does not report p99.99.

### WebSocket p99.99

| Server | inline | cache | raw |
|---|---:|---:|---:|
| encryl | 8.598 ms | 0.995 ms | 1.654 ms |
| gorilla/websocket | 14.372 ms | 4.693 ms | 2.126 ms |
| coder/websocket | 13.560 ms | 2.228 ms | 1.900 ms |
| gobwas/ws | 17.840 ms | 5.040 ms | 4.763 ms |
| fasthttp/websocket | 22.609 ms | 6.913 ms | 2.912 ms |
| encryl worker | 31.760 ms | — | — |

The encryl cache p99.99 median is 1.129 ms.
The encryl raw p99.99 median is 1.098 ms.
Both values are below 1.8 ms.

## 11.1 Mixed HTTP and WebSocket results

| Server | HTTP raw | WS msg/s | WS p99.99 | WS failed |
|---|---:|---:|---:|---:|
| **encryl_httpuring** | **312,171** | 49,823 | 21.245 ms | 0 |
| encryl_nethttp | 92,260 | 49,807 | 56.358 ms | 0 |
| gorilla | 110,209 | 49,781 | 17.184 ms | 0 |
| coder | 95,929 | 49,778 | 20.899 ms | 0 |
| gobwas | 96,659 | 49,778 | 24.804 ms | 0 |
| fasthttp | **146,555** | 49,783 | **16.482 ms** | 0 |

The first encryl mixed run had 407 WebSocket failures. The attached-mode
attach ring was too small. The ring is now 16,384 entries. The rerun completed
1,000,000 messages with 0 failures.

## 11.2 WebSocket capacity results

| Server | Peak open | Failed | Cgroup peak |
|---|---:|---:|---:|
| **encryl** | **500,000** | **0** | 1,101.8 MiB |
| gorilla | 113,672 | 386,328 | — |
| coder | 84,048 | 415,952 | — |
| gobwas | 94,628 | 405,372 | — |
| fasthttp | 132,918 | 367,082 | — |

The non-encryl servers ended during the ramp.

## 11.3 Ingest and data transfer results

| Server | Peak open | Failed | Msg/s | Inbound MiB/s |
|---|---:|---:|---:|---:|
| **encryl** | **99,994** | **0** | **61,953** | **71.0** |
| gorilla | 99,062 | 938 | 28,629 | 32.8 |
| coder | 49,997 | 22,762 | 17,355 | 19.9 |
| gobwas | 49,991 | 13,885 | 20,970 | 24.0 |
| fasthttp | 49,999 | 15,617 | 20,989 | 24.1 |

The non-encryl servers ended during the 100k ingest run.

## 11.4 What we are best at

- HTTP raw throughput: encryl_httpuring, 389,649 requests per second.
- HTTP database throughput: encryl_httpuring, 41,401 requests per second.
- WebSocket p99.99: encryl cache 0.995 ms, raw 1.654 ms.
- Open WebSocket connections: encryl, 500,000.
- 100k-connection ingest: encryl, 61,953 messages per second.
- Mixed HTTP and WebSocket: encryl_httpuring leads HTTP raw and database.
  fasthttp leads WebSocket p99.99.

## 12. Limits

The tests do not check these items:

- TLS.
- Login and authentication.
- Multiple hosts.
- Cross-host room fan-out.
- A real wide-area network.
- Disk failure.
- Network failure.
- Large write traffic.
- Production data size.
- Cache invalidation.
- Cache size limits.
- Server-side latency histograms.

The WebSocket p99.99 value is client round-trip time.
The client time includes two scheduler hops.
A server-side histogram is the next measurement step.

## 13. Important warnings

- Do not use `inline` mode in production.
- Do not use `raw` mode in production.
- Use `cache` with a size limit and an invalidation plan.
- Use `worker` for a slow database. Do not use it as a general latency fix.
- Call `EmitIDLocal` and `EmitFrameLocal` only from the connection's own loop.
- Do not compare one best run. Compare the median of three or more runs.
- Do not compare results from different CPU quotas.

## 14. Reproduce

```bash
cd ../encryl-benchmark
./scripts/build.sh
./scripts/seed.sh
./scripts/run-p9999-all.sh
```

For one target:

```bash
./scripts/run-p9999-isolated.sh encryl cache 200 500 10s cache
./scripts/run-p9999-isolated.sh encryl raw   200 500 10s raw
```
