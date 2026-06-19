#!/usr/bin/env bash
# cave-tools — PreToolUse redirect hook.
# Blocks built-in Read/Grep/Glob when cave-tools mode is `enforce` or `strict`,
# pointing the model at the cave-tools equivalent.
#
# Honors the flag file at $CLAUDE_CONFIG_DIR/.cave-tools-active:
#   off    → exits 0, no blocking (skill dormant)
#   hint   → exits 0, no blocking (rules injected only; model self-corrects)
#   enforce → blocks Read/Grep/Glob (the original behavior)
#   strict → above + blocks Edit/Write when no prior cave__read of the target
#
# Wire it via PreToolUse with matcher "Read|Grep|Glob|Edit|Write" so the strict
# tier can intercept Edit/Write too. The script ignores tools it doesn't care
# about for the active mode, so a single matcher works for both modes.

set -u

INPUT=$(cat)

# Refuse symlinks on the flag, cap read, validate against whitelist.
FLAG="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.cave-tools-active"
MODE=""
if [ -f "$FLAG" ] && [ ! -L "$FLAG" ]; then
  RAW=$(head -c 32 "$FLAG" 2>/dev/null | tr -d '\n\r' | tr '[:upper:]' '[:lower:]')
  RAW=$(printf '%s' "$RAW" | tr -cd 'a-z0-9-')
  case "$RAW" in
    off|hint|enforce|strict) MODE="$RAW" ;;
  esac
fi

# Default to enforce when the flag is missing (back-compat with existing
# installs that never wrote the flag — preserves the original blocker).
[ -z "$MODE" ] && MODE="enforce"

# Modes that perform no blocking.
case "$MODE" in
  off|hint) exit 0 ;;
esac

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
FPATH=$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // empty' 2>/dev/null)

case "$TOOL" in
  Read)
    # Built-in Read still handles images directly for some clients — let
    # those through, redirect text files to cave__read.
    case "$FPATH" in
      *.png|*.jpg|*.jpeg|*.gif|*.webp|*.bmp|*.ico|*.pdf|*.svg) exit 0 ;;
    esac
    echo "BLOCKED: Use cave__read instead of Read — it's the optimized drop-in replacement (dedup + line budgets)." >&2
    exit 2
    ;;
  Grep)
    echo "BLOCKED: Use cave__grep instead of Grep — it's the optimized drop-in replacement (ripgrep + line budgets)." >&2
    exit 2
    ;;
  Glob)
    echo "BLOCKED: Use cave__find instead of Glob — it's the optimized drop-in replacement (fd-based search)." >&2
    exit 2
    ;;
  Edit|Write)
    # strict tier only — block if the file path was not previously read via
    # cave__read. The read registry is a flat text file maintained by the
    # cave__read tool: one absolute path per line, deduplicated, persisted
    # across sessions. Fast exact match via grep -qxF.
    [ "$MODE" != "strict" ] && exit 0
    [ -z "$FPATH" ] && exit 0
    REGISTRY="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/cave-tools/read-registry.txt"
    [ -L "$REGISTRY" ] && exit 0
    if [ -f "$REGISTRY" ] && grep -qxF "$FPATH" "$REGISTRY" 2>/dev/null; then
      exit 0  # File was previously read via cave__read — safe to edit
    fi
    echo "BLOCKED (strict): cave__read this file first before editing — keeps dedup cache coherent." >&2
    exit 2
    ;;
esac

exit 0
