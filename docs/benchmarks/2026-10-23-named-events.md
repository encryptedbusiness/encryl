# Named events: hash cost and echo parity

Date: 2026-10-23
Commit: working tree on `v2` (named-event API added)
Author: Sairash

## Environment

| Field | Value |
| ----- | ----- |
| Host | Apple Silicon, Lima VM |
| Guest | Ubuntu 24.04, arm64 |
| Kernel | 6.8.0-147 |
| CPU | 8 vCPU |
| Memory | 15.6 GiB |
| Go | 1.27.2 |
| Commit | working tree on `v2` |
| Date | 2026-10-23 |
| Command | see below |
| Raw | console output in this report |
| Sysctls | not applicable (hash and echo benchmarks) |
| TLS mode | plaintext `ws://` (TLS terminates upstream) |

## Hash

The public API uses strings; the wire keeps the 16-byte envelope with a 32-bit
event ID. The ID is FNV-1a 32 of the name.

```sh
./scripts/linux-run.sh go test -run=NONE \
  -bench='BenchmarkEventIDForName|BenchmarkTopicEcho100k|BenchmarkStringEcho100k|BenchmarkServerEcho100k' \
  -benchtime=100000x -benchmem .
```

| Name | ns/op | B/op | allocs/op |
| ---- | ----: | ---: | --------: |
| `chat` | **1.13** | **0** | **0** |
| `chat.message` | **3.44** | **0** | **0** |
| `room:presence:update` | **5.26** | **0** | **0** |

The hash happens once per emit, not per recipient. A room or broadcast send
encodes one frame per encoding and shares it.

## End-to-end

`BenchmarkServerEcho100k` uses the numeric low-level API.
`BenchmarkStringEcho100k` uses the same server path with
`srv.On("echo", func(c, data) { c.Emit("echo", data) })`. The client is a raw
WebSocket loopback connection that sends one message and waits for one reply.
The per-op time is dominated by that loopback round trip; the allocation count
is the important part.

| Path | ns/op | B/op | allocs/op |
| ---- | ----: | ---: | --------: |
| Numeric echo | 26,993 | **0** | **0** |
| Named echo | **26,807** | **0** | **0** |
| Topic echo, no hash | **26,804** | **0** | **0** |

The named and Topic paths are within noise of the numeric path. `Topic` stores
the precomputed ID, so its handler and `TopicTarget` do not hash at all. The handler registry is
frozen at `ListenAndServe`: dispatch is one atomic load and one map lookup, with
no per-message lock. The payload is borrowed; the handler does not allocate.

## Wire size

The native frame is the existing 16-byte envelope plus the WebSocket header and
the application payload. The event field is 4 bytes; no event name, JSON quotes,
brackets, commas, or ack array is added. The named API does not add wire bytes.

## What this does not measure

- A 1M named-event fan-out. The fan-out reports use the native client; the
  emitter path is unchanged and encode-once.
- A JavaScript client. A client must reproduce FNV-1a and speak the native
  envelope; a small SDK is a follow-up.
- String payload codecs. The library never decodes payloads; the application
  owns that cost.
- Collision probability. The tests cover the fixed hash vectors; registration
  rejects a name mapped to a different ID.

## Sources

The API shape follows v1, Socket.IO, NATS, Phoenix Channels, SignalR,
Centrifuge, and uWebSockets.js. The decision record is
[`docs/adr/0020-named-event-api.md`](../adr/0020-named-event-api.md); the public
guide is [`docs/events.md`](../events.md).
