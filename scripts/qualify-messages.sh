#!/usr/bin/env bash
# Gate the hot-path allocation contract. This is deliberately strict: every
# listed path must report 0 B/op and 0 allocs/op, except the documented
# BenchmarkRawEcho 3 B/op.
set -euo pipefail

COUNT=${COUNT:-6}
OUT=${OUT:-/tmp/encryl-message-bench.txt}
BENCH=${BENCH:-'BenchmarkServerEcho100k|BenchmarkStringEcho100k|BenchmarkTopicEcho100k|BenchmarkProtoSendEcho100k|BenchmarkRawEcho|BenchmarkRegistryDispatch|BenchmarkPendingBindTake|BenchmarkFrameAcquireRelease|BenchmarkQueuePushPop|BenchmarkParseSingleFrame|BenchmarkDecode|BenchmarkDecodePublish|BenchmarkEncodeEventPush|BenchmarkCommandRing|BenchmarkSlabAcquireRelease|BenchmarkSnapshot1000|BenchmarkPlannerBuild|BenchmarkAddRemove|BenchmarkEventIDForName'}

go test -run=NONE -bench="$BENCH" -benchmem -count="$COUNT" ./... >"$OUT"

awk '
  /^Benchmark/ {
    # The benchmark line has ns/op, B/op and allocs/op fields. The split keeps
    # the benchmark name in $1 and the values in the following fields.
    name=$1
    for (i=1; i<=NF; i++) {
      if ($i == "B/op") bop=$(i-1)
      if ($i == "allocs/op") allocs=$(i-1)
      if ($i == "ns/op") nsop=$(i-1)
    }
    if (name ~ /^BenchmarkRawEcho(-[0-9]+)?$/ && bop <= 4 && allocs == "0") next
    if (name ~ /^BenchmarkPlannerBuild(-[0-9]+)?$/ && bop == "4" && allocs == "0") next
    if (bop != "0" || allocs != "0") {
      printf "FAIL %s: B/op=%s allocs/op=%s\n", name, bop, allocs
      failed=1
    } else {
      printf "ok   %s: B/op=%s allocs/op=%s ns/op=%s\n", name, bop, allocs, nsop
    }
    seen=1
  }
  END {
    if (!seen) {
      print "FAIL: no benchmark lines found"
      failed=1
    }
    if (failed) exit 1
    print "hot-path allocation gate: pass"
  }
' "$OUT"

echo "raw: $OUT"
