# net/http listener example

This example creates the TCP listener explicitly, starts encryl's reactor
in attached mode, and passes the listener to `http.Server.Serve`. Only `/ws`
is handed to encryl; every other path remains ordinary `net/http`.

```sh
go run ./examples/nethttp
# HTTP:       http://127.0.0.1:18092/healthz
# WebSocket:  ws://127.0.0.1:18092/ws
```

On Linux only. The reactor is io_uring-only.

Events:

| Event | Behavior |
| ----- | -------- |
| `echo` | Sends the payload back to the sender. |
| `broadcast` | Sends the payload to `lobby`, excluding the sender. |

The connection joins `lobby` in `OnConnect`. `srv.Start` initializes the
io_uring loops without binding a port; the explicit `net.Listener` belongs to
`net/http`. Shutdown stops HTTP first, then drains encryl.

Use this as the starting point when an application already manages its own
`net.Listener` (for example, to share it with another HTTP server).
