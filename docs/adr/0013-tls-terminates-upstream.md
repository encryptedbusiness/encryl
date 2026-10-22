# 0013 — TLS terminates upstream; the library speaks plaintext ws://

Status: Accepted
Date: 2026-10-22
Deciders: Sairash

Supersedes [0002](0002-tls-and-ktls.md).

## Context

ADR-0002 kept userspace TLS 1.3 as the default and kTLS as a gated optimization
for the 1M-WSS memory target. M0 proved the kTLS handoff works, but also showed
that RX offload cannot resume once userspace consumes a pipelined application
record.

The deployment model has changed. The server runs behind a load balancer or
reverse proxy (HAProxy, Envoy, cloud LB) that already terminates TLS, owns
certificates, and handles protocol upgrades. Duplicating TLS termination in the
library adds per-connection cipher state, record buffers, a second certificate
and configuration surface, and a key-handling path — all for a hop that is
already encrypted.

The library's value is the room/event transport and its per-connection memory,
not a TLS stack.

## Decision

1. **The library does not terminate, offload, or negotiate TLS.** It speaks
   plaintext `ws://` only.
2. `Config.TLSConfig`, `Config.TLSMode`, `TLSUserspace`,
   `TLSKernelTXPrefer`, `TLSKernelTXRXPrefer`, `TLSKernelTXRXRequire`, and the
   plaintext-on-loopback restriction are removed.
3. The kTLS code under `internal/ktls` and the kTLS spike remain as M0
   historical artifacts. The library never imports them and they are removed
   from the active CI path.
4. TLS termination is an explicit deployment requirement. The operator must
   place the process behind a TLS-terminating proxy and must not expose the
   plaintext listener to an untrusted network.
5. `AllowedOrigins` becomes load-bearing: with no TLS hop in-process, the
   Origin allowlist is the in-process defense against cross-site WebSocket
   hijacking.
6. Client identity from a TLS certificate is not available to the library.
   Authentication is deferred to M2 and, in M1, every connection is treated as
   pre-authenticated by the trusted proxy.

## Consequences

- Per-connection memory no longer includes cipher state or TLS record buffers.
  The density budget is a plaintext budget.
- Operators must configure the proxy to preserve the WebSocket upgrade and to
  enforce its own limits. The library's `MaxHeaderBytes` still bounds the
  upgrade it receives.
- `MaxConnsPerIP` sees the proxy's address unless PROXY protocol support is
  added. PROXY protocol is an M2 item; the limitation is documented now.
- The "1M WSS" claim is removed. Any future encrypted-density claim belongs to
  the proxy, not this library.
- A deployment that needs in-process TLS must add it in a fork or an outer
  listener; it is not a configuration option.

## Alternatives rejected

- **Keep userspace TLS as the default.** Adds memory and a certificate surface
  for a hop the deployment already encrypts.
- **Keep kTLS as an opt-in mode.** It is Linux-only, has the RX pipelining
  limitation, and requires a key-log handoff that belongs in a separate
  project if it is ever needed.
- **Terminate TLS only on loopback and require a proxy on the same host.**
  Still duplicates the proxy's job and does not remove the cipher-state cost.
- **Use mTLS identity for authorization.** The library never sees the
  certificate; the proxy must pass identity separately (M2 design).
