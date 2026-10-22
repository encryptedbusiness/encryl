# Wire protocols: `encryl.v2` and protobuf v1

The transport is RFC 6455 over plaintext `ws://`. The server supports two
encodings. The native 16-byte v2 envelope is selected by
`encryl.v2`. The protobuf protocol is selected by
`encryl.pb.v1`, `?format=protobuf`, or no protocol hint at all; a
connection that offers neither negotiates protobuf v1 by default.

The envelope adds framing, correlation, and versioning. It does **not** provide
confidentiality. TLS terminates at a load balancer or reverse proxy in front of
the process; the library never terminates, offloads, or negotiates TLS (see
`docs/adr/0013-tls-terminates-upstream.md`).

## Envelope

16 bytes, network byte order (big endian), followed by an opaque payload:

| Offset | Size | Field | Notes |
| ------ | ---- | ----- | ----- |
| 0 | 1 | `version` | `2` for this document |
| 1 | 1 | `kind` | see below |
| 2 | 2 | `flags` | reserved bits must be zero |
| 4 | 4 | `eventID` | numeric event, `0` for non-event kinds |
| 8 | 8 | `requestID` | `0` means unsolicited; otherwise correlates a response |
| 16 | … | `payload` | length = WebSocket message length − 16 |

A message shorter than 16 bytes is a protocol error and closes the connection
with `1002`.

### Kinds

| Value | Name | Direction | Purpose |
| ----- | ---- | --------- | ------- |
| 1 | `AUTH` | client → server | Authentication. Accepted as a no-op in M1; enforced in M2. |
| 2 | `EVENT` | both | Application event |
| 3 | `ACK` | server → client | Successful handling of a `requestID` |
| 4 | `ERROR` | server → client | Failed handling; payload carries a code and message |
| 5 | `CONTROL` | both | Rate limits, goaway, application-level keepalive |

Unknown kinds are a protocol error.

### Flags

| Bit | Mask | Name | Meaning |
| --- | ---- | ---- | ------- |
| 0 | `0x0001` | `RESPONSE` | This message is a response to the carried `requestID` |
| 1 | `0x0002` | `NO_REPLY` | Sender does not want an `ACK` |
| 2–15 | — | reserved | Must be zero; a non-zero reserved bit closes with `1002` |

### Payload grammars

**AUTH** payload:

| Offset | Size | Field |
| ------ | ---- | ----- |
| 0 | 1 | `scheme`: `1` bearer token, `2` proxy identity (reserved for M2) |
| 1 | … | scheme-specific bytes (bearer: opaque token, max 4096 bytes) |

The library does not terminate TLS, so it never sees a client certificate. A
proxy-supplied identity is an M2 design; in M1 the whole `AUTH` message is a
no-op.

**ERROR** payload:

| Offset | Size | Field |
| ------ | ---- | ----- |
| 0 | 2 | `code` — see the table below |
| 2 | … | UTF-8 message, no NUL requirement, max 1024 bytes |

| Code | Meaning |
| ---- | ------- |
| 1 | unauthenticated |
| 2 | unauthorized for this event |
| 3 | unknown event |
| 4 | payload too large |
| 5 | rate limited |
| 6 | backpressure (`Send` rejected) |
| 7 | internal error |

**ACK** payload:

The payload is opaque application bytes, from zero to `MaxMessageSize − 16`.
An empty ACK means the event was handled successfully. A non-empty ACK is a
reply body produced by `Conn.Reply`. The `requestID` echoes the request.

`ACK` is sent when a handler returns nil for a non-zero `requestID` and the
`NO_REPLY` flag is clear. A handler that calls `Conn.Reply` suppresses the
automatic empty ACK. A handler error sends `ERROR` with the same `requestID`
instead.

**CONTROL** payload:

| Offset | Size | Field |
| ------ | ---- | ----- |
| 0 | 1 | `subtype`: `1` keepalive, `2` goaway, `3` rate-limit notice |
| 1 | … | subtype-specific |

## Event IDs

- `0` is reserved and never dispatched.
- `1–1023` are reserved for protocol and server-generated events.
- `1024–2^32−1` are application event IDs.

Event IDs are compile-time constants emitted for Go and TypeScript from the same
registry (M2). The numeric ID is what goes on the wire; names are documentation.

There is no wire distinction between a unicast, a room send, and a broadcast.
All three produce one `EVENT` envelope per recipient. A connection's implicit
Self-Room is a server-side addressing concept only; it is never encoded.

If no handler is registered for an `EVENT` ID, the default behavior is an
`ERROR` with code `3` (unknown event) and the same `requestID`. A lifecycle
`OnMessage` callback, if registered, receives the event instead and the error
is not sent.

## Middleware outcomes

Per-event and per-connect middleware run on the owning loop. A middleware that
rejects an event returns an error; the peer receives `ERROR` with code `2`
(unauthorized) for `ErrUnauthorized`, `6` (backpressure) for
`ErrBackpressure`, `4` (payload too large) for `ErrPayloadTooLarge`, and `7`
(internal) otherwise. A connection
middleware that rejects runs after the 101 response and closes with `1008`.

## Golden vectors

Canonical vectors live in `testdata/vectors/` and are consumed by Go and
TypeScript tests. Three examples:

`EVENT`, ID 1024, request 0x0102030405060708, payload `"hi"`:

```
02 02 00 00 00 00 04 00 01 02 03 04 05 06 07 08 68 69
```

`AUTH`, bearer `"abc"`, request 1:

```
02 01 00 00 00 00 00 00 00 00 00 00 00 00 00 01 01 61 62 63
```

`ACK` for the first vector (same event and request, no payload):

```
02 03 00 02 00 00 04 00 01 02 03 04 05 06 07 08
```

## Upgrade

Requirements beyond RFC 6455:

| Header | Rule |
| ------ | ---- |
| `Sec-WebSocket-Version` | `13` |
| `Sec-WebSocket-Protocol` | `encryl.v2` for the native envelope, `encryl.pb.v1` for protobuf, or absent for the protobuf default |
| `Sec-WebSocket-Extensions` | Ignored; `permessage-deflate` is not negotiated |
| `Origin` | Must match `Config.AllowedOrigins`, or be absent for non-browser clients |
| Request headers | Bounded by `MaxHeaderBytes` (default 8 KiB) |
| `Host` | Required |

Missing or malformed requirements produce a 4xx response and no upgrade.

In M1, authentication is not enforced: every connection is treated as
pre-authenticated by the trusted proxy, `AUTH` is accepted as a no-op, and
The verifier authenticates the connect token. M2 adds an `AUTH` deadline, and
rejects events that arrive before authentication.

## WebSocket-level rules

- Client frames must be masked; an unmasked client frame is a protocol error.
- Control frames: FIN must be set, payload ≤ 125 bytes, never fragmented.
- Application data is binary-only. A text frame is rejected at the header with
  `1003`; there is no incremental UTF-8 path in M1.
- Fragmented messages are bounded by `MaxFragments` and `MaxMessageSize`.
- A close handshake uses the codes below; an abrupt close is `1006` internally
  and is never sent on the wire.
- Ping frames are answered with pong using the same payload.
- Server pings are sent every `PingInterval`; a peer that does not complete an
  idle connection within `IdleTimeout` is closed.

### Close codes

| Code | Sent by | Meaning |
| ---- | ------- | ------- |
| 1000 | either | Normal closure |
| 1001 | server | Going away, shutdown |
| 1002 | either | Protocol error |
| 1003 | server | Unsupported data (non-binary message) |
| 1008 | server | Policy violation (auth, Origin, rate limit) |
| 1009 | either | Message too big |
| 1011 | server | Internal error |
| 1013 | server | Try again later (slow consumer) |
| 4000–4999 | application | Application-defined |

## Versioning

`version` is the envelope version, not the wire-format magic. A server accepts
only `2` and closes with `1002` otherwise. Adding a kind or a flag is a
compatible change within version 2 only if receivers must ignore unknown values;
because `flags` has reserved bits, that is only true for new optional flags.
Anything that changes the 16-byte header is version 3.

---

## Protobuf protocol v1

This encoding is an alternative to the 16-byte envelope. It is inspired by
Centrifuge, but it is **not** wire-compatible with Centrifuge, Centrifugo, or
Grafana Live. See ADR-0018.

### Negotiation

| Client offer | Encoding |
| ------------ | -------- |
| `Sec-WebSocket-Protocol: encryl.pb.v1` | Protobuf v1; the 101 response echoes the subprotocol. |
| `GET /ws?format=protobuf` | Protobuf v1; no subprotocol is echoed. |
| No subprotocol and no `format` | Protobuf v1 default; no subprotocol is echoed. |
| `Sec-WebSocket-Protocol: encryl.v2` | The native 16-byte envelope. |
| `encryl.v2` plus `?format=protobuf` | Rejected with 400. |
| `?format=json` | Rejected with 400. JSON is not supported. |

### Framing

One WebSocket binary message contains one or more length-delimited protobuf
messages:

```text
varint length | message | varint length | message | ...
```

- Client to server: `Command` messages.
- Server to client: `Reply` messages. A `Reply` with `id = 0` carries a `Push`.
- One frame may mix replies and pushes.
- A truncated length, an oversized message, or an unknown field closes the
  connection with a protocol error.

### Messages

| Message | Purpose |
| ------- | ------- |
| `Command` | One request with an `id`: connect, subscribe, unsubscribe, publish, send, ack, or pong. |
| `Reply` | The result for the same `id`, an error, or a push when `id` is zero. |
| `Push.event` | An event delivered to a room: room name, event ID, payload. |
| `Push.message` | A Self-Room unicast event: event ID and payload. |
| `Push.join` / `Push.leave` | Room membership change. |
| `Push.disconnect` | The server is closing the connection. |
| `Push.request` | A server-initiated request; answer with `Command.ack`. |

The schema is in `proto/encryl/protocol/v1/protocol.proto`. The server
implements a hand-written codec for the hot path; generated code is for
external clients and as the test oracle.

### Event identity

- The hot path uses the numeric Event ID.
- `Config.EventNames` registers string names before start:
  `{"chat.message": 1024}`. `Server.On("chat.message", ...)`, `OnRequest`, and
  `Server.EventID` use the same registry.
- A `Publish` or `Send` may carry an event ID or a name. Names are resolved and
  interned once.
- `Config.ChannelEvents` maps a room to a default event. A publish that names no
  event uses the room mapping.

### Rooms and Self-Rooms

- `subscribe` joins a room and returns a per-session room handle.
- `publish` uses the handle (hot path) or a room name (first use).
- A room or broadcast delivery is `Push.event`.
- A Self-Room delivery is `Push.message`. It carries the event ID and payload.
- `unsubscribe` removes the membership. Closing the connection removes every
  membership.

### Replies and ACKs

- A client command gets a `Reply` with the same `id`. `Conn.Reply` returns an
  `AckResult` with the application payload.
- A server-initiated request (`Conn.Ask`, or numeric `EmitWithAckID`) sends
  `Push.request` with the
  pending-slab token. The client answers with `Command.ack`. The slab checks the
  connection identity and generation exactly as in the native path.
- A request that receives no ACK completes with `ErrAckTimeout`.

### Errors

`Reply.error` carries a small stable code: internal, unauthorized, unknown
room, permission, unknown event, already subscribed, limit exceeded,
backpressure, bad request, unsupported, not subscribed. A malformed command
closes the connection with 1002. A failed connect closes with 1008.

### Limits

- One message is at most 1 MiB.
- One frame is at most 4 MiB and 1024 messages.
- A payload is borrowed for the handler call. Do not retain it.

### Golden example

A `Command` with `id = 1`, a `send` body, event ID 1024, and payload `"hi"`:

```text
frame = varint(13) | 08 01 32 09 0a 03 08 80 08 12 02 68 69
```

The exact bytes are covered by the protocol tests in `internal/protocol`.

### Event IDs and names

The native envelope carries a 32-bit event ID. The public Go API uses a string
name and hashes it with FNV-1a:

```text
id = FNV-1a-32(name)
```

- `chat.message` → `0x5158bc5e`.
- `Server.On`/`OnRequest` register the name and handler by that ID.
- `Conn.Emit`/`To`/`ToRoom`/`Broadcast` hash the name once per send.
- The wire does not carry the event name; a client must use the same hash.
- The protobuf protocol can carry `EventRef.Name`; the server resolves it to the
  same ID through its registry. Pushes carry the numeric ID.
- No per-message string allocation or name lookup is required on the native
  fast path.

The raw numeric APIs (`OnID`, `OnRequestID`, `EmitID`) remain for
low-level clients and benchmarks. See `docs/events.md`.
