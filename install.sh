#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if ! command -v node >/dev/null 2>&1; then
  echo "error: node >=20 is required" >&2
  exit 1
fi

NODE_MAJOR=$(node -p "process.versions.node.split('.')[0]" 2>/dev/null || printf '0')
if [ "$NODE_MAJOR" -lt 20 ]; then
  echo "error: node >=20 is required (found $(node -v))" >&2
  exit 1
fi

exec node "$SCRIPT_DIR/scripts/install-agent-instructions.mjs" "$@"
