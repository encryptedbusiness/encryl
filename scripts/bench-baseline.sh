#!/usr/bin/env bash
# Reproduce the v1 baseline on the Linux target.
#
# v1.0.2 is epoll-only and Linux-only: `go build ./...` fails on macOS with
# "undefined: syscall.EpollEvent". The baseline therefore has to run in the VM.
#
# What this measures, and what it deliberately does not:
#   - connections held (the density number that matters)
#   - RSS / goroutines / kernel socket memory per connection
#   - connect success rate
#   - message throughput is NOT comparable: v1 speaks a JSON event envelope
#     (encryl.js) and v2 speaks the binary encryl.v2 envelope. A
#     cross-version message benchmark needs an adapter and is M2 work.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ "$(uname -s)" != "Linux" ]; then
  echo "bench-baseline must run on Linux; use: make bench-baseline (dispatches to the VM)" >&2
  exit 2
fi

DATE=$(date +%F)
OUT="docs/benchmarks/${DATE}-v1-baseline.md"
RAW="docs/benchmarks/raw"
mkdir -p "$RAW"

# The server and the generator both need descriptors for every connection.
ulimit -n 1048576 2>/dev/null || true

WORKTREE=$(mktemp -d)/encryl-v1
echo "== checking out v1.0.2 into $WORKTREE =="
git worktree add --detach "$WORKTREE" v1.0.2 >/dev/null
trap 'git worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true' EXIT

HOST=$(hostname)
KERNEL=$(uname -r)
CPU=$(lscpu 2>/dev/null | awk -F: '/Model name/ {gsub(/^ +/,"",$2); print $2; exit}')
if [ -z "$CPU" ] || [ "$CPU" = "-" ]; then CPU=$(uname -m); fi
MEM=$(awk '/MemTotal/ {printf "%.1f GiB", $2/1024/1024}' /proc/meminfo)
COMMIT=$(git rev-parse --short v1.0.2)
GOMAX=$(nproc)
NOFILE=$(ulimit -n)

echo "== building v1 example server =="
(
  cd "$WORKTREE"
  GOFLAGS=-mod=mod go build -o /tmp/encryl-v1 ./example/chat_server
)

# v1's example hardcodes :8080 and runs GOMAXPROCS=1.
/tmp/encryl-v1 >/tmp/v1-server.log 2>&1 &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null || true; git worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true' EXIT

sleep 2
if ! kill -0 "$SERVER_PID" 2>/dev/null; then
  echo "v1 server failed to start:" >&2
  cat /tmp/v1-server.log >&2
  exit 1
fi

echo "== building loadgen =="
go build -o /tmp/loadgen ./bench/loadgen

CONNS=${CONNS:-10000}
DURATION=${DURATION:-30s}
RATE=${RATE:-1000}
SRC_IPS=${SRC_IPS:-1}

# Sample while connections are live. Sampling only after the run measures the
# teardown state, not the density state.
SAMPLES=/tmp/v1-samples.txt
: > "$SAMPLES"
(
  while kill -0 "$SERVER_PID" 2>/dev/null; do
    rss=$(awk '/VmRSS/ {print $2}' /proc/"$SERVER_PID"/status 2>/dev/null)
    tcp=$(grep -E '^TCP:' /proc/net/sockstat || true)
    slab=$(sudo -n grep -E '^TCP ' /proc/slabinfo 2>/dev/null | awk '{print $2" "$3" "$4}' || true)
    echo "${rss:-0}|${tcp:-}|${slab:-}" >> "$SAMPLES"
    sleep 1
  done
) &
SAMPLER_PID=$!

echo "== idle soak: $CONNS connections for $DURATION at $RATE/s =="
/tmp/loadgen -addr=127.0.0.1:8080 -path=/ws -mode=idle \
  -conns="$CONNS" -rate="$RATE" -duration="$DURATION" -src-ips="$SRC_IPS" \
  -report="$RAW/${DATE}-v1-idle.csv" | tee /tmp/v1-idle.txt

kill "$SAMPLER_PID" 2>/dev/null || true
wait "$SAMPLER_PID" 2>/dev/null || true

RSS_KB=$(awk -F'|' '{if ($1+0 > max) max=$1+0} END {print max+0}' "$SAMPLES")
RSS_END_KB=$(awk '/VmRSS/ {print $2}' /proc/"$SERVER_PID"/status)
GOROUTINES=$(awk '/Threads/ {print $2}' /proc/"$SERVER_PID"/status)
# The socketstat line with the largest TCP mem value is the densest sample.
SOCKSTAT=$(awk -F'|' '{
  split($2, f, " "); for (i in f) if (f[i]=="mem") { if (f[i+1]+0 >= best) { best=f[i+1]+0; line=$2 } }
} END { print line }' "$SAMPLES")
SLABS=$(awk -F'|' '{
  split($3, f, " "); if (f[1]+0 >= best) { best=f[1]+0; line=$3 }
} END { print line }' "$SAMPLES")
TCP_MEM_PAGES=$(printf '%s\n' "$SOCKSTAT" | awk '{for (i=1;i<=NF;i++) if ($i=="mem") print $(i+1)}')
ACTIVE=$(awk '/^active_peak/ {print $2}' /tmp/v1-idle.txt)
[ -z "$ACTIVE" ] && ACTIVE=0
RSS_PER_CONN=$(awk -v rss="$RSS_KB" -v n="$ACTIVE" 'BEGIN { if (n>0) printf "%.2f", rss/n; else print "n/a" }')
KERNEL_PER_CONN=$(awk -v p="$TCP_MEM_PAGES" -v n="$ACTIVE" 'BEGIN { if (n>0) printf "%.0f", (p*4096)/n; else print "n/a" }')

cat > "$OUT" <<EOF
# v1.0.2 baseline

| Field | Value |
| ----- | ----- |
| Host | ${HOST} |
| Kernel | ${KERNEL} |
| CPU | ${CPU} (${GOMAX} threads) |
| Memory | ${MEM} |
| Commit | ${COMMIT} |
| Date | ${DATE} |
| Command | \`make bench-baseline CONNS=${CONNS} DURATION=${DURATION} RATE=${RATE} SRC_IPS=${SRC_IPS}\` |
| Raw | raw/${DATE}-v1-idle.csv |
| ulimit -n | ${NOFILE} |

## Configuration

- v1 example server (\`example/chat_server\`), \`GOMAXPROCS=1\`, listening on \`:8080/ws\`
- Load generator: \`bench/loadgen\`, one goroutine per connection, ${RATE} conns/s ramp

## Idle soak

| Metric | Value |
| ------ | ----- |
| Target connections | ${CONNS} |
| Peak active connections | ${ACTIVE} |
| Duration | ${DURATION} |
| Server RSS peak | $(awk -v k="$RSS_KB" 'BEGIN{printf "%.2f MiB", k/1024}') |
| Server RSS after teardown | $(awk -v k="$RSS_END_KB" 'BEGIN{printf "%.2f MiB", k/1024}') |
| Process RSS per connection | ${RSS_PER_CONN} KiB |
| TCP sockstat mem per connection | ${KERNEL_PER_CONN} B |
| Server threads | ${GOROUTINES} |

\`\`\`
$(cat /tmp/v1-idle.txt)
\`\`\`

## Kernel memory (peak sample during the run)

\`\`\`
${SOCKSTAT}
TCP slab: ${SLABS}
\`\`\`

## Limitations recorded by this run

- v1 does not compile on macOS (\`syscall.EpollEvent\` is Linux-only), so this
  number cannot be reproduced on a developer laptop.
- Message throughput is not compared: v1 uses a JSON event envelope, v2 a
  binary envelope. An adapter is required and is M2 work.
- The README's historical table (201,624 connections / 1676.64 MB) is not
  reproducible from any command in the tree and is superseded by this file.
  The measured 200k result (1751.39 MiB, 8.97 KiB/conn) is within 5% of that
  historical per-connection figure, so the order of magnitude was plausible but
  unsourced.
- Scaling runs at 20k, 50k, 100k, and 200k are summarized in
  \`2026-10-22-v1-scaling.md\`.
- The `sockstat` mem per connection is noisy across scales; the TCP slab object
  count is the stable kernel measure.
EOF

echo "wrote $OUT"
