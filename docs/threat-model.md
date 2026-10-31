# Threat model

Status: M1 draft. Every control marked **planned** is implemented in the
milestone named in `docs/roadmap.md`. Controls marked **accepted** are residual
risks the project knowingly carries.

## Deployment premise

The process sits behind a TLS-terminating load balancer or reverse proxy
(HAProxy, Envoy, a cloud LB). The proxy owns certificates, terminates TLS, and
forwards plaintext `ws://` to the process. The listener must not be exposed to
an untrusted network.

This premise is recorded in `docs/adr/0013-tls-terminates-upstream.md`. It is a
deployment requirement, not an in-process control.

## Assets

1. The integrity of application messages.
2. The availability of the listener for all connections (one connection must
   not degrade its core).
3. The server process and host kernel.
4. Credentials and identities, once M2 adds authentication.

## Trust boundaries

```
client ──TLS──▶ proxy ──plaintext ws──▶ listener ──▶ reactor ──▶ handler
                                            │
                                            ├── untrusted input: HTTP upgrade,
                                            │   WebSocket frames, envelopes
                                            └── trusted: no untrusted code runs

kernel (io_uring) ── trusted, but a large attack surface
```

The proxy is part of the trusted path for confidentiality and client identity.
The library trusts it to have authenticated the client in M1.

## Threats and controls

| # | Threat | Control | Status |
| - | ------ | ------- | ------ |
| T1 | Plaintext exposure on an untrusted network | Deployment requires a TLS-terminating proxy; the library docs and `SECURITY.md` state it. The listener is not intended to be public. | Accepted (deployment) |
| T2 | Proxy misconfiguration silently downgrades security | No in-process TLS fallback exists. Operators must configure the proxy; the library refuses to pretend it terminates TLS. | Accepted |
| T3 | Malformed HTTP upgrade | Strict parser with header count/size limits, required Host/Upgrade/Connection/Version/Key, request-line limits, `MaxHeaderBytes` | M1 |
| T4 | WebSocket protocol abuse (unmasked frames, bad RSV, oversized control frames, fragmentation bombs) | Full RFC 6455 state machine; mask required from clients; control frames ≤125 bytes and never fragmented; `MaxFragments`, `MaxFrameSize`, `MaxMessageSize` | M1 |
| T5 | Text-frame smuggling | Application data is binary-only; a text frame is rejected at the header with `1003`. No UTF-8 path exists in M1. | M1 |
| T6 | Cross-site WebSocket hijacking (CSWSH) | Origin allowlist; empty list means same-origin only; no wildcard by default. With TLS upstream, this is the in-process browser defense. | M1 |
| T7 | Credential theft via URL/logs | Tokens only in the first `AUTH` envelope (native) or the `Connect` command (Bebop); never in query strings; structured logs redact credentials. | M2 |
| T8 | Brute force / credential stuffing | Constant-time comparison; auth deadline; per-IP connection cap; optional external rate limiter hook. | M2 |
| T9 | Per-connection floods | Token buckets for messages and bytes; max frame/message size | M2 |
| T10 | Slowloris at handshake | `HandshakeTimeout` enforced by the timer wheel, not by goroutines | M1 |
| T11 | Slow consumer exhausts server memory | Bounded write queue in bytes and messages; four explicit policies; `CloseSlowConsumer`; slow-consumer counter | M1 |
| T12 | Fan-out amplification | Encode once per encoding; reference-counted shared frame; per-recipient admission; no per-recipient encoding | M1 |
| T13 | Resource exhaustion by connection count | Slab ceiling and chunked growth; fd limits documented; accept path closes rather than leaks when the slab is full. A per-IP cap is deferred, so deploy a proxy-side limit | M1 |
| T14 | Stale-handle use-after-close | `(slot, generation)` validation on every operation; a slot is not released while an operation is outstanding; fuzzed | M1 |
| T15 | Descriptor double-close / reuse | Close exactly once via `IORING_OP_CLOSE`; close completion tagged and generation-checked | M1 |
| T16 | Handler blocks the reactor | Documented non-blocking contract; bounded worker pool; reactor-lag metric | M2 |
| T17 | io_uring widens the kernel attack surface | io_uring is the only backend and is Linux-only. The process must be trusted; privileges are dropped after ring setup; `IORING_REGISTER_RESTRICTIONS` allowlists every opcode. | Accepted |
| T18 | Proxy identity is mistaken for client identity | Native connections are pre-authenticated and carry no peer identity. A Bebop connection may call `Config.TokenVerifier`; a nil verifier accepts every token. There is no per-IP cap; the proxy must enforce one until PROXY protocol support lands. | Accepted, later milestone |
| T19 | Supply-chain compromise of the io_uring binding | Vendored, pinned, with a NOTICE, LICENSE, and a documented patch; upstream is audited on upgrade | M0 |
| T20 | Randomness / identifier predictability | Connection IDs are not secrets and are never used for authorization; auth tokens are compared constant-time; any generated token uses `crypto/rand` | M2 |
| T21 | Malformed Bebop command or frame | Bounds-checked hand decoders; 4 MiB frame, 1 MiB message, 1024-message batch caps; decoder fuzzed; malformed input closes with 1002 | M1 (protocol slice) |
| T22 | Event name hash collision | FNV-1a 32; registration rejects a name mapped to a different ID; unknown events are dropped and unknown requests get an error ACK; raw bytes are borrowed, never decoded by the library; use `OnID` for collision-sensitive applications | M1 (named events) |

## io_uring: explicit trust statement

io_uring request contents live in shared memory, so seccomp cannot inspect or
filter them. The subsystem also has a history of privilege-escalation bugs:
Google's kernel security team reported that 60% of the Linux kernel exploits it
received in one year used io_uring, and restricted it in Android (seccomp),
ChromeOS (disabled), and Google production.

The architecture now depends on io_uring rather than offering it as an option
(ADR-0012). Therefore:

- The process is trusted. Do not run untrusted tenants in the same process.
- Drop privileges after ring setup where the deployment allows it.
- `IORING_REGISTER_RESTRICTIONS` is mandatory and allowlists every opcode.
- Containers whose seccomp profile denies `io_uring_setup` cannot run the
  server. This is an accepted limitation, not a bug.
- The startup probe fails closed rather than silently changing the I/O model.

## TLS: explicit statement

The library does not terminate, offload, or negotiate TLS. It does not see
client certificates, and it does not claim confidentiality.

- The operator must terminate TLS in front of the process.
- `AllowedOrigins` is the in-process CSWSH control.
- A proxy-supplied client identity is an M2 design. Until then, every
  connection is treated as pre-authenticated by the proxy.
- The former kTLS code and spike remain as M0 evidence and are never imported
  by the library.

## Out of scope for v2

- Application-level authorization semantics (the project provides hooks).
- MQTT/Socket.IO semantics (possible adapters after v2).
- Protecting against a compromised host kernel or a malicious same-tenant
  process.
- Confidentiality of the proxy-to-process hop; deploy the proxy and the process
  on a trusted network.
- Post-quantum TLS (follows the proxy's TLS stack).

## Review checklist for new features

1. Does it add a new way for a peer to allocate memory without a bound?
2. Does it put a secret in a URL, a log line, or an error string?
3. Does it introduce an operation that can block the owning loop?
4. Does it widen the io_uring opcode allowlist?
5. Does it add a public type that exposes internal state?
6. Does it weaken a default (Origin, limits, policy, trusted-proxy assumption)?
