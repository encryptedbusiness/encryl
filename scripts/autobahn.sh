#!/usr/bin/env bash
# Run the Autobahn WebSocket server suite against the echo example.
#
# Requires Docker. The server is binary-only, so text/UTF-8 and compression
# cases are excluded; those are expected rejects in M1.
#
# The report is copied to docs/benchmarks/autobahn-<date>/ so the result is
# reproducible after the temporary directory is removed.
set -euo pipefail

ADDR=${ADDR:-0.0.0.0:18080}
BIN=${BIN:-/tmp/encryl-echo}
OUT=${OUT:-docs/benchmarks/autobahn-$(date +%F)}
DIR=$(mktemp -d)
PID=""

cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  rm -rf "$DIR"
}
trap cleanup EXIT

go build -o "$BIN" ./examples/echo
"$BIN" -addr "$ADDR" >/tmp/encryl-autobahn-server.log 2>&1 &
PID=$!
sleep 1

cat >"$DIR/fuzzingclient.json" <<'JSON'
{
  "outdir": "./reports",
  "servers": [
    {
      "agent": "encryl-v2",
      "url": "ws://127.0.0.1:18080"
    }
  ],
  "cases": ["*"],
  "exclude-cases": ["6.*", "12.*", "13.*"],
  "exclude-agent-cases": {}
}
JSON

sed -i "s/\"\*\"/\"${CASES:-*}\"/" "$DIR/fuzzingclient.json"
mkdir -p "$DIR/reports"
docker run --rm --network host --user "$(id -u):$(id -g)" --platform linux/amd64 \
  -v "$DIR:/config" \
  -v "$DIR/reports:/reports" \
  crossbario/autobahn-testsuite \
  wstest -m fuzzingclient -s /config/fuzzingclient.json

mkdir -p "$OUT"
cp -r "$DIR/reports/." "$OUT/"
echo "report: $OUT/index.html"

python3 - "$OUT/index.json" <<'PY' || true
import json, sys
p = sys.argv[1]
try:
    data = json.load(open(p))
except Exception as e:
    print("summary unavailable:", e)
    raise SystemExit
agent = next(iter(data.values()))
counts = {}
for case, results in agent.items():
    if not isinstance(results, dict):
        continue
    behavior = results.get("behavior", "?")
    counts[behavior] = counts.get(behavior, 0) + 1
print("cases:", sum(counts.values()), counts)
PY
