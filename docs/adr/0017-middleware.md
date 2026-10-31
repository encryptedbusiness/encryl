# 0017 — Per-event and per-connection middleware

Status: Accepted
Date: 2026-10-23
Deciders: sairash

## Context

socket.io has `io.use` for connection middleware and per-namespace middleware
for packet middleware. encryl needs a hook for authentication,
authorization, rate limiting, logging, and per-connection admission without
putting those concerns inside every handler or inside the reactor.

## Decision

Two chains, both inline on the owning loop:

```go
srv.UseConnect(func(c *Conn) error)   // after the upgrade, before OnOpen
srv.Use(func(next EventHandler) EventHandler) error // per event
```

### Per-event chain

`Use` appends a `Middleware[*Conn]`. The registry composes the chain **when a
handler is registered**, not on every event:

```go
srv.Use(mw)
srv.On("chat", handler) // stored as mw(handler); OnID for numeric IDs
```

Therefore `Use` must be called before the first `On`; calling it later returns
an error. The chain is a fixed slice, registration order is execution order,
and dispatch does not allocate.

A handler error sends an `ERROR` envelope. A middleware can reject by returning
an error: `ErrUnauthorized` maps to code 2, `ErrBackpressure` to 6,
`ErrPayloadTooLarge` to 4, and an arbitrary error to 7.

### Connection chain

`UseConnect` appends a `ConnectMiddleware`. The chain runs after the 101
response has been sent and the connection has entered `Open`, before the
lifecycle `OnOpen`. It is the natural place to `Join` a room, inspect the peer,
or reject an admission. A reject closes the connection with `1008`.

### Non-blocking contract

Both chains run on the connection's loop. A blocking middleware stalls every
connection on that core. This is the same contract as an event handler, and it
is documented on both types. The M2 bounded worker pool is the sanctioned path
for work that may block.

## Consequences

- Authorization and rate limiting live outside handlers and are applied in one
  place.
- Composition happens once per handler, so dispatch stays allocation-free
  (`BenchmarkRegistryDispatch`: 5.5 ns, 0 allocs).
- A middleware reject is observable by the peer as a protocol error, not a
  silent drop.
- Middleware is not hot-reloadable. Register it before `ListenAndServe`; the
  registry rejects `Use` after the first handler.

## Rejected alternatives

- **Compose per event at dispatch time.** It allocates or needs a precomputed
  cache, and it makes registration order ambiguous.
- **Run middleware in the reactor.** The reactor is a mechanism; application
  policy does not belong on the completion path.
- **A goroutine per middleware.** It breaks the no-per-connection-goroutine
  rule and makes ordering nondeterministic.
