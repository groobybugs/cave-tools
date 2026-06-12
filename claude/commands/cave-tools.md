---
description: Toggle cave-tools enforcement level (off | hint | enforce | strict) or show status
argument-hint: "[off|hint|enforce|strict|status]"
disable-model-invocation: true
---

Set or inspect the cave-tools mode.

Argument: `$ARGUMENTS`

The UserPromptSubmit hook intercepts `/cave-tools <mode>` and writes the flag
file at `$CLAUDE_CONFIG_DIR/.cave-tools-active`. The next assistant turn picks
up the new mode via the per-turn reinforcement hook and the PreToolUse redirect.

- No arg or unknown arg → activate at the configured default (env `CAVE_TOOLS_MODE` or `~/.config/cave-tools/config.json`, falls back to `enforce`).
- `off` / `stop` / `disable` → remove the flag, skill dormant for the session.
- `hint` → rules injected and reinforced every turn, no PreToolUse blocking.
- `enforce` → rules injected + Read/Grep/Glob blocked with cave-tools redirect.
- `strict` → above + built-in Edit/Write blocked when no prior `cave__read` of the target.
- `status` → blocks the prompt and shows the full `cave-tools status` CLI output (savings, hit rate, RTK rewrites, budgets).

The statusline badge updates immediately to reflect the new mode.
