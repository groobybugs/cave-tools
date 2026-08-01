#!/usr/bin/env bash
# preToolUse redirect: block Kiro built-ins that Cave Tools replaces.
INPUT=$(cat)

TOOL=$(echo "$INPUT" | jq -r '.tool_name // .tool // empty' 2>/dev/null)

case "$TOOL" in
  fs_read|read)
    echo "BLOCKED: Use cave__read (cave-tools MCP) instead of $TOOL." >&2
    exit 2
    ;;
  execute_bash|shell)
    echo "BLOCKED: Use cave__bash (cave-tools MCP) instead of $TOOL. Pass raw command (no rtk prefix)." >&2
    exit 2
    ;;
  grep)
    echo "BLOCKED: Use cave__grep (cave-tools MCP) instead of $TOOL." >&2
    exit 2
    ;;
  glob)
    echo "BLOCKED: Use cave__find (cave-tools MCP) instead of $TOOL." >&2
    exit 2
    ;;
esac

exit 0
