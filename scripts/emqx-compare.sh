#!/usr/bin/env bash
# EMQX workload comparison: one topic, N subscribers, one publisher.
#
# The delivery shape matches the encryl group run:
#   10,000 subscribers x 600 messages = 6,000,000 deliveries at ~100k/s.
#
#   CONNS=10000 RATE=10 SECONDS=60 ./scripts/emqx-compare.sh
set -euo pipefail

CONNS=${CONNS:-10000}
RATE=${RATE:-10}
SECONDS_RUN=${SECONDS_RUN:-60}
COUNT=$((RATE * SECONDS_RUN))
BENCH=${BENCH:-/tmp/eb/bin/emqtt_bench}
ulimit -n 1048576 2>/dev/null || true
ulimit -u 1048576 2>/dev/null || true
SUBLOG=/tmp/emqx-sub.log
PUBLOG=/tmp/emqx-pub.log

pkill -f emqtt_bench 2>/dev/null || true
"$BENCH" sub -h 127.0.0.1 -p 1883 -c "$CONNS" -R 1000 -t bench/topic -q 0 \
  >"$SUBLOG" 2>&1 &
SUB=$!
trap 'kill "$SUB" 2>/dev/null || true' EXIT

# Wait for the subscriber fleet to ramp (10k at 1k/s plus handshakes).
for _ in $(seq 1 40); do
  sleep 1
  n=$(sg docker -c "docker exec emqx emqx ctl broker stats" 2>/dev/null | awk '/^live_connections.count/{print $NF}' || true)
  n=${n:-0}
  if [ "$n" -ge "$CONNS" ]; then break; fi
done
echo "clients=$n target=$CONNS"

"$BENCH" pub -h 127.0.0.1 -p 1883 -c 1 -I $((1000 / RATE)) -L "$COUNT" \
  -t bench/topic -q 0 -s 128 >"$PUBLOG" 2>&1
sleep 3
kill "$SUB" 2>/dev/null || true

echo "--- publisher ---"; tail -6 "$PUBLOG"
echo "--- subscriber ---"; tail -6 "$SUBLOG"
echo "--- broker stats ---"
sg docker -c "docker exec emqx emqx ctl broker stats" 2>/dev/null | grep -E 'live_connections|connections.count' | head -3
sg docker -c "docker exec emqx emqx ctl broker metrics" 2>/dev/null | grep -E '^messages.(sent|received|dropped) ' | head
echo "--- memory ---"
sg docker -c "docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.CPUPerc}}' emqx" 2>/dev/null
