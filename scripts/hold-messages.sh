#!/usr/bin/env bash
# 1M-member hold with messages: every connection receives one broadcast every
# INTERVAL seconds for HOLD seconds. Samples RSS and file descriptors once per
# SAMPLE seconds. This exercises the full lifecycle: accept, join, fan-out,
# write queue, refcount return, and close.
#
#   CONNS=1000000 HOLD=3600 INTERVAL=10s ./scripts/hold-messages.sh
set -euo pipefail

CONNS=${CONNS:-1000000}
SHARDS=${SHARDS:-2}
SRC_IPS=${SRC_IPS:-40}
RATE=${RATE:-10000}
HOLD=${HOLD:-3600}
SAMPLE=${SAMPLE:-60}
INTERVAL=${INTERVAL:-10s}
RAMP_TIMEOUT=${RAMP_TIMEOUT:-600}
ADDR=${ADDR:-127.0.0.1:18082}
TCPKALI=${TCPKALI:-/Users/sairash/.cache/encryl-tcpkali/tcpkali}
[ -x "$TCPKALI" ] || TCPKALI=/tmp/tcpkali/src/tcpkali
LOG=${LOG:-/tmp/encryl-hold-fanout.log}
SAMPLES=${SAMPLES:-/tmp/encryl-hold-samples.log}

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
sudo sh -c 'echo 3000000 > /proc/sys/net/netfilter/nf_conntrack_max' 2>/dev/null || true

per=$((CONNS / SHARDS))
perIPs=$((SRC_IPS / SHARDS))
count=$((HOLD / $(to_seconds "$INTERVAL")))
[ "$count" -gt 0 ] || count=1
read_life=$((HOLD + RAMP_TIMEOUT))

go build -o /tmp/encryl-fanout ./bench/fanout
/tmp/encryl-fanout -addr "$ADDR" -max-conns $((CONNS * 2)) \
  -interval "$INTERVAL" -count "$count" -auto-start=false >"$LOG" 2>&1 &
SRV=$!
PIDS=()
trap 'kill "$SRV" 2>/dev/null || true; for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT
sleep 1
: >"$SAMPLES"

for s in $(seq 0 $((SHARDS - 1))); do
  ARGS=(--ws -c "$per" --connect-rate "$RATE" -T "$read_life"
        -H "Sec-WebSocket-Protocol: encryl.v2")
  base=$((2 + s * perIPs))
  for i in $(seq "$base" $((base + perIPs - 1))); do
    ARGS+=(--source-ip "127.0.0.$i")
  done
  "$TCPKALI" "${ARGS[@]}" "$ADDR" >"/tmp/encryl-hold-reader-$s.log" 2>&1 &
  PIDS+=($!)
done

open_now() { grep -o 'ConnectionsOpen:[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d: -f2; }
fd_now() { ls "/proc/$SRV/fd" 2>/dev/null | wc -l | tr -d ' '; }
room_now() { grep -o 'room=[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d= -f2; }
sent_now() { grep -o 'MessagesOut:[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d: -f2; }
drop_now() { grep -o 'DroppedMessages:[0-9]*' "$LOG" 2>/dev/null | tail -1 | cut -d: -f2; }

sample() {
  local label=$1
  kill -USR1 "$SRV" 2>/dev/null || true
  local rss fds open room sent drop
  rss=$(ps -o rss= -p "$SRV" | tr -d ' ')
  fds=$(fd_now)
  kill -HUP "$SRV" 2>/dev/null || true
  sleep 1
  open=$(open_now); room=$(room_now); sent=$(sent_now); drop=$(drop_now)
  echo "$label t=${SECONDS}s rss_kb=$rss fd=$fds open=${open:-0} room=${room:-0} sent=${sent:-0} drop=${drop:-0}" | tee -a "$SAMPLES"
  SAMPLE_RSS=$rss
  SAMPLE_FD=$fds
}

target=$((CONNS - CONNS/50))
deadline=$((SECONDS + RAMP_TIMEOUT))
room=0
while [ "$SECONDS" -lt "$deadline" ]; do
  kill -HUP "$SRV" 2>/dev/null || true
  sleep 1
  room=$(room_now); room=${room:-0}
  [ "$room" -ge "$target" ] && break
done
echo "room=$room target=$target"
[ "$room" -gt 0 ] || { echo "ramp failed" >&2; exit 1; }

baseline=$(ps -o rss= -p "$SRV" | tr -d ' ')
sample ramp_end
fd_ramp=$SAMPLE_FD
steady=$SAMPLE_RSS

echo "--- start broadcasts (${count} x ${INTERVAL}) ---"
kill -USR1 "$SRV" 2>/dev/null || true

hold_end=$((SECONDS + HOLD))
while [ "$SECONDS" -lt "$hold_end" ]; do
  sleep "$SAMPLE"
  [ "$SECONDS" -ge "$hold_end" ] && break
  sample hold
  # Stop early if the broadcast loop has already finished.
  grep -q 'broadcasts=' "$LOG" && break
done

sample hold_end
fd_hold=$SAMPLE_FD
steady2=$SAMPLE_RSS

kill -HUP "$SRV" 2>/dev/null || true
sleep 1
STATS=$(grep 'stats:' "$LOG" | tail -1 || true)
kill "$SRV" 2>/dev/null || true
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
sleep 1

echo "=== summary ==="
grep 'broadcasts=' "$LOG" | tail -1 || echo "no broadcast summary"
echo "$STATS"
awk -v conns="$CONNS" -v b="$baseline" -v s="$steady" -v s2="$steady2" \
    -v fr="$fd_ramp" -v fh="$fd_hold" 'BEGIN {
  printf "connections=%d\n", conns
  printf "baseline_rss_kb=%d\n", b
  printf "ramp_end_rss_kb=%d\n", s
  printf "hold_end_rss_kb=%d\n", s2
  printf "steady_delta_kb_per_conn=%.3f\n", (s-b)/conns
  printf "hold_delta_kb_per_conn=%.3f\n", (s2-b)/conns
  printf "rss_growth_pct=%.3f\n", (s > 0 ? (s2-s)*100.0/s : 0)
  printf "fd_ramp=%d\nfd_hold=%d\nfd_leak=%d\n", fr, fh, fh-fr
}'
