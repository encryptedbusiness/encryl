# 0003 — A 16-byte binary envelope inside WebSocket messages

Status: Accepted
Date: 2026-10-22
Deciders: Sairash

## Context

v1 sends JSON `{event, data}` objects and looks the event up in a
`map[string]HandlerFunc`. That costs a JSON parse, a string allocation, and a
map lookup per message, and it makes the event name the routing key. At high
message rates those three costs dominate dispatch.

v2 needs: numeric routing, request/response correlation, a place for protocol
control messages, and a version field that cannot be confused with the
WebSocket protocol version.

## Decision

Exactly one envelope per WebSocket binary message:

```
version:u8 | kind:u8 | flags:u16 | eventID:u32 | requestID:u64 | payload
```

- 16 bytes, network byte order, fixed offsets. Parsing is two unaligned loads
  and a bounds check.
- `version = 2`. Any other value closes the connection with `1002`.
- `kind` covers `AUTH`, `EVENT`, `ACK`, `ERROR`, `CONTROL`.
- `flags` has reserved bits that must be zero, leaving room for compatible
  additions without a version bump.
- `eventID` is numeric; names exist only as generated constants and docs.
- `requestID` correlates responses; zero means unsolicited.

Text frames are not used for application data. Binary-only removes the UTF-8
validation path from the hot path (text is still parsed and validated if a
client sends it, then rejected with `1003`).

Golden vectors are versioned in `testdata/vectors/` and shared by the Go and
TypeScript clients.

## Consequences

- Dispatch is a numeric table lookup, with no allocation and no string hashing.
- Clients must be updated; the v1 JSON envelope is gone. This is part of the
  breaking major version.
- 16 bytes per message is overhead for tiny payloads. At 64 B payloads that is
  25%; it buys correlation and versioning. Applications sending very small
  messages frequently should batch at the application layer.
- The envelope does not compress. `permessage-deflate` stays disabled because
  it costs memory per connection and introduces compression-oracle risk.

## Alternatives rejected

- **Keep JSON.** Per-message parse and allocation dominate; event names become
  an implicit schema with no compile-time checking.
- **MessagePack/CBOR.** Still requires a schema-less decode and allocation;
  numeric IDs in a fixed header are strictly cheaper.
- **Protobuf/gRPC-style length-delimited frames.** Adds a dependency and a
  codegen step for what are opaque payloads; the envelope only needs routing.
- **Put the envelope in a text frame.** Forces UTF-8 validation and prevents
  embedding arbitrary bytes; binary is the correct WebSocket type.
- **No envelope, one event per subprotocol.** Every event would need a
  negotiation. Unworkable for dynamic application events.
