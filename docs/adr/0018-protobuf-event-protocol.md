# 0018 — A protobuf event protocol

Status: Superseded
Date: 2026-10-23
Deciders: sairash
Related: ADR-0003 (wire format), ADR-0014 (vertical slices), ADR-0015
(Self-Rooms)

> Historical record. ADR-0023 replaced this codec with Bebop, and the protobuf
> codec was removed in 1.1.0. This ADR is retained as decision history only.

## Context

The native v2 envelope is compact and allocation-free: a 16-byte header with a
numeric event ID and an opaque payload. It is fast, but it is also a local
format. There is no schema, no command vocabulary, and no way for a generated
client or a Grafana-style channel client to express subscriptions, requests, or
acknowledgements.

Centrifuge, the library under Grafana Live and Centrifugo, uses a protobuf
protocol with commands, replies, pushes, channel subscriptions, and batched
frames. Its transport adapter layer lets one session core serve WebSocket, SSE,
HTTP-stream, and unidirectional clients. Its hot path is shaped around batched
writes, encode-once publications, pooled buffers, and a zero-copy JSON decoder.

We want the ergonomics and future-proofing of a schema-based protocol without
abandoning the event-based core. The core rules stay: numeric Event IDs, the
event registry, rooms, Self-Rooms, the bounded write queue, and the io_uring
reactor. The new protocol is an edge codec, not a replacement for the core.

## Decision

### 1. A protobuf protocol, inspired by Centrifuge

The protocol is a small protobuf subset. It is **not wire-compatible** with
Centrifuge, Centrifugo, or Grafana Live. It borrows the useful shape:

- a WebSocket binary message carries one or more messages, each prefixed with a
  varint length;
- the client sends `Command` messages; the server sends `Reply` messages;
- a `Reply` with `id = 0` carries an asynchronous `Push`;
- command IDs correlate replies; a connect command opens the session;
- subscriptions are multiplexed over one connection.

### 2. Event identity is numeric on the hot path, named at the edge

- `Event` is a `uint32`; the hot path never carries a string.
- A name registry maps string names to IDs. Names are registered before traffic
  with `Config.EventNames`, `Server.RegisterEvent`, or the generated constants.
- `Server.On`/`OnRequest` and `Server.EventID` are the string-facing API.
- A `Publish` or `Send` may carry either an event ID or an event name. A name is
  resolved and interned once; the fan-out uses the ID.
- A channel may map to a default event with `Config.ChannelEvents`; this lets a
  channel-oriented client publish without naming an event.

### 3. Rooms, Self-Rooms, and ACKs

- `subscribe` joins a room and returns a per-session room handle. Later
  `publish` and pushes use the handle, which keeps strings out of the hot path.
- A room or broadcast send becomes a `Push.event` with the room name and the
  numeric event ID.
- A Self-Room or unicast send becomes a `Push.message` with the numeric event ID
  and no room.
- A client command gets a `Reply` with the same command ID. `Conn.Reply` returns
  an `AckResult` with the application payload.
- A server-initiated request (`Conn.Ask`, or numeric `EmitWithAckID`) becomes
  `Push.request` with the
  pending-slab token; the client answers with `Command.ack`. The identity and
  generation checks of the slab are unchanged.

### 4. A new vertical slice

`protocol` owns the codec, framing, session state, event-name registry,
and the transport seam. The facade gains `protocol_dispatch.go` for command
handling. The reactor gains one encoding byte on the slot and passes it to
`OnOpen`; it has no protocol logic.

The native v2 envelope stays behind the same seam as `EncodingNative` for one
release. Connections that offer `encryl.pb.v1` (or `?format=protobuf`) use the
protobuf codec. Connections that offer `encryl.v2` keep the native path. A
connection that offers neither now negotiates Bebop by default; see ADR-0023.

### 5. Zero allocations on the hot path

- The hot decoder parses fields by hand and aliases the payload, room, and event
  name to the receive buffer.
- The hot encoder writes into a pooled `connection.Frame`; it does not allocate
  per recipient.
- The fan-out encodes once per encoding. Native, Bebop, and protobuf recipients
  get one frame each per send, never one per recipient.
- The session, decoder scratch, and frame come from pools.
- Control messages (connect, subscribe, names, tokens) may allocate. They are
  not on the message hot path.
- A room frame converts the room name to bytes once per frame. Self-Room and
  unicast frames do not. No path allocates per recipient.

Measured on the 8 vCPU Linux VM: decode publish 40 ns, encode event push 36 ns,
end-to-end protobuf echo 27.2 µs, all at **0 B/op and 0 allocs/op**.

### 6. Multi-client seam

`protocol.Transport` is the adapter interface: `Write`, `WriteMany`, `Close`,
`Encoding`, `Unidirectional`, `DisabledPushFlags`, and `PingPong`. WebSocket is
the first adapter. SSE and HTTP-stream can implement the same interface later
without touching the event core.

## Consequences

- A second wire format exists. It is the default for a connection that does not
  offer `encryl.v2`; the native envelope remains for explicit clients.
- The protocol is not compatible with Centrifuge or Grafana clients. A
  translation gateway or a compatibility codec would be required for that.
- The slot grows by one byte for the encoding. A protobuf session is allocated
  from a pool only for protobuf connections.
- Event names are compile-time constants or registered before start. A name that
  is not registered fails with a protocol error.
- Protobuf `bytes` fields are not copied on decode; handlers must not retain the
  payload after they return, which is already the contract.
- Batching multiple replies into one write and SSE/HTTP-stream transports are
  follow-up work.

## Alternatives rejected

- **Use Centrifuge's schema and codecs unchanged.** It brings JSON, recovery,
  presence, history, delta compression, and a large surface we do not need. It
  also makes the event core a second-class concept. Adopt the shape, not the
  wire.
- **JSON first.** JSON parsing and string keys are slower and allocate. The
  protocol is protobuf-only; a JSON codec can implement the same session later.
- **Official `google.golang.org/protobuf` on the hot path.** It allocates for
  bytes and strings. Use it as the test oracle, not the runtime path.
- **Strings as the only event identity.** A string lookup on every publish
  costs more than a numeric ID. Names stay at registration and control.
- **Replace the native envelope immediately.** It would break every existing
  client and test in one step. Keep it as a compatibility codec and remove it
  after a migration period.
