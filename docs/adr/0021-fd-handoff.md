# ADR-0021: FD handoff for framework-owned listeners

Status: Accepted
Date: 2026-10-24
Deciders: Sairash

## Context

Applications often already run an HTTP server for routing, authentication, and
metrics. Reimplementing those concerns inside the WebSocket server is not
acceptable. The framework must keep its listener while encryl serves the
upgraded socket on its io_uring reactor.

The frameworks represent the accepted socket as a `net.Conn`. Passing that
object to the reactor would keep the Go netpoller and finalizer in the path.
Reading it through a wrapper would copy every frame. Both defeat the reactor's
ownership and zero-copy message path.

## Decision

1. The framework parses and validates the HTTP request using its normal
   handler. Encryl validates the WebSocket fields from the parsed headers
   with `PrepareUpgrade`, computes the accept key and subprotocol, and returns
   a value-sized `UpgradeSpec`.
2. The adapter hijacks the connection. Encryl duplicates the descriptor
   with a single `F_DUPFD_CLOEXEC`. This is a kernel reference count bump; it
   copies no socket data and preserves `O_NONBLOCK`.
3. The duplicate is sent to a loop through a small `AttachRing`, separate from
   the send/fan-out command ring. The loop creates the connection directly in
   the upgrade state, queues the `101`, and arms poll. There is no synthetic
   HTTP request and no second HTTP parse.
4. The adapter closes its original `net.Conn` after enqueueing. The reactor owns
   the duplicate. A failed enqueue closes the duplicate and returns
   `ErrBackpressure`.
5. Bytes the framework already read into a userspace buffer are forwarded once
   and only if present. The normal handoff copies no data.
6. Framework response writing is suppressed (`HijackSetNoResponse(true)` for
   fasthttp/Fiber); encryl writes the `101`.

## Consequences

The core remains independent of net/http, fasthttp, and Fiber. Framework
adapters live in a separate `adapters` module. `Start` replaces listener
binding; `ListenAndServe` remains unchanged for standalone deployments.

The handoff cost is a few syscalls and an attach-ring entry, not a request
buffer allocation. `net.Conn.SyscallConn` still allocates its short-lived
raw-conn wrapper; the duplicate itself and the attach command are
allocation-free on the common path. Fixed-file registration is deliberately not
on the attach path:
it adds a registration syscall and a sparse table for the connection ceiling.
It can be benchmarked later without changing this ownership model.
