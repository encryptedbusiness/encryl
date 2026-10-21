# io_uring HTTP server example

This is the io_uring analogue of the `go-uring` HTTP-server example. It runs a
normal `http.Server` and `http.ServeMux` on top of `httpuring.Listen`, a
`net.Listener` whose accept/read/write operations are serviced by one
io_uring ring.

```sh
go run ./examples/uringhttp
# http://127.0.0.1:18094/ping
# http://127.0.0.1:18094/post
# http://127.0.0.1:18094/get-file
# http://127.0.0.1:18094/file-info
```

Use `-no-uring` to run the exact same mux on a standard `net.Listener`:

```sh
go run ./examples/uringhttp -no-uring
```

Routes:

| Method | Path | Behavior |
| ------ | ---- | -------- |
| `GET` | `/ping` | Returns `pong`. |
| `POST` | `/post` | Echoes the request body. |
| `GET` | `/get-file` | Returns the embedded `gopher.txt`. |
| `POST` | `/file-info` | Parses a multipart upload and reports its metadata. |
| `GET` | `/` | Usage text. |

## How it works

`httpuring.Listen` binds the socket with `netio.Listen` and starts a
single-owner io_uring loop. The loop submits `IORING_OP_ACCEPT`; accepted
descriptors are returned from `Accept()`. A blocking `Read` or `Write` from
`net/http` becomes a request to the loop, which submits `IORING_OP_RECV` or
`IORING_OP_SEND` and completes the caller when the CQE arrives.

`SetReadDeadline` and `SetWriteDeadline` cancel the matching pending operation
with `IORING_OP_ASYNC_CANCEL`. This is what lets `net/http` abort a background
read during keep-alive shutdown without closing a healthy connection.

On Linux only. The runtime is io_uring-only.
