# Chat example

A runnable browser chat: one Go server, one embedded HTML/JS client, rooms,
named events, and a request/reply round trip. A single `net/http` listener
serves the UI and upgrades `/ws` into encryl.

```sh
go run ./examples/chat
# open http://127.0.0.1:18082
```

On Linux only; the server is io_uring-only.

Flags:

| Flag | Default | Purpose |
| ---- | ------- | ------- |
| `-addr` | `127.0.0.1:18082` | HTTP and WebSocket listener |
| `-ws-url` | `ws://127.0.0.1:18082/ws` | WebSocket URL injected into the page |
| `-loops` | `NumCPU` | io_uring loops |

If you open the page from another machine, set `-ws-url` to the address the
browser can reach.

## Events

All payloads are JSON bytes. The library passes them through; the handlers
decode them.

| Event | Payload | Behavior |
| ----- | ------- | -------- |
| `chat.join` | `{"room":"..."}` | Joins a room; empty means `lobby`. The connection already joins `lobby` on connect. |
| `chat.say` | `{"room":"...","text":"..."}` | Broadcast to the room, excluding the sender. The sender's UI appends locally. |
| `chat.ask` | `{"question":"..."}` | Request/reply; returns `{"question":"...","at":nanos}` in the acknowledgement. |

## Why this example uses `Topic`

`chat.join`, `chat.say`, and `chat.ask` are created once with
`srv.Topic("…")`. Their IDs are precomputed, and every send uses
`Topic.Emit`/`Topic.OnRequest`/`TopicTarget.Emit` with the numeric ID. There is
no event-name hash on the message path.

## HTTP routes

| Method | Path | Behavior |
| ------ | ---- | -------- |
| `GET` | `/` | Embedded chat UI. |
| `GET` | `/app.js` | Browser client that speaks the native envelope. |
| `GET` | `/healthz` | Liveness check. |
| `GET` | `/ws` | WebSocket upgrade handed to the reactor. |

The browser client is deliberately small because it has to speak the native
wire:

- WebSocket subprotocol `encryl.v2`;
- 16-byte big-endian envelope: version, kind, flags, event ID, request ID;
- event ID = FNV-1a 32 of the name;
- JSON payload after the header;
- `KIND_EVENT=2`, `KIND_ACK=3`, `KIND_ERROR=4`.

That is the same contract as `EventIDForName` in the Go API. A production
client SDK would add reconnect state, heartbeats, binary attachment handling,
and typed payload helpers; this one keeps the wire visible.
