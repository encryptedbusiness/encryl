#!/usr/bin/env bash
# Run one practical workload: unicast, group, ack, or churn.
#
#   WORKLOAD=group CONNS=10000 ROOMSIZE=100 RATE=1000 DURATION=60s \
#     ./scripts/practical.sh
#
# The server and the client run in separate processes. The script prints the
# client summary, the server stats, and peak server RSS.
set -euo pipefail

WORKLOAD=${WORKLOAD:-group}
CONNS=${CONNS:-10000}
RATE=${RATE:-1000}
DURATION=${DURATION:-60s}
ROOMSIZE=${ROOMSIZE:-100}
SRC_IPS=${SRC_IPS:-20}
WORKERS=${WORKERS:-10}
CHURN_GAP=${CHURN_GAP:-5ms}
ADDR=${ADDR:-127.0.0.1:18090}
LOG=${LOG:-/tmp/encryl-practical-server.log}
CLOG=${CLOG:-/tmp/encryl-practical-client.log}

ulimit -n 1048576 2>/dev/null || true

go build -o /tmp/encryl-workload ./bench/workload

/tmp/encryl-workload -role server -addr "$ADDR" -workload "$WORKLOAD" \
  -conns "$CONNS" -room-size "$ROOMSIZE" >"$LOG" 2>&1 &
SRV=$!
trap 'kill "$SRV" 2>/dev/null || true' EXIT
sleep 1

peak=0
(
  while kill -0 "$SRV" 2>/dev/null; do
    rss=$(ps -o rss= -p "$SRV" 2>/dev/null | tr -d ' ')
    [ -n "$rss" ] && [ "$rss" -gt "$peak" ] && peak=$rss
    sleep 2
  done
  echo "$peak" >/tmp/encryl-practical-peak
) &
SAMPLER=$!

/tmp/encryl-workload -role client -addr "$ADDR" -workload "$WORKLOAD" \
  -conns "$CONNS" -rate "$RATE" -duration "$DURATION" -room-size "$ROOMSIZE" \
  -src-ips "$SRC_IPS" -workers "$WORKERS" -churn-gap "$CHURN_GAP" \
  >"$CLOG" 2>&1

kill -HUP "$SRV" 2>/dev/null || true
sleep 1
STATS=$(grep 'stats:' "$LOG" | tail -1 || true)
kill "$SRV" 2>/dev/null || true
wait "$SAMPLER" 2>/dev/null || true
peak=$(cat /tmp/encryl-practical-peak 2>/dev/null || echo 0)

echo "=== workload=$WORKLOAD conns=$CONNS rate=$RATE room=$ROOMSIZE ==="
tail -6 "$CLOG"
echo "--- server ---"
echo "$STATS"
echo "peak_rss_kb=$peak"
awk -v p="$peak" -v c="$CONNS" 'BEGIN{if(c>0) printf "peak_rss_kb_per_conn=%.3f\n", p/c}'
