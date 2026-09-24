#!/usr/bin/env bash
# cave-tools — PreToolUse redirect hook.
# Blocks built-in Read/Grep/Glob/WebFetch/WebSearch when cave-tools mode is
# `enforce` or `strict`, pointing the model at the cave-tools equivalent.
#
# Works for Claude Code, Grok Build CLI and Gemini CLI (tri-harness):
#   - Claude stdin:  tool_name / tool_input.file_path
#   - Grok stdin:    toolName  / toolInput.file_path (or filePath / path / targetFile)
#   - Gemini stdin:  tool_name / tool_input.file_path, with hook_event_name=BeforeTool
#   - Tool aliases:  read_file≡Read, grep≡Grep, list_dir|Glob≡Glob,
#                    search_replace≡Edit|Write, web_fetch≡WebFetch,
#                    web_search≡WebSearch, run_terminal_cmd≡Bash
#   - Deny: JSON decision on stdout — Grok and Gemini both read the flat
#     {"decision":"deny","reason":...}; Claude reads
#     hookSpecificOutput.permissionDecision=deny (exit 0)
#
# Honors the flag file at $CLAUDE_CONFIG_DIR/.cave-tools-active:
#   off    → exits 0, no blocking (skill dormant)
#   hint   → exits 0, no blocking (rules injected only; model self-corrects)
#   enforce → blocks Read/Grep/Glob/WebFetch/WebSearch
#   strict → above + blocks Edit/Write when no prior cave__read of the target,
#            + blocks Bash (stream monitors and background jobs still allowed)
#
# Wire via PreToolUse matcher including both Claude and Grok names, e.g.:
#   Read|Grep|Glob|Edit|Write|WebFetch|WebSearch|Bash
#
# Pass --from-skill for the copy registered from skill frontmatter: it exits 0
# when $CLAUDE_CONFIG_DIR/.cave-tools-wired exists, so it never double-fires
# alongside the settings.json / plugin copy of the same handler.

set -u

FROM_SKILL=0
for arg in "$@"; do
  case "$arg" in
    --from-skill) FROM_SKILL=1 ;;
  esac
done

CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"

if [ "$FROM_SKILL" = "1" ] && [ -f "$CLAUDE_DIR/.cave-tools-wired" ]; then
  exit 0
fi

INPUT=$(cat)

# Refuse symlinks on the flag, cap read, validate against whitelist.
FLAG="$CLAUDE_DIR/.cave-tools-active"
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
# Gemini CLI sends the Claude-style snake_case envelope but expects the flat
# decision object Grok uses, so it needs its own discriminator.
IS_GEMINI=0
if printf '%s' "$INPUT" | jq -e '.hook_event_name == "BeforeTool"' >/dev/null 2>&1; then
  IS_GEMINI=1
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
  read_file|ReadFile|read_many_files) TOOL=Read ;;
  grep|Grep|grep_search|search_file_content) TOOL=Grep ;;
  list_dir|ListDir|Glob|glob|list_directory) TOOL=Glob ;;
  search_replace|MultiEdit|Edit|Write|write_file|WriteFile|replace) TOOL=Edit ;;
  web_fetch|webfetch|WebFetch|fetch_url) TOOL=WebFetch ;;
  web_search|websearch|WebSearch|google_search) TOOL=WebSearch ;;
  bash|Bash|shell|run_terminal_cmd|run_shell_command) TOOL=Bash ;;
esac

deny() {
  local reason="$1"
  local json_reason
  json_reason=$(printf '%s' "$reason" | jq -Rs . 2>/dev/null)
  # No jq, no JSON: fall back to the exit-2 blocking path so a missing
  # dependency can never turn a deny into a silent allow.
  if [ -z "$json_reason" ]; then
    echo "BLOCKED: $reason" >&2
    exit 2
  fi
  # Grok PreToolUse expects a top-level decision object; Claude Code expects the
  # documented PreToolUse decision under hookSpecificOutput. Only one harness
  # reads stdout, so emit exactly one shape (stdout must hold the JSON alone).
  if [ "$IS_GROK" = "1" ] || [ "$IS_GEMINI" = "1" ]; then
    printf '%s\n' "{\"decision\":\"deny\",\"reason\":$json_reason}"
    echo "BLOCKED: $reason" >&2
    exit 2
  fi
  printf '%s\n' "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":$json_reason}}"
  # Debug-log copy only; the permissionDecisionReason above is what Claude sees.
  echo "BLOCKED: $reason" >&2
  exit 0
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
  WebFetch)
    deny "Use cave__webfetch instead of WebFetch — same page as markdown/text/html, redacted, archived if large, budget-compressed."
    ;;
  WebSearch)
    deny "Use cave__websearch instead of WebSearch — same results, redacted, archived if large, budget-compressed."
    ;;
  Bash)
    # strict tier only — cave__bash already prepends rtk, so at enforce the
    # separate `rtk hook claude` PreToolUse handler covers built-in Bash.
    [ "$MODE" != "strict" ] && exit 0
    # Explicit opt-out for the session/command.
    [ "${CAVE_TOOLS_ALLOW_BASH:-0}" = "1" ] && exit 0
    # Background jobs and stream monitors must keep the built-in: cave__bash
    # captures output to completion and would burn the turn.
    BG=$(printf '%s' "$INPUT" | jq -r '
      .tool_input.run_in_background // .toolInput.run_in_background // false
    ' 2>/dev/null)
    [ "$BG" = "true" ] && exit 0
    CMD=$(printf '%s' "$INPUT" | jq -r '
      .tool_input.command // .toolInput.command // empty
    ' 2>/dev/null)
    case "$CMD" in
      *"tail -f"*|*"watch "*|*"journalctl -f"*|*--watch*) exit 0 ;;
    esac
    deny "STRICT: use cave__bash instead of Bash — RTK rewriting + structured extraction + line budgets. For long jobs use cave__bash_start. Stream monitors (tail -f, watch, --watch, run_in_background) are still allowed here."
    ;;
  Edit)
    # strict tier only — block if the file path was not previously read via
    # cave__read. The read registry is a flat text file maintained by the
    # cave__read tool: one absolute path per line, deduplicated, persisted
    # across sessions. Fast exact match via grep -qxF.
    [ "$MODE" != "strict" ] && exit 0
    [ -z "$FPATH" ] && exit 0
    REGISTRY="$CLAUDE_DIR/cave-tools/read-registry.txt"
    [ -L "$REGISTRY" ] && exit 0
    if [ -f "$REGISTRY" ] && grep -qxF "$FPATH" "$REGISTRY" 2>/dev/null; then
      exit 0  # File was previously read via cave__read — safe to edit
    fi
    deny "STRICT: cave__read this file first before editing — keeps dedup cache coherent."
    ;;
esac

exit 0
