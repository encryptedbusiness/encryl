# WebSocket upgrade on the io_uring HTTP listener

One listener serves both a chi HTTP API and WebSocket clients on the raw
`httpuring` HTTP/1.1 loop. chi routes and middleware handle `/api/*`; `/ws`
calls encryl's `ServeHTTP`, which validates the upgrade, hijacks the
ring-owned descriptor, and hands a duplicate to the encryl reactor. There
is no net/http server in this path.

```sh
cd examples/uringhttp_websocket
go run .
# HTTP API:  http://127.0.0.1:18096/healthz
# WebSocket: ws://127.0.0.1:18096/ws
```

Flags:

| Flag | Default | Purpose |
| ---- | ------- | ------- |
| `-addr` | `127.0.0.1:18096` | HTTP and WebSocket listener |
| `-loops` | `NumCPU` | reactor loops; use 1 for a 1-vCPU run |
| `-max-conns` | `100000` | encryl slab ceiling |
| `-ping` | `30s` | server ping interval; `0` disables it |
| `-quiet` | `false` | disable per-request chi logging |
| `-no-uring` | `false` | use the standard net/http listener bridge |

Use the standard Go `net/http` + `httpuring.Listen` bridge for comparison:

```sh
go run . -no-uring
```

Routes:

| Method | Path | Behavior |
| ------ | ---- | -------- |
| `GET` | `/healthz` | Liveness. |
| `GET` | `/api/stats` | encryl server stats as JSON. |
| `POST` | `/api/rooms/{room}/broadcast` | Publishes the body to a room as `message`. |
| `GET` | `/ws` | WebSocket upgrade handed to encryl. |

WebSocket events:

| Event | Behavior |
| ----- | -------- |
| `echo` | Sends the payload back to the sender. |
| `broadcast` | Sends the payload to `lobby`, excluding the sender. |

The connection joins `lobby` in `OnConnect`. The client must negotiate
`encryl.v2` and speak the native envelope.

## One listener, two protocols

The HTTP server is `httpuring.Server{HTTPHandler: router, Workers: 0}`. On
`/ws`:

1. chi invokes `srv.ServeHTTP` as an ordinary `http.Handler`.
2. encryl validates the handshake and calls `http.Hijacker.Hijack`.
3. `httpuring` detaches the ring-owned descriptor from the HTTP loop.
4. encryl duplicates it with `F_DUPFD_CLOEXEC`; the reactor writes the
   `101` and owns all subsequent frames.
5. The raw HTTP loop continues accepting API requests on the same listener.

The worker-ring mode (`-workers > 0`) cannot hijack a connection; serve `/ws`
inline.

This is an isolated example module so chi is not a dependency of the core
encryl module.
