# ADR-0023: Bebop is the only client event codec

Status: Accepted
Date: 2026-10-31
Deciders: sairash

> Historical note: the protobuf codec was removed in 1.1.0. This ADR records the
> comparison that led to that decision. Bebop is the only client codec.

## Context

The event protocol had one optional encoding: a hand-written protobuf subset
(ADR-0018). It is zero-allocation, but every decode still walks field tags and
varints, and every encode re-emits those tags. The hot paths are:

1. Decode each client command: publish, send, ack, subscribe.
2. Encode each room event or Self-Room push once, then deliver it to every
   recipient.
3. Copy the encoded frame to each recipient's write queue.

Bebop encodes values at fixed widths with no field tags, so decode is a
sequence of bounded reads. The question was whether that speed survives the
encryl message shape: small messages, short room names, 64-byte payloads, and
radiation to many recipients.

## Decision

Bebop is the only client event codec for a connection that does not offer the
native `encryl.v2` subprotocol. The protobuf codec was removed in 1.1.0.

The Bebop codec is hand-written and zero-allocation. It follows the Bebop wire
format for values (fixed-width little-endian integers, `uint32` lengths for
strings/bytes/arrays) and adds two encryl-specific layout rules:

- A one-byte presence bitmap per record, so zero-valued fields cost no bytes.
- A one-byte frame type on server messages: `0` reply, `2` push. Pushes do not
  carry the four-byte reply ID or the reply-kind byte.

These rules keep a 64-byte event push at 84 bytes, only 5% above protobuf's
80 bytes, while retaining fixed-offset reads.

## Measured result

Ubuntu 24.04 / kernel 6.8.0-147 / arm64 / Go 1.27.2, 8 vCPU Lima VM.
Command:

```sh
./scripts/linux-run.sh go test -run=NONE -bench=. -benchmem -benchtime=2s -count=3 ./bench/bebop
```

| Path | Protobuf | Bebop | Delta |
| ---- | -------: | ----: | ----: |
| Decode publish, 64B | 34.6 ns | **10.6 ns** | **3.3x faster** |
| Decode 16-message batch | 545 ns | **162.6 ns** | **3.35x faster** |
| Encode event push, 64B | 20.6 ns | **17.9 ns** | **15% faster** |
| Encode Self-Room push, 64B | 18.9 ns | **15.8 ns** | **19% faster** |
| Fan-out to 1000 recipients | 1673 ns | 1696 ns | 1.4% slower |
| Allocations | 0 | 0 | equal |

Wire sizes: publish +6.1%, send +3.9%, event push +5.0%, Self-Room push +2.7%.

The decode win is the load-bearing result: every client command pays it. The
encode win applies once per delivery. The fan-out copy difference is 1.4% at
1000 recipients in the userspace copy model; the server shares one frame per
encoding, so the real cost is the extra bytes in the kernel socket buffers.
That is the trade: 2.7-6.4% more bytes for 3.3x faster decode and 15-19% faster
encode.

## Consequences

- Clients must offer `encryl.bebop.v1`. A connection with no subprotocol
  negotiates Bebop.
- Clients that used the removed protobuf codec must implement Bebop.
- The presence bitmap is an encryl framing rule, not generated Bebop. External
  clients must implement the layout in `protocol/bebop.bop`; generated Bebop
  structs alone are not wire-compatible.
- The decoder is fuzzed (`FuzzDecodeBebopCommands`).
- The reactor broadcast path filters by `Slot.Encoding`; without that filter a
  mixed-encoding broadcast would send every frame to every connection.
