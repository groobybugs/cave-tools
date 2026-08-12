# Cave Tools MCP

Prefer Cave Tools MCP over Kiro built-ins. Same results, fewer tokens. Always on — not optional, not gated on caveman mode.

## Tool map (Kiro)

| Prefer | Instead of |
|--------|------------|
| `cave__read` | `fs_read`, `read` |
| `cave__grep` | `grep`, shell `rg`/`grep` |
| `cave__find` | `glob`, shell `find`/`fd` |
| `cave__ls` | shell `ls`, `fs_read` directory mode |
| `cave__bash` | `execute_bash`, `shell` |
| `cave__bash_start` / `cave__bash_status` / `cave__bash_stop` | long builds/tests/installs (detached job + polling, `wait` up to 60s) |
| `cave__edit` / `cave__write` | `fs_write`, `write` (single file) |
| `cave__apply_patch` | multi-file add/update/delete/move |
| `cave__websearch` | ad-hoc web search when a local tool is needed |
| `cave__webfetch` | fetch one URL → markdown/text/html (or image) |
| `cave__invalidate` | after external edits (never empty `cave__write` for cache-only) |
| `cave__compress` | large pasted or tool-produced text |
| `cave__status` | RTK availability, cache, savings |
| `cave__configure` | budgets / cache reset |

## Rules

- Use `cave__*` whenever cave-tools MCP is available. Built-ins only if MCP is down, or task needs streaming monitors or interactive stdin.
- Commands expected to exceed ~2-3min: `cave__bash_start` + poll `cave__bash_status { wait: 60 }` — never `sleep`-poll inside `cave__bash` (10min cap).
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`; pass the raw command.
- After editing a file outside Cave Tools, call `cave__invalidate` with changed path(s).
- Prefer `cave__read` then `cave__edit` / `cave__apply_patch`. Do not batch read and edit in parallel.
- MCP tool names may appear as `cave__read` or `@cave-tools/cave__read` — same tools.
