# 0008 — A custom incremental RFC 6455 parser

Status: Accepted; paths moved by ADR-0014
Date: 2026-10-22
Deciders: sairash

## Context

v1 uses `gobwas/ws` for framing. It is fast and well tested, but its
`ReadHeader`/`ReadFrame` API assumes a contiguous header and a complete frame in
one buffer. v2 must parse across arbitrary completion boundaries: a kernel read
may deliver half a header, a header plus three frames, or one byte of a
continuation frame. The parser also has to enforce limits and validate UTF-8
incrementally while the data is still in a provided buffer, before it is
recycled.

## Decision

1. Implement a custom incremental parser in `protocol/websocket` with an
   explicit state machine over `(header bytes, payload bytes)`.
2. Enforce on the hot path: client masking, reserved-bit zero, control-frame
   size and FIN rules, fragment count/size limits, close-code validity,
   incremental UTF-8 for text, and "no data after close".
3. Unmask in place in the provided buffer. The server owns it and will recycle
   it, so no copy is needed.
4. Keep `gobwas/ws` as a **test-only** dependency and use it as a differential
   oracle in fuzz tests: both parsers must agree on accept/reject and on
   message boundaries.
5. Implement the HTTP upgrade parser too, with strict header count and size
   limits. It is small, and it must share the same buffer discipline as the
   frame parser.

## Consequences

- Parsing has no allocation for a message that fits in one buffer, and correct
  behavior when it does not.
- The parser is security-critical and must carry a fuzz corpus from day one.
- Two parsers exist in tests, which is a feature: disagreement is a bug.
- More code than importing a library. Offset by removing a dependency from the
  production path and by owning the limits and the buffer lifecycle.

## Alternatives rejected

- **Use gobwas/ws in production.** It cannot consume from arbitrary offsets
  without copying or retaining a fragment buffer, which defeats the
  no-buffer-while-idle goal.
- **Use coder/websocket or gorilla.** Both are designed around `net.Conn` with
  their own read buffering and goroutines; they fight the reactor model.
- **Hand-roll without a differential oracle.** Reinventing a parser with no
  second opinion is how framing bugs ship.
- **Parse per-message into a `bytes.Buffer`.** Allocation per message and a copy
  of every payload.

## Amendment — M1 implementation notes (2026-10-22)

1. The parser lives in `internal/ws`, not `protocol/websocket`. The library is
   under `internal/` so the public surface stays in ADR-0010.
2. Application data is binary-only. A text frame is rejected at the header with
   `1003`; there is no incremental UTF-8 path in M1. This removes a parser state
   and a fuzz surface. Autobahn text cases are declared expected rejects.
3. The HTTP upgrade parser lives in `internal/upgrade` and shares the
   fixed-size, incremental discipline. It keeps only the fields it needs and
   never buffers the whole request.
4. Unmasking is in place; the mask offset accumulates across completion
   boundaries.
