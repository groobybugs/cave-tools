---
inclusion: always
---

# Compression Tools

## Cave Tools MCP

MCP server: drop-in replacements for Kiro file/shell/search tools (token-optimized). Configured in `~/.kiro/settings/mcp.json` (`cave-tools` entry).

| Tool | Replaces | Purpose |
|------|----------|---------|
| `cave__read` | `fs_read`, `read` | Read dedup (stub on re-read) + line-budget compression |
| `cave__grep` | `grep`, shell rg/grep | Search file contents with compressed output |
| `cave__find` | `glob`, shell find/fd | Find files by glob with compressed output |
| `cave__ls` | shell ls | List directories with compressed output |
| `cave__bash` | `execute_bash`, `shell` | RTK rewrite + structured JSON/XML + line budgets |
| `cave__edit` | `fs_write` / `write` (edit) | Single-file string replace |
| `cave__write` | `fs_write` / `write` (create/overwrite) | Create or overwrite a file |
| `cave__apply_patch` | multi-file edits | Add/update/delete/move patches |
| `cave__websearch` | ad-hoc web search | Current web info (Exa/Parallel) |
| `cave__webfetch` | raw URL fetch | URL → markdown/text/html or image |
| `cave__invalidate` | — | Refresh read dedup after external edits |
| `cave__compress` | — | Manually compress large text |
| `cave__status` | — | Compression / RTK statistics |
| `cave__configure` | — | Budgets, cache reset |

Use `cave__*` instead of Kiro built-ins when available. These ARE the tools — pass paths, patterns, or commands directly. Never wrap `rtk` inside `cave__bash`.

## RTK — Rust Token Killer

Token-optimized CLI proxy. Integrated into `cave__bash` automatically.

| Command | Purpose |
|---------|---------|
| `rtk gain` | Token savings analytics |
| `rtk gain --history` | Command history with savings |
| `rtk discover` | Find missed optimization opportunities |
| `rtk proxy <cmd>` | Execute raw, unfiltered command |

`cave__bash` prepends `rtk` when available (fail-open). For shell outside cave-tools, prefix `rtk` yourself.
