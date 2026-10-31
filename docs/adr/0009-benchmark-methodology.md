# 0009 — Benchmark methodology and what counts as evidence

Status: Accepted
Date: 2026-10-22
Deciders: sairash

## Context

The v1 README claims 201,624 connections at 1676.64 MB from a table with no
command, no commit, and no host. The benchmark driver it came from has data
races, no percentiles, and no server in the repository. That is not evidence; it
is a screenshot.

v2's entire premise — io_uring, kTLS, a specific memory budget — is a
performance claim. It needs a methodology that can fail.

## Decision

1. **Every result records** host, kernel, CPU, memory, commit, date, exact
   command, sysctls, `ulimit -n`, Go version, proxy/TLS termination mode, and a
   path to raw data. `make preflight` produces this block.
2. **Percentiles, not means.** p50/p95/p99/p99.9 for latency; message rate and
   byte rate for throughput. The mean hides exactly the tail that matters.
3. **At least three repetitions**, and the spread is published, not the best
   run.
4. **Separate the generator from the server** on cores, and use
   `-shard/-shards` plus `-src-ips` above 100k connections. If the generator is
   saturated, the run is invalid, not slow.
5. **Memory is reported by component**: process RSS, Go heap and stacks, kernel
   socket slabs, and kernel io_uring poll requests. A single "RSS per
   connection" number hides where the memory went and which optimization would
   help.
6. **Comparability rules**:
   - v1 and v2 message throughput are not comparable without a protocol
     adapter; density and accept rate are.
   - v2 numbers are plaintext. TLS terminates upstream; its cost is not the
     library's and must not be attributed to it.
   - io_uring is the only backend, so there is no epoll comparison. Report the
     v1 baseline and the v2 slope.
7. **Pre-registered thresholds.** The io_uring go/no-go threshold in ADR-0001
   was withdrawn by ADR-0012. New thresholds must be registered before the run
   that tests them.
8. **No number without a command.** `make docs-check` enforces metadata on
   every file under `docs/benchmarks/`.

## Consequences

- Some claims disappear. That is the intended outcome.
- Runs take longer and require a dedicated host. There is no shortcut that
  produces trustworthy density numbers.
- A 1M-connection claim cannot be made until it is measured on the documented
  host. The former "1M WSS" claim is removed by ADR-0013.

## Alternatives rejected

- **`go test -bench` only.** Necessary for microbenchmarks, useless for 1M
  connections and end-to-end latency.
- **Load-test with `wrk`/`autocannon`.** HTTP tools, not WebSocket-capable at
  this connection count, and they hide per-connection state.
- **Trust the v1 README table.** It is not reproducible and its driver is racy.
- **Compare against published third-party numbers.** Different hardware,
  kernel, and workload; useful only as a sanity check, never as a substitute.
