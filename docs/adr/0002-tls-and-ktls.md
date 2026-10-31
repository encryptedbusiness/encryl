# 0002 — Userspace TLS by default, kTLS as a gated optimization

Status: Superseded by [0013](0013-tls-terminates-upstream.md)
Date: 2026-10-22
Deciders: sairash

> **Superseded.** TLS now terminates at a load balancer or reverse proxy; the
> library speaks plaintext ws:// only. The kTLS findings remain as M0 evidence.
> 0013 records the decision that replaced this ADR.

## Context

The original plan made kTLS TX+RX a hard production requirement and a release
blocker. That is the riskiest premise in the program, so it was researched
before being designed around.

### Why kTLS is worth having *here*

The widely cited kTLS wins come from `sendfile`/`splice` on static files. That
does not apply to WebSockets, which build every frame in memory. The argument
that does apply is **per-connection memory**:

| Per idle WSS connection, userspace TLS | Approx. |
| -------------------------------------- | ------- |
| `tls.Conn` and connection state | ~0.5 KiB |
| AEAD cipher state | ~0.5–1 KiB |
| Record read buffer + write staging | ~2–4 KiB |
| **Total before application state** | **~3–6 KiB** |

At 1M connections that is 3–6 GiB of process RSS before connection state or room
membership, which cannot fit the `≤4 KiB process RSS/conn` budget. kTLS moves
the AEAD and record layer into the kernel (`tls_context`), lets RX decrypt
directly into provided buffers, and lets TX accept a shared plaintext fan-out
frame as-is. That is what makes the 1M-WSS number arithmetically reachable.

### Why it is not a correctness requirement

- Userspace `crypto/tls` is the reference implementation and is the safer
  default for a security-sensitive library.
- 1M plaintext WebSocket connections do not need kTLS at all.
- Moderate-scale throughput does not need it; encryption is rarely the
  bottleneck for small messages.
- kTLS is Linux-only, needs the `tls` module, supports TLS 1.3 only here, and
  has more RX bugs on older kernels.

### Feasibility

Go does not expose TLS 1.3 traffic secrets through a supported API
(golang/go#44506 remains open). Two viable mechanisms exist:

1. `Config.KeyLogWriter` emits NSS key-log lines including
   `SERVER_TRAFFIC_SECRET_0` and `CLIENT_TRAFFIC_SECRET_0`; derive `key`/`iv`
   with HKDF-Expand-Label.
2. A patched `crypto/tls`, as used by production forks, which is a maintenance
   liability across Go releases.

Prior art already implements option 1 end to end, including record sequence
accounting, post-handshake drain, and `EKEYEXPIRED` KeyUpdate handling
(`northernside/ktls`, `costela/ktls-listener`). This de-risks the handoff
substantially.

## Decision

1. `Config.TLSMode` is explicit:
   - `TLSUserspace` (default): `crypto/tls` only. Available everywhere.
   - `TLSKernelTXPrefer`: offload encryption when the suite and kernel allow it.
   - `TLSKernelTXRXPrefer`: offload both directions when available.
   - `TLSKernelTXRXRequire`: refuse the connection if offload cannot be armed.
2. `Config.MinVersion` is forced to TLS 1.3. TLS 1.2 is not supported.
3. Offload failure is explicit and counted (`tls_offload_total{mode,result}`).
   `Require` never downgrades silently.
4. The "1M WSS" **claim** — not the release — is gated on
   `TLSKernelTXRXPrefer` passing M3. If it does not, the claim is re-scoped to
   plaintext plus a measured lower WSS number.
5. Key derivation lives in `internal/ktls` behind `ParseKeyLog` and
   `ExpandLabel`, with a golden test pinning the key-log format and an
   independent RFC 5869 vector test.

## Consequences

- v2 ships even if kTLS never works; only a marketing number depends on it.
- Two TLS paths must be tested. Mitigation: userspace is the reference, and kTLS
  must pass the same interop and fault matrix.
- Key material exists in both userspace and kernel during takeover. The key log
  buffer is never logged or persisted, and capture exists only in the spike.
- The key-log format is not a Go API contract. Mitigation: a golden test and a
  `go tip` canary job; a format change is a loud test failure, not a silent
  misconfiguration.

## Amendment — measured 2026-10-22

The spike ran on Ubuntu 24.04 / kernel 6.8.0-147 / arm64 / Go 1.27.2
(`docs/benchmarks/2026-10-22-m0-linux-verification.md`). Results:

1. **TX offload works** in every tested mode, verified by
   `/proc/net/tls_stat` (`TlsTxSw` moved), not only by a successful
   `setsockopt`.
2. **RX offload works only when userspace consumes no application-epoch
   record.** A synchronized client is fine: the Finished belongs to the
   handshake key epoch, so RX arms at sequence 0. `TlsRxSw` moved.
3. **RX cannot resume after userspace consumes an application record.** A
   client that pipelines data right after its Finished forces crypto/tls to
   decrypt that record. Arming `TLS_RX` afterwards returns `EBADMSG` at every
   starting sequence tested (0–3), even though the kernel demonstrably honors
   `rec_seq`. There is no recoverable position.

Consequences for the design:

- `TLSUserspace` remains the default and the only unconditional mode.
- `TLSKernelTXPrefer` is safe for all clients.
- `TLSKernelTXRXPrefer` is per connection: it offloads RX when it can and
  reports `RXFallback` when it cannot. It never pretends to have offloaded.
- `TLSKernelTXRXRequire` must close any connection that pipelines. The M2
  protocol rule — clients wait for the server's AUTH ACK before sending events
  — makes that rare for conforming clients, but it is not a security boundary
  and must be enforced, not assumed.
- The offload-rate metric must count fallbacks. A "100% offloaded" claim based
  on handshake success alone would be false.

The drain approach (read plaintext out of `tls.Conn` and replay it) was
implemented and then removed: it cannot fix the kernel sequence, and it adds a
partial-record failure mode. Explicit fallback is simpler and honest.

## Alternatives rejected

- **Mandatory kTLS as originally specified.** Blocks the entire release on an
  unsupported Go API plus kernel-version behavior, with no fallback.
- **Userspace TLS only.** Abandons the memory budget that makes the 1M-WSS goal
  possible, without saving much work (the handshake path is shared).
- **cgo + OpenSSL.** Adds a C dependency, static-linking problems, a second
  crypto CVE surface, and still requires the same key handoff or an
  OpenSSL-specific kTLS mode. Rejected for the default path.
- **Patching `crypto/tls`.** Works, but every Go upgrade becomes a merge. The
  key-log path is isolated to one file and can be swapped for a fork later
  without touching the server.
