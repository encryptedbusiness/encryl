#!/usr/bin/env bash
# Gate B at a true message rate, driven by bench/loadgen instead of tcpkali.
#
# tcpkali batches fractional per-connection rates, so it cannot hold 50k msg/s
# across 1M idle connections. loadgen paces each connection with an absolute
# schedule.
#
#   CONNS=400000 SHARDS=2 MSG_RATE=0.125 DURATION=400s ./scripts/gate-b-loadgen.sh
#
# Aggregate rate = CONNS * MSG_RATE = 50,000 messages per second by default.
set -euo pipefail

CONNS=${CONNS:-400000}
SHARDS=${SHARDS:-2}
SRC_IPS=${SRC_IPS:-40}
RATE=${RATE:-10000}
MSG_RATE=${MSG_RATE:-0.125}
DURATION=${DURATION:-400s}
MONITOR=${MONITOR:-60}
ADDR=${ADDR:-127.0.0.1:18080}
LOG=${LOG:-/tmp/encryl-gateb-loadgen.log}

ulimit -n 1048576 2>/dev/null || true

per=$((CONNS / SHARDS))
perIPs=$((SRC_IPS / SHARDS))
[[ "$per" -gt 0 && "$perIPs" -gt 0 ]] || { echo "bad shard math" >&2; exit 2; }

go build -o /tmp/encryl-echo ./examples/echo
go build -o /tmp/encryl-loadgen ./bench/loadgen

/tmp/encryl-echo -addr "$ADDR" -max-conns $((CONNS * 2)) -idle-timeout 2h \
  -tx-bytes 67108864 -buffer-count 32768 -read-buffer 2048 >"$LOG" 2>&1 &
SERVER=$!
PIDS=()
trap 'kill "$SERVER" 2>/dev/null || true; for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT
sleep 1

echo "conns=$CONNS shards=$SHARDS msg_rate=$MSG_RATE aggregate=$(awk -v c="$CONNS" -v r="$MSG_RATE" 'BEGIN{printf "%.0f", c*r}')/s"
for s in $(seq 0 $((SHARDS - 1))); do
  /tmp/encryl-loadgen \
    -addr="$ADDR" -mode=echo -envelope \
    -conns="$CONNS" -rate="$RATE" -msg-rate="$MSG_RATE" -duration="$DURATION" \
    -src-ips="$perIPs" -shard="$s" -shards="$SHARDS" \
    -msg-size=128 -report="/tmp/encryl-gateb-shard-$s.csv" \
    >"/tmp/encryl-gateb-shard-$s.log" 2>&1 &
  PIDS+=($!)
done

# Sample the server while the run is live. The server prints Stats on SIGHUP.
STOP=/tmp/encryl-gateb-stop
rm -f "$STOP"
(
  while [ ! -f "$STOP" ]; do
    sleep "$MONITOR"
    [ -f "$STOP" ] && break
    kill -HUP "$SERVER" 2>/dev/null || true
    sleep 1
    grep 'stats:' "$LOG" | tail -1 || true
    free -m | awk '/Mem:/{printf "mem used=%dMB available=%dMB\n",$3,$7}'
  done
) &
SAMPLER=$!

for p in "${PIDS[@]}"; do wait "$p" || true; done
touch "$STOP"
wait "$SAMPLER" 2>/dev/null || true

kill -HUP "$SERVER" 2>/dev/null || true
sleep 1
echo "--- server ---"
grep 'stats:' "$LOG" | tail -1
echo "--- shards ---"
for s in $(seq 0 $((SHARDS - 1))); do
  echo "shard $s:"
  tail -12 "/tmp/encryl-gateb-shard-$s.log"
done
