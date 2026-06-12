#!/bin/bash
# cave-tools — statusline badge script for Claude Code.
# Reads the cave-tools mode flag + savings suffix and outputs a colored badge.
#
# Usage in ~/.claude/settings.json:
#   "statusLine": { "type": "command", "command": "bash /path/to/cave-tools-statusline.sh" }
#
# Or chained with caveman via statusline-wrapper.sh.

FLAG="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.cave-tools-active"

# Refuse symlinks — a local attacker could point the flag at ~/.ssh/id_rsa and
# have the statusline render its bytes (including ANSI escape sequences) to
# the terminal every keystroke.
[ -L "$FLAG" ] && exit 0
[ ! -f "$FLAG" ] && exit 0

# Hard-cap the read at 32 bytes and strip anything outside [a-z0-9-] — blocks
# terminal-escape injection and OSC hyperlink spoofing via the flag contents.
MODE=$(head -c 32 "$FLAG" 2>/dev/null | tr -d '\n\r' | tr '[:upper:]' '[:lower:]')
MODE=$(printf '%s' "$MODE" | tr -cd 'a-z0-9-')

# Whitelist. Anything else → render nothing rather than echo attacker bytes.
case "$MODE" in
  off|hint|enforce|strict) ;;
  *) exit 0 ;;
esac

# off → render nothing. Skill still installed but dormant.
[ "$MODE" = "off" ] && exit 0

# Gray (244) to visually distinguish from caveman orange (172).
if [ "$MODE" = "enforce" ]; then
  printf '\033[38;5;244m[CAVE-TOOLS]\033[0m'
else
  SUFFIX=$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')
  printf '\033[38;5;244m[CAVE-TOOLS:%s]\033[0m' "$SUFFIX"
fi

# Savings suffix: on by default. Opt out via CAVE_TOOLS_STATUSLINE_SAVINGS=0.
# Reads a pre-rendered string written by `cave-tools status --emit-statusline`
# so we don't spawn node on every keystroke. Same symlink/control-byte
# hardening as the flag file. Until the CLI has run at least once the suffix
# file is absent and nothing extra is rendered — safe default for fresh installs.
if [ "${CAVE_TOOLS_STATUSLINE_SAVINGS:-1}" != "0" ]; then
  SAVINGS_FILE="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.cave-tools-statusline-suffix"
  if [ -f "$SAVINGS_FILE" ] && [ ! -L "$SAVINGS_FILE" ]; then
    SAVINGS=$(head -c 64 "$SAVINGS_FILE" 2>/dev/null | tr -d '\000-\037')
    [ -n "$SAVINGS" ] && printf ' \033[38;5;244m%s\033[0m' "$SAVINGS"
  fi
fi
