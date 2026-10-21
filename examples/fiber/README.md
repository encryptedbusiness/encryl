# Fiber example

A Fiber v3 application that keeps its own listener and hands `/ws` to
encryl.

```sh
cd examples/fiber
go run .
# HTTP:       http://127.0.0.1:18091/healthz
# WebSocket:  ws://127.0.0.1:18091/ws
```

On Linux only. The example uses `encryl.v2`; a client must negotiate that
subprotocol and speak the native envelope.

This is a separate module so the core `encryl` module does not depend on
Fiber. The adapter is imported from `github.com/encryptedbusiness/encryl/adapters/fiberv3`.

Events:

| Event | Behavior |
| ----- | -------- |
| `echo` | Sends the payload back to the sender. |
| `broadcast` | Sends the payload to `lobby`, excluding the sender. |

The connection joins `lobby` in `OnConnect`. `srv.Start` initializes the reactor
without binding a port; Fiber owns `-addr`. Shutdown stops Fiber first, then
drains the WebSocket side.
