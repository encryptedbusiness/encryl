# 0010 — The public API is handles, interfaces, and value types

Status: Accepted
Date: 2026-10-22
Deciders: Sairash

## Context

v1's public surface leaks implementation: a `chan []byte` per subscriber, a
`sync.Map` for metadata, a `sync.Map` of rooms, mutable `Config` fields with no
validation, and an `On map[string]HandlerFunc` registry. Every one of those is a
compatibility promise the server cannot afford, and several are the direct cause
of v1's memory behavior.

## Decision

Public, in package `encryl`:

| Type | Role |
| ---- | ---- |
| `Server` | Owns listeners, loops, and state. Created by `New`. |
| `Config` | Validated configuration. `DefaultConfig` returns a usable value. |
| `Handler` | Lifecycle callbacks: `OnOpen`, `OnMessage`, `OnClose`. |
| `EventHandler` | Per-event callback. |
| `Conn` | Opaque handle `{server, slot, gen}` with `Send`, `Join`, `Leave`, `Close`, `ID`, `RemoteAddr`. |
| `ConnID` | `{Slot, Gen}`. Identifies a connection; never authorizes anything. |
| `Message` | `Event`, `RequestID`, `Payload` (borrowed), `Binary`. |
| `CloseInfo` | Code, reason, whether the peer initiated. |
| `Stats` | Counters and gauges. |
| `ConfigError`, sentinel errors | `errors.Is`-compatible values. |

Operations: `New`, `ListenAndServe`, `On`, `OnRequest`, `OnConnect`,
`OnDisconnect`, `OnID`, `Emit`, `To`, `Shutdown`, `Stats`, `Config`; and on
`Conn`: `ID`, `Emit`, `To`, `ToRoom`, `Broadcast`, `Join`, `Leave`, `Close`,
`RemoteAddr`.

Explicitly **not** public:

- No maps, channels, slices of connections, or file descriptors.
- No `*Subscriber`-style pointer that outlives its connection.
- No mutable connection state or room membership.
- No backend type, loop, ring, or buffer type. io_uring is the only backend
  (ADR-0012) and TLS is terminated upstream (ADR-0013).
- No metadata `sync.Map`; application state is keyed by `ConnID` in an
  application-owned store.

## Consequences

- The API can evolve internally without breaking callers.
- Applications cannot inspect room membership; they use `Emit` and counters.
- `Send` returns an error instead of a `bool`, which is a breaking change but
  removes an ambiguity v1 could not express.
- `New` validates and normalizes, so misconfiguration fails at startup rather
  than at the first connection. It does not accept TLS configuration: the
  library speaks plaintext `ws://` and expects a TLS-terminating proxy.

## Alternatives rejected

- **Keep v1's surface and add methods.** Preserves the leaks and forces the
  implementation to keep channels and maps per connection.
- **Expose an interface for `Conn`.** Interface values allocate on the hot path
  and hide the generation check from the compiler.
- **Expose room membership as a slice.** Tempts callers into per-recipient
  sends, which is exactly the fan-out anti-pattern.
- **Option-struct configuration.** No compile-time validation and no single
  place to document defaults; `Config.Validate` reports every problem at once.
