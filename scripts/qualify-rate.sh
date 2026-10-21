#!/usr/bin/env bash
# Gate the message path at a fixed offered rate. The server is examples/echo;
# the generator wraps every payload in the native envelope.
set -euo pipefail

CONNS=${CONNS:-1000}
RATE=${RATE:-2000}
MSG_RATE=${MSG_RATE:-10}
DURATION=${DURATION:-10s}
MSG_SIZE=${MSG_SIZE:-256}
MIN_RATE=${MIN_RATE:-5000}
MAX_P99_US=${MAX_P99_US:-200000}
ADDR=${ADDR:-127.0.0.1:18080}
BIN=${BIN:-/tmp/encryl-echo-rate}
LOG=${LOG:-/tmp/encryl-rate-server.log}
OUT=${OUT:-/tmp/encryl-rate-load.log}

go build -o "$BIN" ./examples/echo
"$BIN" -addr "$ADDR" -max-conns "$((CONNS * 2))" -idle-timeout 2m >"$LOG" 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT
sleep 1

set +e
go run ./bench/loadgen \
  -addr="$ADDR" \
  -mode=echo \
  -conns="$CONNS" \
  -rate="$RATE" \
  -duration="$DURATION" \
  -msg-size="$MSG_SIZE" \
  -envelope \
  -msg-rate="$MSG_RATE" >"$OUT" 2>&1
load_rc=$?
set -e

kill "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true

cat "$OUT"
if [ "$load_rc" -ne 0 ]; then
  echo "FAIL: load generator exited $load_rc"
  exit 1
fi

messages=$(awk '/^messages / {print $2; exit}' "$OUT")
failed=$(awk '/^failed / {print $2; exit}' "$OUT")
p99=$(awk '/^rtt_us / {for (i=1; i<=NF; i++) if ($i ~ /^p99=/) {sub("p99=","",$i); print $i; exit}}' "$OUT")
if [ -z "${messages:-}" ] || [ -z "${failed:-}" ] || [ -z "${p99:-}" ]; then
  echo "FAIL: missing summary fields"
  exit 1
fi
if [ "$failed" -ne 0 ]; then
  echo "FAIL: failed=$failed"
  exit 1
fi
if [ "$messages" -le 0 ]; then
  echo "FAIL: messages=$messages"
  exit 1
fi
seconds=$(awk -v d="$DURATION" 'BEGIN { sub(/s$/,"",d); print d }')
rate=$(awk -v m="$messages" -v s="$seconds" 'BEGIN { if (s <= 0) print 0; else printf "%.0f", m/s }')
if [ "$rate" -lt "$MIN_RATE" ]; then
  echo "FAIL: message rate $rate/s is below $MIN_RATE/s"
  exit 1
fi
if [ "$p99" -gt "$MAX_P99_US" ]; then
  echo "FAIL: p99 ${p99}us is above ${MAX_P99_US}us"
  exit 1
fi

echo "message-rate gate: pass messages=$messages failed=$failed rate=${rate}/s p99=${p99}us"
echo "server log: $LOG"
echo "raw: $OUT"
