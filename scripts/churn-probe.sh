#!/usr/bin/env bash
# Isolate connection churn and sample the server's fd count. A flat count means
# the close path releases descriptors; a rising count means a leak.
set -euo pipefail

SECS=${SECS:-60}
WORKERS=${WORKERS:-50}
GAP=${GAP:-2ms}
ulimit -n 1048576 2>/dev/null || true

go build -o /tmp/encryl-workload ./bench/workload
/tmp/encryl-workload -role server -workload churn -conns 400000 >/tmp/churn-server.log 2>&1 &
SRV=$!
trap 'kill "$SRV" 2>/dev/null || true' EXIT
sleep 1

/tmp/encryl-workload -role client -workload churn -workers "$WORKERS" -churn-gap "$GAP" \
  -duration "${SECS}s" >/tmp/churn-client.log 2>&1 &
CL=$!

for i in $(seq 1 $((SECS / 5))); do
  sleep 5
  echo "t=$((i * 5)) fd=$(ls "/proc/$SRV/fd" 2>/dev/null | wc -l) rss=$(ps -o rss= -p "$SRV" | tr -d ' ')"
done

wait "$CL" 2>/dev/null || true
for d in 5 10 20 30; do sleep "$d"; echo "drain +${d}s fd=$(ls "/proc/$SRV/fd" 2>/dev/null | wc -l)"; done
echo "--- fd types ---"
ls -l "/proc/$SRV/fd" 2>/dev/null | awk '{print $NF}' | sed 's/\[.*//' | sort | uniq -c | sort -rn | head
echo "--- socket states ---"
ss -tanp 2>/dev/null | grep "pid=$SRV," | awk '{print $1}' | sort | uniq -c
echo "unix_sockets=$(ss -xap 2>/dev/null | grep -c "pid=$SRV,")"
LEAK=$(ls "/proc/$SRV/fd" | tail -1)
echo "leakfd=$LEAK -> $(readlink "/proc/$SRV/fd/$LEAK")"
INO=$(readlink "/proc/$SRV/fd/$LEAK" | grep -o '[0-9]\+' | head -1)
for f in tcp tcp6 unix netlink raw udp; do printf "%s=" "$f"; grep -c " $INO " "/proc/net/$f" 2>/dev/null || true; done
echo "ss_tcp_proc=$(ss -tanp 2>/dev/null | grep -c "pid=$SRV,")"
echo "ss_tcp_total=$(ss -tan 2>/dev/null | wc -l) close_wait=$(ss -tan state close-wait 2>/dev/null | wc -l)"
echo "--- sockstat ---"; cat /proc/net/sockstat
echo "--- fd count ---"; ls "/proc/$SRV/fd" | wc -l
kill -HUP "$SRV" 2>/dev/null || true
sleep 1
echo "--- client ---"; tail -2 /tmp/churn-client.log
echo "--- server ---"; grep 'stats:' /tmp/churn-server.log | tail -1
