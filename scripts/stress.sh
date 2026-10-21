#!/usr/bin/env bash
# Mixed stress: a stable group-chat fleet, continuous connection churn, and
# periodic room joins/leaves. Checks for descriptor, slot, and membership
# leaks when the churn stops.
#
#   CONNS=100000 ROOMSIZE=100 RATE=1000 DURATION=300s WORKERS=50 \
#     ./scripts/stress.sh
set -euo pipefail

CONNS=${CONNS:-100000}
ROOMSIZE=${ROOMSIZE:-100}
RATE=${RATE:-1000}
DURATION=${DURATION:-300s}
WORKERS=${WORKERS:-50}
CHURN_GAP=${CHURN_GAP:-2ms}
SRC_IPS=${SRC_IPS:-40}
ADDR=${ADDR:-127.0.0.1:18090}
LOG=${LOG:-/tmp/encryl-stress-server.log}

ulimit -n 1048576 2>/dev/null || true
go build -o /tmp/encryl-workload ./bench/workload

/tmp/encryl-workload -role server -addr "$ADDR" -workload group \
  -conns $((CONNS * 2)) -room-size "$ROOMSIZE" >"$LOG" 2>&1 &
SRV=$!
trap 'kill "$SRV" 2>/dev/null || true' EXIT
sleep 1

/tmp/encryl-workload -role client -addr "$ADDR" -workload group \
  -conns "$CONNS" -rate "$RATE" -duration "$DURATION" -room-size "$ROOMSIZE" \
  -src-ips "$SRC_IPS" >/tmp/encryl-stress-group.log 2>&1 &
GPID=$!
/tmp/encryl-workload -role client -addr "$ADDR" -workload churn \
  -conns "$CONNS" -workers "$WORKERS" -churn-gap "$CHURN_GAP" \
  -duration "$DURATION" -src-ips "$SRC_IPS" >/tmp/encryl-stress-churn.log 2>&1 &
CPID=$!

peak=0
while kill -0 "$GPID" 2>/dev/null; do
  rss=$(ps -o rss= -p "$SRV" | tr -d ' ')
  [ -n "$rss" ] && [ "$rss" -gt "$peak" ] && peak=$rss
  fd=$(ls "/proc/$SRV/fd" 2>/dev/null | wc -l | tr -d ' ')
  kill -HUP "$SRV" 2>/dev/null || true
  sleep 1
  echo "t=${SECONDS}s rss_kb=$rss fd=$fd $(grep 'stats:' "$LOG" | tail -1 | grep -o 'ConnectionsOpen:[0-9]*')"
  sleep 9
done

wait "$GPID" 2>/dev/null || true
wait "$CPID" 2>/dev/null || true
sleep 5
kill -HUP "$SRV" 2>/dev/null || true
sleep 1

echo "=== group client ==="; tail -5 /tmp/encryl-stress-group.log
echo "=== churn client ==="; tail -5 /tmp/encryl-stress-churn.log
echo "=== server after cleanup ==="; grep 'stats:' "$LOG" | tail -1
echo "peak_rss_kb=$peak"
