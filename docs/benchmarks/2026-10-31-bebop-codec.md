# Bebop codec: performance and fan-out

> Historical comparison. The protobuf codec was removed in 1.1.0; Bebop is the
> only client codec.

Date: 2026-10-31
Commit: working tree (1.1.0)
Author: sairash

## Environment

| Item | Value |
| ---- | ----- |
| Host | Apple Silicon, Lima VM |
| Guest | Ubuntu 24.04, arm64 |
| Kernel | 6.8.0-147 |
| CPU | 8 vCPU |
| Memory | 15.6 GiB |
| Go | 1.27.2 |
| Commit | working tree (1.1.0) |
| Date | 2026-10-31 |
| Command | `./scripts/linux-run.sh go test -run=NONE -bench=. -benchmem -benchtime=2s -count=3 ./bench/bebop` |
| Raw | console output in this report |
| TLS mode | plaintext `ws://` (TLS terminates upstream) |

The comparison uses the same semantic `protocol.Command` and `protocol.Push`
values for both codecs. Frames are varint-length-prefixed in both cases, so
only the message body differs. Both codecs are hand-written and reuse a
preallocated destination, which is what the server does.

## Codec

| Benchmark | Protobuf ns/op | Bebop ns/op | B/op | allocs/op |
| --------- | -------------: | ----------: | ---: | --------: |
| Decode publish, 64B payload | 34.6 | **10.6** | 0 | 0 |
| Decode 16-message batch | 545 | **162.6** | 0 | 0 |
| Encode event push, 64B | 20.6 | **17.9** | 0 | 0 |
| Encode Self-Room push, 64B | 18.9 | **15.8** | 0 | 0 |

Decode is **3.3x** faster for one command and **3.35x** faster for a 16-command
batch. Event-push encode is **15%** faster and Self-Room encode is **19%**
faster. Both codecs allocate nothing.

## Wire size

| Frame | Protobuf | Bebop | Delta |
| ----- | -------: | ----: | ----: |
| Publish by room name + event ID | 82 | 87 | +6.1% |
| Publish by room ID + event ID | 78 | 83 | +6.4% |
| Send by event ID | 76 | 79 | +3.9% |
| Event push by room name | 80 | 84 | +5.0% |
| Event push by room ID | 76 | 80 | +5.3% |
| Self-Room push | 74 | 76 | +2.7% |

Bebop pays a four-byte length for every string and byte field. Protobuf pays a
tag and a varint length. For a 64-byte payload the varint is one byte, so the
fixed-width length costs three extra bytes per variable field. The presence
bitmap removes absent fields; without it the gap is 18-37% instead of 2.7-6.4%.

## Fan-out

One encode per broadcast, then a copy of the frame to each recipient's write
queue:

| Recipients | Protobuf ns/op | Bebop ns/op | Delta |
| ---------: | -------------: | ----------: | ----: |
| 1000 | 1673 | 1696 | 1.4% slower |

The server shares one encoded frame per encoding; the copy model approximates
the per-recipient kernel write. Bebop's 5% larger event push costs 1.4% at 1000
recipients, less than the encode saving. The real 1M-recipient fan-out will
convert the extra bytes into extra kernel copies; the trade was accepted because
command decode is 3.3x faster and pushes are encoded 15-19% faster.

## Decision

Bebop became the only client encoding in 1.1.0. The presence bitmap keeps the
size penalty small, and the decode win applies to every client command. The
protobuf codec was removed. See `docs/adr/0023-bebop-default.md`.

## Reproduce

```sh
make limactl-up
./scripts/linux-run.sh go test -run=NONE -bench=. -benchmem -benchtime=2s -count=3 ./bench/bebop
./scripts/linux-run.sh go test -run=NONE -fuzz=FuzzDecodeBebopCommands -fuzztime=60s ./protocol
```
