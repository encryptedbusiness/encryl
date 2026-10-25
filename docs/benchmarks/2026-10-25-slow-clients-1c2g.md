# Slow-client and edge-condition report

Date: 2026-10-25
Commit: working tree on `v2`
Host: `lima-encryl-dev`, Apple Silicon, VM 8 vCPU / 15.6 GiB
Kernel: 6.8.0-147-generic
Memory budget: server cgroup `MemoryMax=2G`, `CPUQuota=100%`
Go: 1.27.2 linux/arm64
Client: `bench/loadgen` and direct TCP probes
TLS mode: plaintext
Raw: console output in this report

## Results

### 10k WebSocket clients that stop reading

Server: `bench/fanout`, one room, 10k members, one CPU, 1000 broadcasts.
Clients: `bench/loadgen -mode=idle` and never call Read.

| Metric | Result |
| ------ | ------ |
| Open clients | 10,000 |
| Broadcasts | 1,000 |
| Messages out | 3,060,013 accepted |
| Backpressure rejections | 6,939,987 |
| Dropped messages | 6,939,987 |
| Write queue depth | 630,000 frames |
| Write queue bytes | 21.4 MiB |
| Process RSS | 38.7 MiB |
| Cgroup | 248 MiB |
| Send errors | 0 |
| Buffer exhausted | 0 |
| Protocol errors | 0 |
| Broadcast p99.99 | 30.5 ms |
| Broadcast max | 41.3 ms |

The reactor did **not** halt. `BackpressureReject` bounded each connection at
64 queued frames / 4 MiB and dropped new sends once a client stopped reading.
There is no global outbound-memory cap, so 10k slow clients settled at about
21 MiB queued. Scaling that naively to 500k is not safe; use
`BackpressureCloseSlow` or a much smaller per-connection queue for untrusted
clients.

Recommended policy:

- **Chat/presence/lossy updates:** `BackpressureDropOldest`.
- **Authoritative state/commands:** `BackpressureCloseSlow`.
- **Trusted low-volume clients:** `BackpressureReject` if the application checks
  the error and closes.
- Keep `WriteQueueMessages`/`WriteQueueBytes` small; the default 64 / 4 MiB is
  a per-connection allowance, not a server-wide budget.

### Slow HTTP clients

Added to the raw `httpuring` server:

| Option | Default | Effect |
| ------ | ------- | ------ |
| `HeaderTimeout` | 10s | Close a connection that has not completed a request |
| `IdleTimeout` | 2m | Close a keep-alive connection with no next request |
| `WriteTimeout` | 30s | Close a connection whose response cannot drain |
| `MaxOutputBytes` | 256 KiB | Pause reads and bound per-connection pipelined output |
| `MaxConnections` | 0 (unlimited) | Reject accepts above the configured raw HTTP connection count |

The header timer covers the request line, headers, and body until one complete
request is parsed. It is stopped on hijack/close, so an upgraded WebSocket does
not carry an HTTP timer.

Edge tests added and passing:

- slow partial HTTP header is closed by `HeaderTimeout`
- idle keep-alive HTTP connection is closed by `IdleTimeout`
- second connection is rejected when `MaxConnections=1`
- `Close`/`Server.Close` closes active raw HTTP connections and stops timers
- hijack stops all HTTP timers before transferring the descriptor
- `MaxOutputBytes` pauses reads when pipelined output reaches the high-water mark
- `WriteTimeout` requests a close instead of letting a non-reading client pin a
  response forever

### Other checked conditions

- Client disconnect while receiving: `recv=0` closes the slot.
- Malformed request line/headers: close, no panic.
- Oversized headers/body: close when the 8 KiB input buffer fills.
- Pipelined requests: supported; output is bounded by `MaxOutputBytes`.
- HTTP/1.0/1.1 connection-close: handled.
- WebSocket slow consumer: backpressure/drop/close policy, no loop block.
- Ping bursts: at 483k sockets, 30s pings reduced API throughput and pushed p99
  to 603 ms; disable or tune pings for density.
- Shutdown: active raw HTTP connections are closed; upgraded sockets are owned
  by encryl and drained by `Server.Shutdown`.

## Remaining recommendations

1. Set `MaxConnections` explicitly in production. The raw HTTP server has no
   safe default because WebSocket upgrade traffic is transient.
2. Use `BackpressureCloseSlow` for untrusted WebSocket fleets; `Reject` prevents
   loop blocking but leaves slow sockets alive.
3. Keep heartbeat intervals long or off for idle-density deployments.
4. Add a server-wide outbound-byte budget if a single process may serve hundreds
   of thousands of slow consumers.
5. Authenticate before upgrade with local JWT/ticket verification. Never make a
   blocking database call inline in the raw HTTP loop.
6. Use `Workers=0` for the listener that serves `/ws`; hijack is inline-only.
