# ADR-0022: High-level public packages and map policy

Status: Accepted
Date: 2026-10-25
Deciders: Sairash

## Context

The v2 implementation grew as internal vertical slices. The ring, listener,
HTTP upgrade parser, and connection slab were usable only inside the module.
The tree also carried Linux filename suffixes, non-Linux stubs, a research
spike, duplicate helpers, and two map styles with different safety rules.

## Decision

1. The module is Linux-only in name and fact. Kernel files carry
   `//go:build linux` and no `_linux` suffix. The `_other` stubs are gone.
2. Public packages are `uring`, `netio`, `reactor`, `connslab`, `timerwheel`,
   `ws`, `httpx`, `event`, `room`, and `protocol`. The root package is the
   facade. `internal/` keeps only shared helpers.
3. Synchronous maps are frozen before traffic. `event.Registry` and
   `protocol.EventRegistry` publish immutable snapshots; lookups take no lock.
4. Asynchronous maps use `xsync/v4`. The room directory is
   `xsync.Map[string, *roomEntry]`; rejection counters use
   `xsync.Map[uint32, *atomic.Uint64]`. Loop-owned state stays as plain maps and
   dense arrays because one loop owns it.
5. Validation returns typed `ConfigProblem` values. Errors and warnings are
   separate. Defaults come from `DefaultConfig` through `normalize`.
6. Dead symbols are deleted rather than kept for compatibility. The bug ledger
   is `docs/BUGS.md`; this ADR does not fix those bugs.

## Consequences

Other projects can import the engine layers without the facade. The cost is a
larger public API surface and a stronger Linux-only rule. A non-Linux build
fails at compile time by design; tests run on the Lima VM.

## Map benchmark verdict

The room snapshot benchmark (`BenchmarkSnapshot1000`) measured 4.76-4.86 µs/op
with the xsync directory on the Lima VM. The pre-change report recorded
5.035 µs/op for the same workload. xsync is kept because it is faster and
allocates 0 B/op. The command is:

```sh
go test -run=NONE -bench=BenchmarkSnapshot1000 -benchmem -count=3 ./bench/room
```
