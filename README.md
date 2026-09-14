# Encryl

_Encryl_ — encrypted loop. A sync engine and WebSocket provider for very high
connection counts with bounded memory per connection, built for room-based and
event-based data transport.

Encryl is being built bottom-up. An idle connection will be a slab slot and a
file descriptor, not a goroutine, timer, or buffer. The transport parsers and
portable data structures come first, then the io_uring reactor and the public
server.

## Status

Skeleton. The module, license, and build files are in place; the runtime does not exist yet.

## What exists

Only the module, license, and build files.

## Shape

- One event loop per CPU. Connections belong to exactly one loop.
- A connection is a slab slot plus a file descriptor. No goroutine, timer, or
  buffer per idle connection.
- Every send goes through a room. A direct send is an emit to the connection's
  Self-Room.
- The write queue is bounded by messages and bytes. A full queue is an explicit
  error, not a silent drop.

## Development

```sh
go test ./...
```

The portable packages build on macOS; the runtime packages are Linux-only.

## License

MIT.
