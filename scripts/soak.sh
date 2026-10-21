#!/usr/bin/env bash
# Start the echo example, hold CONNS idle WebSocket connections, and sample
# server RSS during the soak. Run on the Linux target.
set -euo pipefail

CONNS=${CONNS:-10000}
DURATION=${DURATION:-10s}
RATE=${RATE:-5000}
ADDR=${ADDR:-127.0.0.1:18080}
BIN=${BIN:-/tmp/encryl-echo}
CSV=${CSV:-/tmp/encryl-soak.csv}
PROFILE=${PROFILE:-$PWD/docs/benchmarks/raw/encryl-heap.prof}

ulimit -n 1048576 2>/dev/null || true
mkdir -p "$(dirname "$PROFILE")"

go build -o "$BIN" ./examples/echo
"$BIN" -addr "$ADDR" -heap-profile "$PROFILE" >/tmp/encryl-echo.log 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT
sleep 1

echo "t,rss_kb" >"$CSV"
(
  while kill -0 "$PID" 2>/dev/null; do
    rss=$(ps -o rss= -p "$PID" | tr -d ' ')
    echo "$(date +%s),$rss" >>"$CSV"
    sleep 1
  done
) &
SAMPLER=$!

go run ./bench/loadgen \
  -addr "$ADDR" \
  -mode idle \
  -conns "$CONNS" \
  -rate "$RATE" \
  -duration "$DURATION" &
LOAD=$!
sleep 5
kill -USR2 "$PID" 2>/dev/null || true
kill -USR1 "$PID" 2>/dev/null || true
sleep 3
steady=$(ps -o rss= -p "$PID" | tr -d ' ')
echo "steady,$steady" >>"$CSV"
wait "$LOAD"

kill "$SAMPLER" 2>/dev/null || true
sleep 1
kill "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true

awk -F, -v conns="$CONNS" '
  NR==2 { base=$2 }
  $1=="steady" { steady=$2; next }
  NR>1 && $1!="t" { if ($2>peak) peak=$2 }
  END {
    if (base == 0) { print "no RSS samples"; exit 1 }
    printf "connections=%d\n", conns
    printf "baseline_rss_kb=%d\n", base
    printf "peak_rss_kb=%d\n", peak
    printf "peak_delta_kb_per_conn=%.2f\n", (peak-base)/conns
    if (steady > 0) {
      printf "steady_rss_kb=%d\n", steady
      printf "steady_delta_kb_per_conn=%.2f\n", (steady-base)/conns
    }
    printf "raw_csv=%s\n", "'"$CSV"'"
    printf "heap_profile=%s\n", "'"$PROFILE"'"
  }
' "$CSV"
