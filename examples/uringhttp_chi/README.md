# chi on io_uring

This example mounts a chi router, route groups, URL parameters, and built-in
middleware on `httpuring.Server`'s ring-native `http.Handler` adapter.

```sh
cd examples/uringhttp_chi
go run .
# http://127.0.0.1:18095/healthz
# http://127.0.0.1:18095/api/ping
# http://127.0.0.1:18095/api/items/42
# curl -u user:pass http://127.0.0.1:18095/api/secure
```

`-workers=0` (default) runs chi inline in the ring loop, which is fastest for
small handlers. `-workers=1` sends requests through the request/response ring
to a worker goroutine, which is useful when handlers block on a database or
external service.

Use `-bridge` to run the same router through `net/http` + the io_uring
`net.Listener`, or `-no-uring` for a standard listener:

```sh
go run . -bridge
go run . -no-uring
```

Routes:

| Method | Path | Behavior |
| ------ | ---- | -------- |
| `GET` | `/healthz` | Liveness. |
| `GET` | `/api/ping` | JSON ping. |
| `GET` | `/api/items/{id}` | Returns the chi URL parameter. |
| `POST` | `/api/echo` | JSON-wrapped request body. |
| `GET` | `/api/private` | Demonstrates a nested `chi.Group` middleware. |
| `GET` | `/api/secure` | Built-in `middleware.BasicAuth`; use `user:pass`. |

The router uses `RequestID`, `RealIP`, `Logger`, `Recoverer`, `Timeout`, and
`BasicAuth` middleware plus a custom response-header middleware. The HTTP
handler is unchanged: it is a normal `http.Handler`, so chi, Echo, Gin, or
`http.ServeMux` can use the same adapter.

This is an isolated example module so chi is not a dependency of the core
encryl module.
