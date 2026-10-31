# Historical protobuf protocol v1: codec and end-to-end

> Historical. The protobuf codec was removed in 1.1.0. This report documents the
> removed codec and is not a supported protocol.

Date: 2026-10-23
Commit: working tree (protocol slice added)
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
| Commit | working tree |
| Date | 2026-10-23 |
| Command | see Codec, End-to-end, and Fuzz below |
| Raw | console output in this report |
| Sysctls | not applicable (protocol microbenchmarks) |
| TLS mode | plaintext `ws://` (TLS terminates upstream) |
| Reference | `github.com/centrifugal/protocol` at `HEAD` (same VM, same Go) |

The reference schema is **not** the same schema. It is larger: recovery,
presence, history, JSON, and delta fields. The comparison below is a codec
comparison, not a wire-compatibility claim.

## Codec

Command:

```sh
./scripts/linux-run.sh go test -run=NONE -bench=. -benchmem ./internal/protocol
```

| Benchmark | ns/op | B/op | allocs/op |
| --------- | ----: | ---: | --------: |
| Decode publish, 64B payload | **40.0** | **0** | **0** |
| Decode 16-message batch | **746.6** | **0** | **0** |
| Encode event push, 64B | **36.2** | **0** | **0** |
| Encode Self-Room message | **25.6** | **0** | **0** |

The decoder aliases the payload, room, and event name to the receive buffer.
The encoder writes into a pooled `connection.Frame`; the benchmark reuses a
preallocated destination, which is what the server does.

Reference, same VM:

```sh
git clone --depth 1 https://github.com/centrifugal/protocol /tmp/cfproto
cd /tmp/cfproto && go test -run=NONE -bench=. -benchmem
```

| Reference benchmark | ns/op | B/op | allocs/op |
| ------------------- | ----: | ---: | --------: |
| `EncodePush/protobuf/publication_64B` | 29.7 | 96 | 1 |
| `EncodePush/protobuf/publication_256B` | 53.2 | 288 | 1 |
| `DecodeReplies/protobuf/connect` | 103.1 | 421 | 5 |
| `DecodeReplies/protobuf/8_publications_256B` | 2499 | 11040 | 105 |
| `DecodeStream/protobuf/64_publishes_256B` | 7432 | 33806 | 256 |

The reference `EncodePush` allocates its output buffer per call (96 B for 64 B
payload). The encryl path encodes into a pooled frame (0 B). The reference
decoder materializes its reply structs and strings; the encryl decoder
aliases and is allocation-free. Per publication, the reference's 64-publish
stream is about 116 ns; the encryl batch is about 47 ns per message.

This is the expected result for a narrow, fixed schema with a hand-written
codec. It is not a general-purpose protocol implementation.

## End-to-end

The benchmark opens one raw WebSocket connection, negotiates
`encryl.pb.v1`, sends a framed `Command.send`, and reads the server's
`Push.message` for every message. It measures the full path: frame decode,
command dispatch, handler, Self-Room push encode, write queue, io_uring send,
and the client read.

```sh
./scripts/linux-run.sh go test -run=NONE \
  -bench='BenchmarkProtoSendEcho100k|BenchmarkServerEcho100k' \
  -benchtime=100000x -benchmem .
```

| Path | ns/op | B/op | allocs/op |
| ---- | ----: | ---: | --------: |
| Native v2 echo | 26,853 | **0** | **0** |
| Protobuf v1 echo | 27,116 | **0** | **0** |

The two paths are within 1 percent of each other. This is a single-connection
request/response loop, so the number is dominated by the loopback round trip,
not throughput. It is evidence that the protobuf edge codec did not regress the
public path and that it is allocation-free.

## Fuzz

```sh
./scripts/linux-run.sh go test -run=NONE \
  -fuzz=FuzzDecodeCommands -fuzztime=10s ./internal/protocol
```

5,251,618 executions in 10 seconds, 412 interesting inputs, no panic. The
decoder checks every length and every field boundary. Unknown fields are
skipped.

## What is not measured

- **Centrifuge and Centrifugo server throughput.** A running reference server on
  the same host was not benchmarked. The measured parity claim is codec-level
  and native-vs-protobuf end-to-end on this server.
- **Write batching.** The codec accepts batches; the write path still sends one
  frame per completion. Centrifuge batches replies into one write. This is the
  next optimization.
- **SSE and HTTP-stream transports.** `protocol.Transport` exists; only
  WebSocket is implemented.
- **Fan-out at 100k members with protobuf clients.** The existing fan-out
  reports use native clients. The emitter partitions by encoding; the
  per-encoding encode-once path is covered by unit tests, not by a 100k run.
- **Control messages.** Connect, subscribe, name resolution, and token checks
  allocate. They are not on the message hot path.

## Reproduce

The commands above are the complete reproduction. The VM is built by
`deploy/lima.yaml`; `./scripts/linux-run.sh` runs a command in the guest. The
protocol tests run in the default suite; the end-to-end test file is
`protocol_api_linux_test.go`.
