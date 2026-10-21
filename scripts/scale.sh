#!/usr/bin/env bash
# High-connection WebSocket scale test.
#
# Uses tcpkali (C/epoll) so the client does not dominate memory. The server is
# examples/echo. Run on the Linux target. sysctl changes are host-global; this
# script is intended for the disposable Lima VM.
#
#   CONNS=100000 RATE=5000 DURATION=30s SRC_IPS=4 ./scripts/scale.sh
#   CONNS=1000000 RATE=5000 DURATION=300s SRC_IPS=20 MSG_RATE=0.01 ./scripts/scale.sh
set -euo pipefail

CONNS=${CONNS:-100000}
RATE=${RATE:-5000}
DURATION=${DURATION:-30s}
SRC_IPS=${SRC_IPS:-4}
MSG_RATE=${MSG_RATE:-0}
LATENCY=${LATENCY:-0}
ADDR=${ADDR:-127.0.0.1:18080}
BIN=${BIN:-/tmp/encryl-echo}
TCPKALI=${TCPKALI:-/tmp/tcpkali/src/tcpkali}
MSG=${MSG:-/tmp/encryl-msg.bin}
TCP_MEM=${TCP_MEM:-"800000 1000000 1200000"}
CONNTRACK_MAX=${CONNTRACK_MAX:-3000000}
CSV=${CSV:-/tmp/encryl-scale.csv}
TX_BYTES=${TX_BYTES:-$([ "$MSG_RATE" = "0" ] && echo 1048640 || echo 67108864)}

[[ -x "$TCPKALI" ]] || { echo "tcpkali not built at $TCPKALI" >&2; exit 2; }

ulimit -n 1048576 2>/dev/null || true

# Small buffers buy connection density; Appwrite's 1M run used the same style.
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

python3 - "$MSG" <<'PY'
import struct, sys
msg = bytes([2, 2, 0, 0]) + struct.pack(">I", 1024) + struct.pack(">Q", 0) + b"PING"
open(sys.argv[1], "wb").write(msg)
PY

go build -o "$BIN" ./examples/echo
"$BIN" \
  -addr "$ADDR" \
  -max-conns "$CONNS" \
  -idle-timeout 2h \
  -tx-bytes "$TX_BYTES" \
  >/tmp/encryl-scale-server.log 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT
sleep 1

echo "t,rss_kb,client_rss_kb,mem_available_kb" >"$CSV"
(
  while kill -0 "$PID" 2>/dev/null; do
    rss=$(ps -o rss= -p "$PID" | tr -d ' ')
    client=$(ps -C tcpkali -o rss= | awk '{s+=$1} END{print s+0}')
    avail=$(awk '/MemAvailable/ {print $2}' /proc/meminfo)
    echo "$(date +%s),$rss,$client,$avail" >>"$CSV"
    sleep 1
  done
) &
SAMPLER=$!

ARGS=(--ws -v -c "$CONNS" --connect-rate "$RATE" -T "$DURATION"
      -H "Sec-WebSocket-Protocol: encryl.v2")
if [ "$MSG_RATE" != "0" ]; then
  ARGS+=(-e -m '\{ws.binary <'"$MSG"'>}' -r "$MSG_RATE")
  if [ "$LATENCY" = "1" ]; then
    # tcpkali allocates an HdrHistogram per connection when this is enabled.
    ARGS+=(--latency-marker PING)
  fi
fi
for i in $(seq 2 $((SRC_IPS + 1))); do
  ARGS+=(--source-ip "127.0.0.$i")
done

DURATION_SEC=${DURATION%s}
(
  wait_for=$((DURATION_SEC > 10 ? DURATION_SEC - 5 : DURATION_SEC))
  sleep "$wait_for"
  kill -USR2 "$PID" 2>/dev/null || true
) &
STATS_SIGNAL=$!

"$TCPKALI" "${ARGS[@]}" "$ADDR" >/tmp/encryl-tcpkali.log 2>&1 || true
kill "$STATS_SIGNAL" 2>/dev/null || true

kill "$SAMPLER" 2>/dev/null || true
sleep 1
kill "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true

awk -F, 'NR==2{base=$2} NR>1{
  if($2>peak)peak=$2
  if($3>client)client=$3
  if(min==0||$4<min)min=$4
} END{
  printf "connections_requested=%d\n", '"$CONNS"'
  printf "baseline_rss_kb=%d\n", base
  printf "peak_rss_kb=%d\n", peak
  printf "peak_delta_kb_per_conn=%.2f\n", (peak-base)/'"$CONNS"'
  printf "peak_client_rss_kb=%d\n", client
  printf "min_mem_available_kb=%d\n", min
  printf "raw_csv=%s\n", "'"$CSV"'"
}' "$CSV"

echo "--- tcpkali ---"
grep -E 'connections_initiated|connections_accepted|connection_failures|connection_timeouts|open connections|Total data|Message latency|Marker latency|latency_95|latency_99|Bandwidth' /tmp/encryl-tcpkali.log | tail -40 || true
echo "--- server log ---"
grep -E 'stats:|listening' /tmp/encryl-scale-server.log | tail -5 || true
