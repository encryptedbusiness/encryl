#!/usr/bin/env bash
# Run a command on the Linux target, from macOS.
#
# Default target: the Lima VM from deploy/lima.yaml.
# Override: ENCRYL_LINUX_HOST=user@host (requires SSH access and the repo at the
# same absolute path).
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <command> [args...]" >&2
  exit 2
fi

if [ -n "${ENCRYL_LINUX_HOST:-}" ]; then
  quoted=$(printf '%q ' "$@")
  # shellcheck disable=SC2029
  exec ssh -t "$ENCRYL_LINUX_HOST" "cd $(printf '%q' "$PWD") && $quoted"
fi

# Flags must precede the instance name: limactl shell [flags] INSTANCE [COMMAND...]
exec limactl shell --tty=false --workdir "$PWD" "${LIMA_NAME:-encryl-dev}" "$@"
