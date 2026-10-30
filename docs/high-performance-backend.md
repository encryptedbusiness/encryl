# Building a high-performance backend with encryl and httpuring

This guide is for a backend that needs both ordinary HTTP APIs and a large,
long-lived WebSocket fleet. The application shape is a ride-sharing service:
HTTP login/API, SQLite/Postgres persistence, geohash proximity queries, and
WebSocket location updates.

## Architecture

```text
                 TLS / L7 load balancer
                          |
             +------------+------------+
             |                         |
        HTTP API                   WebSocket gateway
   auth, rides, history       httpuring raw loop + chi/Echo
             |                         |
       SQLite/Postgres          encryl reactor
       or a service               rooms / topics / fan-out
```

Use encryl for the WebSocket data plane. Use `httpuring` for the raw HTTP
gateway when you want maximum density and control. Do not use the
`httpuring.Listen` net/http bridge on the hot path.

## CPU topology

### Single CPU

Pin the process to one CPU and run one HTTP ring and one reactor loop.

```go
httpSrv := &httpuring.Server{
    Addr:           ":8080",
    HTTPHandler:    router,
    Workers:        0, // required for /ws hijack
    Loops:          1,
    MaxConnections: 100_000,
    HeaderTimeout:  10 * time.Second,
    IdleTimeout:    2 * time.Minute,
    WriteTimeout:   30 * time.Second,
    MaxOutputBytes: 256 << 10,
    ServerHeader:   "encryl",
}

cfg := encryl.DefaultConfig()
cfg.Loops = 1
cfg.MaxConnections = 100_000
cfg.WriteQueueMessages = 64
cfg.WriteQueueBytes = 4 << 20
cfg.Backpressure = encryl.BackpressureCloseSlow
```

Recommended single-CPU shape:

- One `httpuring` loop for HTTP and `/ws`.
- One encryl reactor loop.
- `GOMAXPROCS=1`.
- `httpuring.Server.Workers = 0`; hijack is inline-only.
- `encryl.Loops = 1`.
- No blocking database call in `/ws` or in an inline HTTP handler.
- Keep `/ws` authentication local: JWT signature check or one-time ticket.
- Use SQLite in WAL mode, or a local read cache, with DB work outside the raw
  HTTP loop.
- Use `BackpressureCloseSlow` or a small queue for untrusted WebSocket clients.
- Disable pings for idle-density runs; tune them for real presence.

Measured single-CPU reference on the development 1-vCPU/2-GiB cgroup:

| Workload | Result |
|---|---:|
| Raw `httpuring` HTTP, c128 | ~396k req/s |
| Combined `/healthz` + `/ws`, 100k open | ~243k API req/s |
| Combined `/healthz` + `/ws`, 490k open | ~271k API req/s, p99 22.9 ms |
| WebSocket open, 8k upgrades/s | 490,078 |
| Process memory | ~0.9 MiB/open socket |
| Cgroup memory | ~2.4 MiB/open socket |

At 2 GiB, plan for roughly **490k open sockets**, not millions. Migration to
millions requires multiple processes/hosts, not a larger single heap.

#### Single-CPU authentication

1. `POST /login` checks the database and creates a server-side session.
2. Issue a short-lived JWT or a one-time WebSocket ticket.
3. chi/Echo middleware validates the token; no DB in the raw loop.
4. Store the session in `Conn.UserData`.
5. Every event performs an O(1) session/permission check.
6. Revocation updates the session cache and closes matching connections.
7. Never query SQLite/Postgres from `OnConnect` or `OnMessage`.

If the handoff does not carry authenticated identity, use a first-message auth
event with a short timeout, or extend the upgrade handoff to carry metadata.

#### Single-CPU backpressure

- `WriteQueueMessages`/`WriteQueueBytes` are **per-connection**, not global.
- Slow clients can still consume aggregate memory.
- For public traffic, use `BackpressureCloseSlow`.
- For lossy presence, use `BackpressureDropOldest`.
- Add a server-wide outbound budget if you allow hundreds of thousands of
  untrusted slow consumers.
- HTTP timeouts: `HeaderTimeout`, `IdleTimeout`, `WriteTimeout`,
  `MaxOutputBytes`, and a production `MaxConnections`.

### Multiple CPUs

Run one HTTP ring and one reactor loop per CPU; keep the WebSocket upgrade path
inline.

```go
httpSrv := &httpuring.Server{
    Addr:           ":8080",
    HTTPHandler:    router,
    Workers:        0,
    Loops:          runtime.NumCPU(),
    MaxConnections: 200_000,
}
cfg := encryl.DefaultConfig()
cfg.Loops = runtime.NumCPU()
```

Each `httpuring` loop owns its own ring, listener, timers, and connection map.
`SO_REUSEPORT` distributes accepted connections. Each encryl loop owns its
connections and rooms shard.

Recommendations:

- `GOMAXPROCS = NumCPU` unless the container has a lower CPU quota.
- `httpuring.Loops = NumCPU`.
- `encryl.Loops = NumCPU`.
- `Workers = 0` for the listener serving `/ws`.
- If HTTP handlers block on a database, put those API routes on a **separate
  worker-backed API server** with its own listener. A `Workers > 0` server
  cannot hijack.
- Shard room/topic ownership where possible; a encryl room is process-local.
- For multiple hosts, add a cross-node bus (NATS, Redis Streams, Kafka) for room
  fan-out. Do not assume encryl rooms span processes.
- Terminate TLS at the load balancer. The reactor speaks plaintext `ws://`.
- Use an L7 health check and keep `/ws` on an HTTP/1.1 upstream.

#### Multiple-CPU SQLite

SQLite can serve a read-heavy ride-sharing service, but not one writer per CPU
on the same file without contention.

- Enable WAL: `journal_mode=WAL`.
- Set `busy_timeout` and a bounded connection pool.
- Serialize writes through one goroutine/queue per database.
- Keep read queries off the reactor loop; push DB work to a worker API service.
- Precompute geohash prefixes and index `(geohash)`.
- For multiple hosts, use Postgres or a read replica instead of sharing SQLite.
- Never put a DB call in `OnMessage`, `OnConnect`, or an inline `/ws` handler.

#### Multiple-CPU memory

Memory scales per open socket, not per CPU. At ~2.4 MiB cgroup per open socket,
a 16-GiB host has a theoretical ceiling around 6M sockets but the practical
limit is kernel memory, file descriptors, accept rate, and heartbeat CPU. Size
hosts conservatively and measure cgroup peak.

## Authentication and authorization

Use a two-plane model:

- **Control plane:** login, refresh, session creation/revocation, HTTP only.
- **Data plane:** authenticated socket, session pointer, room/message ACLs.

Recommended flow:

```text
login -> DB -> opaque session ID + short-lived JWT
/ws   -> local JWT/ticket check -> Hijack
OnConnect -> session in Conn.UserData
OnMessage -> O(1) session/ACL check
revoke -> session cache update -> c.Close(4001, "revoked")
```

Do not verify a JWT on every message. Do not query the database on every
message.

### Identity handoff

Current encryl `Conn` has `SetUserData`/`UserData`, but the HTTP auth
middleware's request identity is not automatically visible in `OnConnect`.
Choose one:

- Extend the `httpuring`/Fiber handoff to carry authenticated metadata, or
- First-message auth with a short timeout and no room joins before success.

A one-time ticket validated in `/ws` still needs a way to carry the resolved
identity into `Conn.UserData`.

## Geohash ride-sharing model

Use a stable geohash precision (typically 5–7) for proximity searches:

```sql
CREATE TABLE drivers (
    id INTEGER PRIMARY KEY,
    lat REAL NOT NULL,
    lon REAL NOT NULL,
    geohash TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX drivers_geohash ON drivers(geohash);

CREATE TABLE rides (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rider TEXT NOT NULL,
    lat REAL NOT NULL,
    lon REAL NOT NULL,
    geohash TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
```

`/nearby?lat=&lon=` queries `geohash LIKE prefix || '%' LIMIT 20`. If no rows,
broaden one precision level. Use a covering set of neighboring geohash cells
for production-grade proximity rather than a single prefix.

WebSocket location updates use the same query and return a small JSON payload.

## HTTP handler rules

- Keep `/raw` handler allocation-free.
- Keep `/nearby` bounded with `LIMIT`.
- Bound request bodies with `http.MaxBytesReader`.
- Do not write logs per request in production.
- Do not run blocking middleware on the inline `/ws` server.
- Keep response bodies under `MaxOutputBytes` when possible.
- Set `HeaderTimeout`, `IdleTimeout`, `WriteTimeout`, and `MaxConnections`.
- Use `BackpressureCloseSlow` for untrusted sockets.

## WebSocket handler rules

- Use `Topic`/`EmitID` so names are hashed once.
- One `Conn.UserData` pointer, not a map per message.
- Per-event `EventMiddleware` checks session expiry/ACL.
- Rooms of 100–1000 members; four room hubs on a multi-core host.
- Bounded write queues and explicit backpressure.
- Keep pings off for idle-density tests; tune for production.
- Close revoked sessions with a stable application close code.
- Use one goroutine per connection in the client, not in the server.

## Observability

Track at minimum:

- open/closed/accepted connections
- accept errors, protocol errors, recv/send errors
- backpressure, dropped messages, slow consumers
- write queue depth/bytes
- timer expirations
- cgroup memory, RSS, open FDs
- upgrade rate and failed upgrades
- HTTP p50/p99/p99.9/p99.99
- WebSocket message RTT/delivery latency

At 100k+ sockets, pings and reaper scans are visible CPU; measure them as part
of the workload.

## Checklist

- [ ] One `httpuring` loop per vCPU; one encryl loop per vCPU.
- [ ] `Workers=0` on the `/ws` listener.
- [ ] No DB call on the raw loop or reactor loop.
- [ ] JWT/ticket auth before upgrade; session in `Conn.UserData`.
- [ ] Per-message O(1) authorization; revocation reaper.
- [ ] HTTP header/idle/write timeouts and connection cap.
- [ ] WebSocket queue caps and `BackpressureCloseSlow`.
- [ ] Pings sized for the fleet.
- [ ] SQLite WAL; serialized writes; geohash index.
- [ ] Multiple hosts/processes before millions of sockets.
- [ ] Load test with the same SQLite seed and application across libraries.

## Cross-framework benchmark harness

The companion workspace `../encryl-benchmark/` runs the same ride-sharing
application against five HTTP servers and five WebSocket servers:

- HTTP: `httpuring`, chi + net/http, Echo v4, Gin, Fiber v3.
- WebSocket: encryl, gorilla/websocket, coder/websocket, gobwas/ws,
  fasthttp/websocket.
- One SQLite adapter (`modernc.org/sqlite`), one schema, one 25k-driver seed,
  and one geohash range query.
- Tests: `/raw` (no DB), `/nearby` (DB), and `/ws` (WebSocket + DB).

Build and reproduce with:

```bash
cd ../encryl-benchmark
./scripts/build.sh
./scripts/seed.sh
./scripts/run-all.sh
```

The captured 1-vCPU / 2-GiB run is in
`../encryl-benchmark/results/README.md`. It is a comparison harness, not a
production deployment: the WebSocket benchmark deliberately runs SQLite inline,
which is the opposite of the production guidance above.

## p99.99 tail latency

p99.99 is not an average-latency problem. The companion
`../encryl-benchmark/results/p9999/README.md` pins each server to one CPU
inside a 1-CPU / 2-GiB cgroup, runs the load generator on CPUs 1-4, and compares
five WebSocket libraries under identical SQLite data, native envelopes, and
load.

Current isolated matrix (200 connections × 500 messages/s, 10s, zero failures):

| Library | inline p99.99 | cache p99.99 | raw p99.99 |
|---|---:|---:|---:|
| encryl | 8.598 ms | **0.995 ms** | **1.654 ms** |
| gorilla | 14.372 ms | 4.693 ms | 2.126 ms |
| coder | 13.560 ms | 2.228 ms | 1.900 ms |
| gobwas | 17.840 ms | 5.040 ms | 4.763 ms |
| fasthttp | 22.609 ms | 6.913 ms | 2.912 ms |

The encryl gate is **cache 3-run median 1.129 ms** and **raw 1.098 ms**,
both below 1.8 ms. This came from two changes:

- loop-local send (`SendLocal`/`EmitIDLocal`/`EmitFrameLocal`) bypasses the MPSC
  command ring and eventfd when the handler is already on the owning loop;
- `raw`/`cache` prebuild complete WebSocket frames once and retain a reference
  per send, removing `BuildFrame`, payload copy, and envelope encode.

`inline` mode is still an anti-pattern. `worker` mode remains useful for slow
or contended databases but was worse than inline at this load. `raw` is a
benchmark lower bound; `cache` needs bounded LRU/TTL and explicit invalidation.
`EmitIDLocal`/`EmitFrameLocal` are loop-thread-only APIs.
