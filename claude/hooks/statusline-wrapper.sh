#!/bin/bash
# statusline-wrapper.sh — chains caveman + cave-tools statusline badges.
#
# Claude Code only allows one statusLine block in settings.json. This wrapper
# pipes the same JSON stdin to both scripts and concatenates the output so
# both badges share a single line.
#
# Result: "[CAVEMAN] [CAVE-TOOLS] ↓42% • 18cache"
#
# Each segment script is responsible for its own sandboxing (symlink refusal,
# byte caps, mode whitelist). The wrapper does no parsing — it just forwards
# stdin and concatenates stdout.
#
# Usage in ~/.claude/settings.json:
#   "statusLine": { "type": "command", "command": "bash /path/to/statusline-wrapper.sh" }

set -u

# Capture stdin once (Claude Code pipes JSON session data on stdin).
INPUT=$(cat)

# Resolve segment script paths. Default is to look for them next to this
# wrapper, but installs that move the cave-tools hook elsewhere can override
# via env vars.
HOOK_DIR="${HOOK_DIR:-$(dirname "$0")}"
CAVEMAN_SCRIPT="${CAVEMAN_STATUSLINE_SCRIPT:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}/hooks/caveman-statusline.sh}"
CAVE_TOOLS_SCRIPT="${CAVE_TOOLS_STATUSLINE_SCRIPT:-$HOOK_DIR/cave-tools-statusline.sh}"

CAVEMAN_OUT=""
CAVE_TOOLS_OUT=""

if [ -f "$CAVEMAN_SCRIPT" ] && [ ! -L "$CAVEMAN_SCRIPT" ]; then
  CAVEMAN_OUT=$(printf '%s' "$INPUT" | bash "$CAVEMAN_SCRIPT" 2>/dev/null)
fi

if [ -f "$CAVE_TOOLS_SCRIPT" ] && [ ! -L "$CAVE_TOOLS_SCRIPT" ]; then
  CAVE_TOOLS_OUT=$(printf '%s' "$INPUT" | bash "$CAVE_TOOLS_SCRIPT" 2>/dev/null)
fi

# Concatenate with a single space between non-empty segments.
if [ -n "$CAVEMAN_OUT" ] && [ -n "$CAVE_TOOLS_OUT" ]; then
  printf '%s %s' "$CAVEMAN_OUT" "$CAVE_TOOLS_OUT"
elif [ -n "$CAVEMAN_OUT" ]; then
  printf '%s' "$CAVEMAN_OUT"
elif [ -n "$CAVE_TOOLS_OUT" ]; then
  printf '%s' "$CAVE_TOOLS_OUT"
fi
