#!/usr/bin/env bash
# Gate A: measure steady-state server RSS and file descriptors per connection.
#
# Starts examples/echo, ramps CONNS WebSocket clients with one or more tcpkali
# shards, then samples RSS and /proc/<pid>/fd once per SAMPLE seconds for HOLD
# seconds. The 60-minute gate is:
#
#   CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 HOLD=3600 RAMP_TIMEOUT=600 \
#     ./scripts/memory.sh
set -euo pipefail

CONNS=${CONNS:-100000}
SHARDS=${SHARDS:-1}
RATE=${RATE:-10000}
HOLD=${HOLD:-90}
SAMPLE=${SAMPLE:-60}
MSG_RATE=${MSG_RATE:-0}
MSG=${MSG:-/tmp/encryl-mem-msg.bin}
SRC_IPS=${SRC_IPS:-20}
RAMP_TIMEOUT=${RAMP_TIMEOUT:-300}
MAX_PER_CONN=${MAX_PER_CONN:-0.40}
ADDR=${ADDR:-127.0.0.1:18080}
BIN=${BIN:-/tmp/encryl-echo}
LOG=${LOG:-/tmp/encryl-mem-server.log}
SAMPLES=${SAMPLES:-/tmp/encryl-mem-samples.log}
TCPKALI=${TCPKALI:-/Users/sairash/.cache/encryl-tcpkali/tcpkali}
[ -x "$TCPKALI" ] || TCPKALI=/tmp/tcpkali/src/tcpkali

[[ -x "$TCPKALI" ]] || { echo "tcpkali not built at $TCPKALI" >&2; exit 2; }
per=$((CONNS / SHARDS))
perIPs=$((SRC_IPS / SHARDS))
[[ "$per" -gt 0 && "$perIPs" -gt 0 ]] || { echo "bad shard math" >&2; exit 2; }
ulimit -n 1048576 2>/dev/null || true
pkill -9 -f tcpkali 2>/dev/null || true
sleep 1
sudo sh -c "echo 3000000 > /proc/sys/net/netfilter/nf_conntrack_max" 2>/dev/null || true

if [ "$MSG_RATE" != "0" ]; then
  python3 - "$MSG" <<'PYMSG'
import struct, sys
msg = bytes([2, 2, 0, 0]) + struct.pack(">I", 1024) + struct.pack(">Q", 0) + b"PING"
open(sys.argv[1], "wb").write(msg)
PYMSG
fi

# Keep every client connected through the whole hold window. tcpkali counts
# -T from each connection's own start, so add the ramp budget.
TCPKALI_HOLD=$((HOLD + RAMP_TIMEOUT))

go build -o "$BIN" ./examples/echo
"$BIN" -addr "$ADDR" -max-conns "$CONNS" -idle-timeout 2h >"$LOG" 2>&1 &
SERVER=$!
PIDS=()
trap 'kill "$SERVER" 2>/dev/null || true; for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT
sleep 1

baseline=$(ps -o rss= -p "$SERVER" | tr -d ' ')
: >"$SAMPLES"

for s in $(seq 0 $((SHARDS - 1))); do
  ARGS=(--ws -c "$per" --connect-rate "$RATE" -T "$TCPKALI_HOLD"
        -H "Sec-WebSocket-Protocol: encryl.v2")
  if [ "$MSG_RATE" != "0" ]; then
    ARGS+=(-e -m '\{ws.binary <'"$MSG"'>}' -r "$MSG_RATE")
  fi
  base=$((2 + s * perIPs))
  for i in $(seq "$base" $((base + perIPs - 1))); do
    ARGS+=(--source-ip "127.0.0.$i")
  done
  "$TCPKALI" "${ARGS[@]}" "$ADDR" >"/tmp/encryl-mem-tcpkali-$s.log" 2>&1 &
  PIDS+=($!)
done

open_now() {
  grep -o 'ConnectionsOpen:[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d: -f2
}

fd_now() { ls "/proc/$SERVER/fd" 2>/dev/null | wc -l | tr -d ' '; }

sample() {
  local label=$1 rss fds open
  kill -USR1 "$SERVER" 2>/dev/null || true
  sleep 3
  rss=$(ps -o rss= -p "$SERVER" | tr -d ' ')
  fds=$(fd_now)
  kill -HUP "$SERVER" 2>/dev/null || true
  sleep 1
  open=$(open_now)
  in=$(grep -o 'MessagesIn:[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d: -f2)
  out=$(grep -o 'MessagesOut:[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d: -f2)
  echo "$label t=${SECONDS}s rss_kb=$rss fd=$fds open=$open in=${in:-0} out=${out:-0}" | tee -a "$SAMPLES"
  SAMPLE_RSS=$rss
  SAMPLE_FD=$fds
}

# Wait until the server itself reports the target open count. Sampling before
# the ramp finishes measures in-flight handshakes, not steady state.
target=$((CONNS - CONNS/100))
deadline=$((SECONDS + RAMP_TIMEOUT))
opened=0
while [ "$SECONDS" -lt "$deadline" ]; do
  kill -HUP "$SERVER" 2>/dev/null || true
  sleep 1
  opened=$(open_now)
  opened=${opened:-0}
  [ "$opened" -ge "$target" ] && break
done
echo "ramp_open=$opened target=$target"
[ "$opened" -ge "$target" ] || echo "WARNING: ramp incomplete" >&2

sample ramp_end
fd_ramp=$SAMPLE_FD
steady=$SAMPLE_RSS
steady2=$steady

hold_end=$((SECONDS + HOLD))
while [ "$SECONDS" -lt "$hold_end" ]; do
  sleep "$SAMPLE"
  [ "$SECONDS" -ge "$hold_end" ] && break
  sample hold
  steady2=$SAMPLE_RSS
done

sample hold_end
fd_hold=$SAMPLE_FD

kill -HUP "$SERVER" 2>/dev/null || true
sleep 1
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done

awk -v conns="$CONNS" -v b="$baseline" -v s="$steady" -v s2="$steady2" \
    -v fr="$fd_ramp" -v fh="$fd_hold" 'BEGIN {
  printf "connections=%d\n", conns
  printf "baseline_rss_kb=%d\n", b
  printf "ramp_end_rss_kb=%d\n", s
  printf "hold_end_rss_kb=%d\n", s2
  printf "steady_delta_kb_per_conn=%.3f\n", (s-b)/conns
  printf "hold_delta_kb_per_conn=%.3f\n", (s2-b)/conns
  printf "per_conn_limit_kb=%.3f\n", '"$MAX_PER_CONN"'
  printf "rss_growth_pct=%.3f\n", (s > 0 ? (s2-s)*100.0/s : 0)
  printf "fd_ramp=%d\nfd_hold=%d\nfd_leak=%d\n", fr, fh, fh-fr
}'

steady_per=$(awk -v b="$baseline" -v s="$steady" -v c="$CONNS" \
  'BEGIN { if (c > 0) v = (s-b)/c; else v = 0; printf "%.3f", v }')
if awk -v value="$steady_per" -v limit="$MAX_PER_CONN" \
    'BEGIN { exit !(value < limit) }'; then
  echo "gate-a: PASS steady_delta_kb_per_conn=$steady_per < $MAX_PER_CONN"
else
  echo "gate-a: FAIL steady_delta_kb_per_conn=$steady_per >= $MAX_PER_CONN" >&2
  exit 1
fi

grep -E "stats:" "$LOG" | tail -1 || true
for s in $(seq 0 $((SHARDS - 1))); do
  grep -E "Ramped up" "/tmp/encryl-mem-tcpkali-$s.log" | tail -1 || true
done
