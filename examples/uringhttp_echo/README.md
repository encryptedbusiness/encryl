# Raw HTTP echo example

A minimal HTTP echo server using `httpuring.Server`, the direct HTTP/1.1 loop
on io_uring. It does not use net/http or fasthttp.

```sh
go run ./examples/uringhttp_echo
# HTTP: http://127.0.0.1:18111
```

```sh
curl http://127.0.0.1:18111/healthz
curl -X POST --data-binary 'hello' http://127.0.0.1:18111/echo
# hello
curl -X PUT --data-binary 'hello again' http://127.0.0.1:18111/echo
# hello again
```

Routes:

| Method | Path | Behavior |
| ------ | ---- | -------- |
| `GET` | `/healthz` | Returns `ok`. |
| `POST`/`PUT` | `/echo` | Echoes the request body. |

This is the exact-path `httpuring.Mux`, not chi. For chi routes and middleware,
use `examples/uringhttp_chi` or `httpuring.Server{HTTPHandler: router}`.

On Linux only; the ring is io_uring-only.
