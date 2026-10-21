# net/http example

A plain `net/http` server that keeps its own listener and routes and hands only
the WebSocket upgrade to encryl.

```sh
go run ./examples/http
# HTTP:       http://127.0.0.1:18090/healthz
# WebSocket:  ws://127.0.0.1:18090/ws
```

On Linux only. The example uses `encryl.v2`; a client should request that
subprotocol and speak the native envelope, or use the browser client in
`examples/chat` as a reference.

Events:

| Event | Behavior |
| ----- | -------- |
| `echo` | Sends the payload back to the sender. |
| `broadcast` | Sends the payload to `lobby`, excluding the sender. |

The connection joins `lobby` in `OnConnect`. `srv.Start` initializes the reactor
without binding a port; `http.Server` owns `-addr`. `http.Server.Shutdown` stops
new HTTP connections first, then `srv.Shutdown` drains the WebSocket side.
