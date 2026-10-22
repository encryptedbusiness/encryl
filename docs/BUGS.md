# Bug ledger

This ledger records the audited issues against the restructured tree. It does
not fix them. Levels: **L1** = crash, corruption, or security; **L2** = wrong
behavior in an edge case; **L3** = cosmetic or metric.

| ID | Level | Status | Title | Location | Cause | Planned phase |
| -- | ----- | ------ | ----- | -------- | ----- | ------------- |
| BUG-001 | L1 | Verified fixed | ACK token ownership | `event/ack.go`, `internal/app/dispatch.go` | `Pending.Take` checks loop, connection slot, and generation before consuming an entry. | — |
| BUG-002 | L2 | Open | Server ping is never armed | `connslab/slab.go`, `reactor/` | `TimerPing` exists, but no path arms it; `PingInterval` currently only appears in configuration. | After restructure |
| BUG-003 | L2 | Open | `Start` returns before loops are ready | `internal/app/lifecycle.go` | Loops are started in goroutines; `Start` stores state and returns without a readiness barrier. | After restructure |
| BUG-004 | L2 | Verified fixed | Process fd ceiling not checked | `reactor/loop.go` | The loop clamps `MaxConnections` to `RLIMIT_NOFILE` when the limit is available. | — |
| BUG-005 | L3 | Open | `MessagesOut` is admission, not delivery | `reactor/send.go` | The counter increments when a frame enters the write queue, not when the kernel accepts it. | After restructure |
| BUG-006 | L3 | Open | No reactor lag metric | `reactor/api.go`, `internal/app/stats.go` | There is no loop-observed timestamp or dispatch-lag histogram. | After restructure |
| BUG-007 | L3 | Open | `MaxConnections` split is implicit | `internal/app/lifecycle.go` | Per-loop capacity is rounded up; only the aggregate ceiling is validated. | After restructure |
| BUG-008 | L3 | Open | Idle `Conn` has no public ID | `internal/app/dispatch.go` | A native connection without an open/close handler gets a `Conn` only on its first message. | Documented |
| BUG-009 | L3 | Open | No single conformance command | `Makefile`, `scripts/` | Qualification is split across `gate-a` through `gate-e` and `qualify-*`. | After restructure |
| BUG-010 | L3 | Open | No small-host profile preset | `internal/app/config.go` | Density tuning is manual; the default profile is not named for a small host. | After restructure |
| BUG-011 | L3 | Open | `ChannelEvents` can be orphaned | `internal/app/config.go` | A room event ID not present in `EventNames` is only a warning. | After restructure |

## Verification notes

- BUG-001 is covered by `TestPendingRejectsForeignConnection`.
- BUG-004 is covered by the loop clamp in `reactor/loop.go`.
- BUG-008 is intentional for idle density; applications that need an ID must
  register a connect or lifecycle handler.
