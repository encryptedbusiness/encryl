#!/usr/bin/env bash
# Verify that the M0 documentation set exists and is well formed.
#
# This is intentionally strict: the ADRs are the contract M1 is built against,
# so a missing Status/Date/Deciders line or a leftover TBD fails the build.
set -euo pipefail

cd "$(dirname "$0")/.."

required=(
  LICENSE
  SECURITY.md
  README.md
  docs/CONTRIBUTING.md
  docs/m0-execution-log.md
  docs/roadmap.md
  docs/architecture.md
  docs/protocol.md
  docs/threat-model.md
  docs/operations.md
  docs/dev-environment.md
  docs/migration-v0-v1.md
  docs/benchmarks/README.md
  deploy/lima.yaml
  deploy/lima-bootstrap.sh
  scripts/linux-run.sh
  .github/workflows/test.yml
  .github/workflows/security.yml
  .github/workflows/kernel-spikes.yml
)

fail=0
for f in "${required[@]}"; do
  if [ ! -f "$f" ]; then
    echo "missing: $f"
    fail=1
  fi
done

shopt -s nullglob
adrs=(docs/adr/*.md)
if [ "${#adrs[@]}" -lt 10 ]; then
  echo "expected at least 10 ADRs in docs/adr, found ${#adrs[@]}"
  fail=1
fi

for f in "${adrs[@]}"; do
  for field in Status Date Deciders; do
    if ! grep -qE "^${field}:" "$f"; then
      echo "$f: missing '$field:' line"
      fail=1
    fi
  done
  if grep -qE '\bTBD\b' "$f"; then
    echo "$f: contains TBD"
    fail=1
  fi
  if ! grep -qE '^Status: (Accepted|Proposed|Superseded|Rejected)' "$f"; then
    echo "$f: Status must be Accepted, Proposed, Superseded, or Rejected"
    fail=1
  fi
done

# Every benchmark report must record the host, kernel, and commit it was
# produced from; otherwise the number is not reproducible.
for f in docs/benchmarks/*.md; do
  case "$f" in
    */README.md) continue ;;
  esac
  for field in Host Kernel Commit; do
    if ! grep -qE "^\|? *\*?${field}" -i "$f"; then
      echo "$f: missing ${field} metadata"
      fail=1
    fi
  done
done

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "docs-check: ok (${#adrs[@]} ADRs)"
