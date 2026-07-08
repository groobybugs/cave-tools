#!/usr/bin/env bash
# cave-tools — PreToolUse redirect hook.
# Blocks built-in Read/Grep/Glob when cave-tools mode is `enforce` or `strict`,
# pointing the model at the cave-tools equivalent.
#
# Works for Claude Code and Grok Build CLI (dual harness):
#   - Claude stdin:  tool_name / tool_input.file_path
#   - Grok stdin:    toolName  / toolInput.file_path (or filePath / path / targetFile)
#   - Tool aliases:  read_file≡Read, grep≡Grep, list_dir|Glob≡Glob,
#                    search_replace≡Edit|Write
#   - Deny: JSON decision on stdout for Grok; stderr + exit 2 for Claude
#
# Honors the flag file at $CLAUDE_CONFIG_DIR/.cave-tools-active:
#   off    → exits 0, no blocking (skill dormant)
#   hint   → exits 0, no blocking (rules injected only; model self-corrects)
#   enforce → blocks Read/Grep/Glob (the original behavior)
#   strict → above + blocks Edit/Write when no prior cave__read of the target
#
# Wire via PreToolUse matcher including both Claude and Grok names, e.g.:
#   Read|Grep|Glob|Edit|Write|read_file|grep|list_dir|search_replace

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

# Extract tool name + path from Claude or Grok event envelopes.
TOOL=$(printf '%s' "$INPUT" | jq -r '
  .tool_name // .toolName // .tool // empty
' 2>/dev/null)
IS_GROK=0
if printf '%s' "$INPUT" | jq -e 'has("toolName") or has("toolInput")' >/dev/null 2>&1; then
  IS_GROK=1
fi
FPATH=$(printf '%s' "$INPUT" | jq -r '
  .tool_input.file_path
  // .tool_input.filePath
  // .tool_input.path
  // .tool_input.target_file
  // .tool_input.targetFile
  // .tool_input.file
  // .tool_input.file_path_old
  // .tool_input.old_file_path
  // .toolInput.file_path
  // .toolInput.filePath
  // .toolInput.path
  // .toolInput.target_file
  // .toolInput.targetFile
  // .toolInput.file
  // .toolInput.file_path_old
  // .toolInput.old_file_path
  // empty
' 2>/dev/null)

# Normalize Grok / Cursor-style names to Claude canonical names used below.
case "$TOOL" in
  read_file|ReadFile) TOOL=Read ;;
  grep|Grep) TOOL=Grep ;;
  list_dir|ListDir|Glob|glob) TOOL=Glob ;;
  search_replace|MultiEdit|Edit|Write|write_file|WriteFile) TOOL=Edit ;;
esac

deny() {
  local reason="$1"
  # Grok PreToolUse requires explicit deny JSON on stdout; keep Claude stdout
  # clean so existing exit-2/stderr UX does not regress.
  if [ "$IS_GROK" = "1" ]; then
    printf '%s\n' "{\"decision\":\"deny\",\"reason\":$(printf '%s' "$reason" | jq -Rs .)}"
  fi
  echo "BLOCKED: $reason" >&2
  exit 2
}

case "$TOOL" in
  Read)
    # Built-in Read still handles images directly for some clients — let
    # those through, redirect text files to cave__read.
    case "$FPATH" in
      *.png|*.jpg|*.jpeg|*.gif|*.webp|*.bmp|*.ico|*.pdf|*.svg) exit 0 ;;
    esac
    deny "Use cave__read instead of Read/read_file — optimized drop-in (dedup + line budgets)."
    ;;
  Grep)
    deny "Use cave__grep instead of Grep/grep — optimized drop-in (ripgrep + line budgets)."
    ;;
  Glob)
    deny "Use cave__find or cave__ls instead of Glob/list_dir — optimized drop-in (ripgrep-backed search)."
    ;;
  Edit)
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
    deny "STRICT: cave__read this file first before editing — keeps dedup cache coherent."
    ;;
esac

exit 0
