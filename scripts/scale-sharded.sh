#!/usr/bin/env bash
# Sharded high-connection scale test.
#
# Two or more tcpkali processes each get a disjoint source-IP range. The echo
# server logs Stats on SIGHUP; this script samples it during the ramp.
#
#   CONNS=1000000 SHARDS=2 SRC_IPS=40 RATE=10000 MONITOR=300 ./scripts/scale-sharded.sh
set -euo pipefail

CONNS=${CONNS:-1000000}
SHARDS=${SHARDS:-2}
SRC_IPS=${SRC_IPS:-40}
RATE=${RATE:-10000}
DURATION=${DURATION:-420s}
MONITOR=${MONITOR:-300}
DRAIN=${DRAIN:-0}
SAMPLE=${SAMPLE:-30}
MSG_RATE=${MSG_RATE:-0}
TX_BYTES=${TX_BYTES:-4194304}
BUFFER_COUNT=${BUFFER_COUNT:-4096}
READ_BUFFER=${READ_BUFFER:-4096}
MSG=${MSG:-/tmp/encryl-msg.bin}
ADDR=${ADDR:-127.0.0.1:18080}
BIN=${BIN:-/tmp/encryl-echo}
TCPKALI=${TCPKALI:-/tmp/tcpkali/src/tcpkali}
TCP_MEM=${TCP_MEM:-"1800000 2200000 2600000"}
CONNTRACK_MAX=${CONNTRACK_MAX:-3000000}
LOG=${LOG:-/tmp/encryl-sharded-server.log}

[[ -x "$TCPKALI" ]] || { echo "tcpkali not built at $TCPKALI" >&2; exit 2; }
per=$((CONNS / SHARDS))
perIPs=$((SRC_IPS / SHARDS))
[[ "$per" -gt 0 && "$perIPs" -gt 0 ]] || { echo "bad shard math" >&2; exit 2; }

ulimit -n 1048576 2>/dev/null || true
sudo sysctl -q -w \
  fs.file-max=4000000 fs.nr_open=4000000 \
  net.core.somaxconn=65535 net.ipv4.tcp_max_syn_backlog=65535 \
  net.core.netdev_max_backlog=65535 \
  net.ipv4.ip_local_port_range="1024 65535" \
  net.ipv4.tcp_tw_reuse=1 \
  net.ipv4.tcp_mem="$TCP_MEM" \
  net.ipv4.tcp_rmem="1024 4096 16384" \
  net.ipv4.tcp_wmem="1024 4096 16384" \
  net.core.rmem_max=16384 net.core.wmem_max=16384 \
  net.ipv4.tcp_moderate_rcvbuf=0
sudo sysctl -q -w net.netfilter.nf_conntrack_max="$CONNTRACK_MAX" 2>/dev/null || true

if [ "$MSG_RATE" != "0" ]; then
  python3 - "$MSG" <<'PY'
import struct, sys
msg = bytes([2, 2, 0, 0]) + struct.pack(">I", 1024) + struct.pack(">Q", 0) + b"PING"
open(sys.argv[1], "wb").write(msg)
PY
fi

go build -o "$BIN" ./examples/echo
"$BIN" -addr "$ADDR" -max-conns "$CONNS" -idle-timeout 2h \
  -tx-bytes "$TX_BYTES" -buffer-count "$BUFFER_COUNT" -read-buffer "$READ_BUFFER" >"$LOG" 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null || true; for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT
sleep 1

PIDS=()
for s in $(seq 0 $((SHARDS - 1))); do
  ARGS=(--ws -c "$per" --connect-rate "$RATE" -T "$DURATION"
        -H "Sec-WebSocket-Protocol: encryl.v2")
  if [ "$MSG_RATE" != "0" ]; then
    ARGS+=(-e -m '\{ws.binary <'"$MSG"'>}' -r "$MSG_RATE")
  fi
  base=$((2 + s * perIPs))
  for i in $(seq "$base" $((base + perIPs - 1))); do
    ARGS+=(--source-ip "127.0.0.$i")
  done
  "$TCPKALI" "${ARGS[@]}" "$ADDR" >"/tmp/encryl-shard-$s.log" 2>&1 &
  PIDS+=($!)
done

echo "shards=${#PIDS[@]} conns=$CONNS per_shard=$per src_ips=$SRC_IPS monitor=${MONITOR}s"
for _ in $(seq 1 $((MONITOR / SAMPLE))); do
  sleep "$SAMPLE"
  kill -HUP "$SERVER" 2>/dev/null || true
  sleep 1
  grep 'stats:' "$LOG" | tail -1
  free -m | awk '/Mem:/{printf "mem used=%dMB available=%dMB\n",$3,$7}'
done

if [ "$DRAIN" != "0" ]; then
  echo "--- drain: stopping clients and waiting ${DRAIN}s ---"
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  sleep "$DRAIN"
  kill -HUP "$SERVER" 2>/dev/null || true
  sleep 1
  grep 'stats:' "$LOG" | tail -1
  free -m | awk '/Mem:/{printf "mem used=%dMB available=%dMB\n",$3,$7}'
fi

echo "--- shard logs ---"
for s in $(seq 0 $((SHARDS - 1))); do
  echo "shard $s:"; tail -n 3 "/tmp/encryl-shard-$s.log" 2>/dev/null || true
done
echo "--- server ---"
grep 'stats:' "$LOG" | tail -1
