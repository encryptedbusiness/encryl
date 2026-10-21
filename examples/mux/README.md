# HTTP mux example

A path-based `net/http` application using `http.NewServeMux`. The same listener
serves ordinary HTTP routes and upgrades `/ws` into encryl.

```sh
go run ./examples/mux
# HTTP:       http://127.0.0.1:18093/
# Health:     http://127.0.0.1:18093/healthz
# Stats:      http://127.0.0.1:18093/api/stats
# Broadcast:  POST http://127.0.0.1:18093/api/rooms/lobby/broadcast
# WebSocket:  ws://127.0.0.1:18093/ws
```

```sh
curl http://127.0.0.1:18093/healthz
curl -X POST --data-binary 'hello' \
  http://127.0.0.1:18093/api/rooms/lobby/broadcast
```

On Linux only. The reactor is io_uring-only.

Routes:

| Method | Path | Behavior |
| ------ | ---- | -------- |
| `GET` | `/` | Usage text. |
| `GET` | `/healthz` | Liveness check. |
| `GET` | `/api/stats` | `encryl.Server.Stats` as JSON. |
| `POST` | `/api/rooms/{room}/broadcast` | Publishes the request body to a room as the `message` event. |
| `GET` | `/ws` | Hands the upgrade to encryl’s attached reactor. |

The WebSocket joins `lobby` in `OnConnect` and supports the `echo` and
`broadcast` events. `srv.Start` owns the io_uring loops; `http.Server` owns the
listener and the path routing. Shutdown stops HTTP first, then encryl.
