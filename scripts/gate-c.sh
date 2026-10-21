#!/usr/bin/env bash
# Gate C: one room, periodic broadcast, N readers, server-side p99 latency.
#
# The fanout server measures each broadcast from Emit to the last socket write
# completion. It reads one process clock, so there is no clock skew.
#
# Scaled run:
#   CONNS=10000 COUNT=20 DURATION=30s ./scripts/gate-c.sh
#
# 1M run (two tcpkali reader shards, 500k each):
#   CONNS=1000000 SHARDS=2 SRC_IPS=40 READERS=tcpkali COUNT=600 \
#   DURATION=900s RAMP_TIMEOUT=600 ./scripts/gate-c.sh
set -euo pipefail

CONNS=${CONNS:-10000}
SHARDS=${SHARDS:-1}
READERS=${READERS:-loadgen}          # loadgen | tcpkali
RATE=${RATE:-5000}                   # connections per second, per shard
DURATION=${DURATION:-30s}            # reader lifetime
INTERVAL=${INTERVAL:-1s}
COUNT=${COUNT:-20}
RAMP_TIMEOUT=${RAMP_TIMEOUT:-600}
SRC_IPS=${SRC_IPS:-1}
ADDR=${ADDR:-127.0.0.1:18082}
TCPKALI=${TCPKALI:-/Users/sairash/.cache/encryl-tcpkali/tcpkali}
[ -x "$TCPKALI" ] || TCPKALI=/tmp/tcpkali/src/tcpkali
LOG=${LOG:-/tmp/encryl-fanout.log}

ulimit -n 1048576 2>/dev/null || true
pkill -9 -f tcpkali 2>/dev/null || true
pkill -9 -f encryl-fanout 2>/dev/null || true
sleep 1

to_seconds() {
  local v=$1
  case "$v" in
    *ms) echo $(( ${v%ms} / 1000 + 1 ));;
    *s)  echo "${v%s}";;
    *m)  echo $(( ${v%m} * 60 ));;
    *)   echo "$v";;
  esac
}

sudo sysctl -q -w \
  fs.file-max=4000000 fs.nr_open=4000000 \
  net.core.somaxconn=65535 net.ipv4.tcp_max_syn_backlog=65535 \
  net.core.netdev_max_backlog=65535 \
  net.ipv4.ip_local_port_range="1024 65535" \
  net.ipv4.tcp_tw_reuse=1 \
  net.ipv4.tcp_mem="1800000 2200000 2600000" \
  net.ipv4.tcp_rmem="1024 4096 16384" net.ipv4.tcp_wmem="1024 4096 16384" \
  net.core.rmem_max=16384 net.core.wmem_max=16384 \
  net.ipv4.tcp_moderate_rcvbuf=0 2>/dev/null || true
sudo sh -c "echo 3000000 > /proc/sys/net/netfilter/nf_conntrack_max" 2>/dev/null || true

per=$((CONNS / SHARDS))
perIPs=$((SRC_IPS / SHARDS))
[[ "$per" -gt 0 && "$perIPs" -gt 0 ]] || { echo "bad shard math" >&2; exit 2; }

go build -o /tmp/encryl-fanout ./bench/fanout
go build -o /tmp/encryl-loadgen ./bench/loadgen

/tmp/encryl-fanout -addr "$ADDR" -max-conns $((CONNS * 2)) \
  -interval "$INTERVAL" -count "$COUNT" -auto-start=false >"$LOG" 2>&1 &
SRV=$!
PIDS=()
trap 'kill "$SRV" 2>/dev/null || true; for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT
sleep 1

if [ "$READERS" = "tcpkali" ]; then
  [[ -x "$TCPKALI" ]] || { echo "tcpkali not built at $TCPKALI" >&2; exit 2; }
  for s in $(seq 0 $((SHARDS - 1))); do
    ARGS=(--ws -c "$per" --connect-rate "$RATE" -T "$(to_seconds "$DURATION")"
          -H "Sec-WebSocket-Protocol: encryl.v2")
    base=$((2 + s * perIPs))
    for i in $(seq "$base" $((base + perIPs - 1))); do
      ARGS+=(--source-ip "127.0.0.$i")
    done
    "$TCPKALI" "${ARGS[@]}" "$ADDR" >"/tmp/encryl-gatec-reader-$s.log" 2>&1 &
    PIDS+=($!)
  done
else
  for s in $(seq 0 $((SHARDS - 1))); do
    /tmp/encryl-loadgen -addr="$ADDR" -mode=read -conns="$CONNS" -rate="$RATE" \
      -duration="$DURATION" -src-ips="$perIPs" -shard="$s" -shards="$SHARDS" \
      >"/tmp/encryl-gatec-reader-$s.log" 2>&1 &
    PIDS+=($!)
  done
fi

target=$((CONNS - CONNS/50))
deadline=$((SECONDS + RAMP_TIMEOUT))
room=0
last=0
stale=0
while [ "$SECONDS" -lt "$deadline" ]; do
  kill -HUP "$SRV" 2>/dev/null || true
  sleep 1
  room=$(grep -o 'room=[0-9]*' "$LOG" | tail -1 | cut -d= -f2)
  room=${room:-0}
  [ "$room" -ge "$target" ] && break
  if [ "$room" -le "$last" ]; then
    stale=$((stale + 1))
  else
    stale=0
  fi
  last=$room
  # The ramp has stopped growing. Start with what is connected; the 2% slack
  # also covers a listener that rejected a few slots.
  [ "$stale" -ge 5 ] && [ "$room" -gt 0 ] && break
  alive=0
  for p in "${PIDS[@]}"; do kill -0 "$p" 2>/dev/null && alive=$((alive + 1)); done
  [ "$alive" -eq 0 ] && break
done
echo "room=$room target=$target"
[ "$room" -gt 0 ] || { echo "no readers connected" >&2; exit 1; }

echo "--- start broadcasts ---"
kill -USR1 "$SRV" 2>/dev/null || true

wait_for=$((COUNT * $(to_seconds "$INTERVAL") + 120))
for _ in $(seq 1 "$wait_for"); do
  grep -q 'broadcasts=' "$LOG" && break
  sleep 1
done

kill -HUP "$SRV" 2>/dev/null || true
sleep 1
echo "--- latency ---"
grep 'broadcasts=' "$LOG" | tail -1 || echo "no summary"
echo "--- server ---"
grep 'stats:' "$LOG" | tail -1
echo "--- readers ---"
for s in $(seq 0 $((SHARDS - 1))); do
  echo "shard $s:"; tail -4 "/tmp/encryl-gatec-reader-$s.log" 2>/dev/null || true
done
