# Golden wire vectors

Canonical `encryl.v2` envelopes, one per file, hex-encoded with whitespace
removed.

`manifest.json` is the machine-readable form: kind, flags, event ID, request ID,
and payload. The Go and TypeScript codecs added in M2 both decode every vector
and re-encode it byte-for-byte.

| File | Kind | Fields |
| ---- | ---- | ------ |
| `event-1024.hex` | `EVENT` | event 1024, request `0x0102030405060708`, payload `"hi"` |
| `auth-bearer-abc.hex` | `AUTH` | scheme 1, token `"abc"`, request 1 |
| `ack-1024.hex` | `ACK` | `RESPONSE` flag, event 1024, same request |

These files are data, not a test harness. Until the M2 codecs exist, the
authoritative description is `docs/protocol.md`; the vectors exist so that the
description and the implementation cannot drift apart.
