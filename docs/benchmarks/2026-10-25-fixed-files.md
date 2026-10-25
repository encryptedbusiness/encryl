# Fixed-file tables: 500k idle connections

Date: 2026-10-25
Commit: working tree on `v2` (`5f894d8` plus the fixed-file changes)
Author: Sairash

## Environment

| Field | Value |
| ----- | ----- |
| Host | `lima-encryl-dev`, Apple Silicon, 8 vCPU |
| Kernel | 6.8.0-147-generic |
| Memory | 15.6 GiB |
| Go | 1.27.2 linux/arm64 |
| Sysctls | `somaxconn=65535`, `tcp_max_syn_backlog=65535`, `ip_local_port_range=1024-65535` |
| TLS mode | plaintext `ws://` (TLS terminates upstream) |
| Raw | console output in this report |

## Change measured

The reactor now gives each loop a sparse registered-file table. The accept path
keeps the process descriptor only long enough to read the peer and install the
socket in the table. It closes the process descriptor and uses the direct index
for poll, receive, send, and close.

The ring setup now uses `SINGLE_ISSUER`, `COOP_TASKRUN`, `DEFER_TASKRUN`, and a
larger CQ. The restriction set allows fixed files and file-table updates.

## 500k idle connections

Command:

```sh
CONNS=500000 RATE=20000 DURATION=40s SRC_IPS=40 ./scripts/scale.sh
```

Result at the 33-second sample:

| Metric | Value |
| ------ | ----- |
| Connections accepted | 500,000 |
| Connections open | 500,000 |
| Connections closed | 0 |
| Process file descriptors | **28** |
| Server RSS | 296,660 KiB |
| RSS per connection | **0.53 KiB** |
| File-table errors | 0 |
| Protocol / recv / send / illegal errors | 0 / 0 / 0 / 0 |
| CQ overflow / buffer exhausted | 0 / 0 |

The process fd count is the main result. The 500,000 sockets do not occupy
500,000 process file descriptors. The registered tables hold them.

The 100,000-connection run used 0.49 KiB per connection:

```sh
CONNS=100000 RATE=10000 DURATION=30s SRC_IPS=8 ./scripts/scale.sh
```

## Close path

A 10,000-connection idle run completed, then all clients closed:

| Metric | Value |
| ------ | ----- |
| Accepted | 10,000 |
| Closed | 10,000 |
| Open | 0 |
| Close submitted / completed | 10,000 / 10,000 |
| Close errors | 0 |
| File-table errors | 0 |

This shows that direct close frees the table slot and the connection record.

## Message path

Fixed-rate echo, 1,000 connections, 256-byte payload, ten messages per second
per connection:

```sh
./scripts/qualify-rate.sh
```

| Metric | Value |
| ------ | ----- |
| Messages received | 97,001 |
| Failed | 0 |
| Message rate | **9,700/s** |
| p99 | below the 1 ms histogram resolution |
| p999 | below the 1 ms histogram resolution |

This is a fixed offered rate, not a maximum-throughput claim. The hot-path
allocation gate also passes:

```sh
make qualify-messages
```

All selected hot paths report **0 B/op and 0 allocs/op**. The documented
`BenchmarkRawEcho` exception remains 3-4 B/op and 0 allocs/op; the
small difference is one-time reactor setup amortized over the benchmark.

## What is not measured

- A 500k echo load. The 500k run was idle.
- Multishot accept and multishot receive. The current path uses normal accept
  plus file-table registration and one poll plus one receive per connection.
- Direct accept without a process descriptor. The current path keeps the peer
  address and uses a short-lived process descriptor.
- A 60-minute hold. The 500k run held for 40 seconds.
- A separate-client network topology. The run used loopback.
- Full runtime allocation sampling during the 500k ramp. The allocation gate
  covers the hot path and the fixed-rate message path, not the ramp.

## Why this matters

The old connection ceiling was the process file table. A 1M connection setting
needed a 1M `RLIMIT_NOFILE`. The fixed-file table removes that dependency.

The process held **500,000 sockets with 28 file descriptors**. This is the
network design that makes a small-host connection target possible.

The next measured steps are multishot accept, multishot receive with provided
buffers, and a 60-minute hold.

## Follow-up: idle-handle elision and the 0.4 KiB gate

The first fixed-file run measured **0.53 KiB/connection**. The connection
record was trimmed without changing the wire path:

- A native connection with no open or close lifecycle handler has no public
  `Conn` pointer until the first message. The reactor keeps the peer, encoding,
  and admission state in the slot.
- The admission gate `Accounting` is allocated on the first message or first
  send, not at accept.
- The handshake parser, queue head/tail, and admission pointer live in the
  optional `connExtra` block. It is freed after the upgrade and allocated again
  only when a message needs it.
- `Slot` is now **160 bytes**, down from **192 bytes**. The size test fails above
  160 bytes.
- Per-loop defaults are now 1024 / 256 / 1 MiB + 64. The idle run does not read
  or write application data, so these are fixed arena bounds, not per-connection
  buffers.

Command:

```sh
CONNS=500000 SHARDS=1 SRC_IPS=41 RATE=20000 HOLD=10 SAMPLE=10 \
  RAMP_TIMEOUT=120 ./scripts/memory.sh
```

Result:

| Metric | Value |
| ------ | ----- |
| Connections accepted | 500,000 |
| Connections open at hold end | 500,000 |
| Process file descriptors | 30 |
| Baseline RSS | 33,520 KiB |
| Ramp-end RSS | 161,332 KiB |
| Steady delta | **0.256 KiB/connection** |
| Limit | **0.400 KiB/connection** |
| File-table / protocol errors | 0 / 0 |
| FD leak | 0 |

A separate 500k sample reported 185,396 KiB total RSS, or **0.371 KiB/
connection**. `make gate-a` now exits non-zero when the steady delta exceeds
`MAX_PER_CONN` (default `0.40`).
