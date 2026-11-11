# Operations

## Host tuning

The defaults below are what `deploy/lima-bootstrap.sh` applies and what every
benchmark report must state. They are not universal truths; verify on your
kernel.

```sysctl
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 65535
net.core.netdev_max_backlog = 65535
net.ipv4.ip_local_port_range = 10240 65535
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
fs.file-max = 2097152
fs.nr_open = 2097152
```

Descriptor limits:

```bash
ulimit -n 1048576
```

Locked-memory limits: newer kernels charge each io_uring ring's pages to the
per-user `RLIMIT_MEMLOCK` quota, and every process of that user shares the
quota. The common 8 MiB default is not enough for several 32K-entry rings and
shows up as `io_uring_setup` returning `ENOMEM`. Raise it for the service and
for test runs:

```ini
# systemd unit
LimitMEMLOCK=infinity
```

```bash
ulimit -l unlimited
```

Memory sizing: an idle connection costs kernel socket memory (typically
1.5–3 KiB, visible in `/proc/slabinfo` as `TCP` and `kmalloc-*`), one kernel
io_uring poll request, and a small slab entry (target under 1 KiB process RSS).
It holds no read buffer and no write buffer. TLS terminates upstream, so there
is no userspace cipher or record state. The budget numbers are frozen in
`docs/adr/0011-m0-verdicts.md` and measured in M1.

## Measured reference: v1.0.2

On a 4-vCPU ARM VM (7.7 GiB, kernel 6.8), v1 holds idle WebSocket connections
at a linear cost of:

| Component | Per connection |
| --------- | -------------- |
| Process RSS | ~9.0 KiB (8.81–9.47 across 20k–200k) |
| Kernel TCP slab | ~2.37 KiB (2368 B object) |
| **Total** | **~11.3 KiB** |

200,000 connections held with zero failures. Full data:
`docs/benchmarks/2026-10-22-v1-scaling.md`.

Use this to sanity-check capacity plans, and as the baseline v2 must beat:
v2's hypothesis is ≤1 KiB process RSS for plaintext WS. v1's ~9 KiB is why a
per-connection goroutine and buffer model cannot reach 1M on a 15 GiB host.

## Measured reference: v2 M1

On the same class of 4-vCPU ARM VM (kernel 6.8), 10,000 idle plaintext
WebSocket connections held with zero failures at:

| Component | Per connection |
| --------- | -------------- |
| Process RSS delta | **0.49 KiB steady / 0.59 KiB peak** |
| Slab entry | 160 B |
| Read buffer while idle | 0 |
| Goroutine / channel / timer | 0 |

The public echo path and the raw reactor echo path are both **0 B/op, 0
allocs/op at 100,000 messages**. Full method and raw data:
`docs/benchmarks/2026-10-23-m1-reactor.md`.

## Capacity planning

Per idle connection, plan for:

| Component | Source | How to measure |
| --------- | ------ | -------------- |
| Kernel socket | `TCP` slab | `grep '^TCP ' /proc/slabinfo` before/after |
| kTLS context | `tls_context` slab (if offloaded) | `grep tls_context /proc/slabinfo` |
| Application slot | process RSS | `VmRSS` delta / connection count |
| Go runtime | heap + stacks | `runtime.ReadMemStats`, pprof |

Do not multiply a single test run by a million. Measure at 10k, 100k, and
250k, and check that the per-connection slope is flat.

## Metrics

Required before any production claim:

- `connections_open`, `connections_accepted_total`, `connections_closed_total`
- `messages_in_total`, `messages_out_total`, `bytes_in_total`, `bytes_out_total`
- `backpressure_total`, `dropped_messages_total`, `slow_consumers_total`
- `protocol_errors_total`, `auth_failures_total`, `rate_limited_total`
- `reactor_lag_seconds` (histogram) — the single most important health signal
- `write_queue_bytes` (histogram), `write_queue_messages` (histogram)
- `handshake_duration_seconds`, `tls_offload_total{mode,result}`
- `goroutines`, `heap_bytes`, `open_fds`

Alert on reactor lag, not on connection count. A server holding a million
idle connections with flat lag is healthy; one holding ten thousand with rising
lag is not.

## Deployment behind TLS

The library speaks plaintext `ws://` only. TLS terminates at a load balancer or
reverse proxy in front of the process. The proxy must:

- terminate TLS 1.3;
- support HTTP/1.1 Upgrade and keep the upgraded connection open;
- enforce its own connection and header limits;
- forward the client address if authorization or per-IP accounting needs it
  (PROXY protocol is a later milestone; there is no in-process per-IP cap yet).

The plaintext listener must be reachable only from the proxy. `AllowedOrigins`
is the in-process cross-site WebSocket hijacking control; set it explicitly.

A quick manual check that the upgrade works through the proxy:

```bash
curl -i -N \
  -H 'Connection: Upgrade' \
  -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Sec-WebSocket-Protocol: encryl.v2' \
  http://proxy.example/ws
```

A `101 Switching Protocols` with `Sec-WebSocket-Protocol: encryl.v2` means
the proxy forwarded the upgrade correctly.

## Shutdown

`Server.Shutdown(ctx)`:

1. Stops accepting.
2. Sends close frames with `1001` (going away).
3. Drains in-flight writes until `CloseTimeout`.
4. Cancels remaining operations, closes descriptors exactly once.
5. Joins loops.

Bound it with a context deadline shorter than the orchestrator's grace period;
`ShutdownTimeout` is a second, internal bound. A descriptor leak after shutdown
is a bug, not an operational condition: check `open_fds` in the test suite.

## Kernel matrix

| Kernel | Role |
| ------ | ---- |
| 6.6 LTS | Minimum supported |
| 6.12 LTS | Current LTS target |
| latest stable | Canary |

io_uring is the only backend. Kernels older than 6.6 are unsupported and must
not be used to produce benchmark numbers. The runtime probes every required
opcode and flag and refuses to start if one is missing. Containers whose seccomp
profile denies `io_uring_setup` cannot run the server.

## Runbook: connection storm

Symptoms: rising accepted rate, `handshake_duration` p99 climbing, memory growth
without a matching open-connection increase, reactor lag.

1. Check `connections_open` vs `connections_accepted_total` — a widening gap
   means handshakes are not completing.
2. Check `SOMAXCONN` drops: `nstat -az | grep -i listen`.
3. Check `backpressure_total`: if it rises, clients are not reading.
4. Check `auth_failures_total`: a storm of failed auth is a credential attack,
   not a capacity problem.
5. If the accept backlog is full and CPU is saturated on one core, you have a
   single-loop bottleneck: raise `Loops` and verify `SO_REUSEPORT` distribution.

## Runbook: memory growth with stable connection count

1. Heap profile: growth in `bytes` from handlers means an application leak;
   growth in slab-sized allocations means connection state is not being
   released.
2. Check `slow_consumers_total` and write-queue histograms. A stalled peer holds
   a bounded queue per connection; many stalled peers add up.
3. Check `goroutines`. A constant goroutine count with growing heap points at
   buffers, not goroutine leaks.
4. Verify the generation counter is advancing on close; a stuck slot is a
   descriptor leak waiting to happen.
