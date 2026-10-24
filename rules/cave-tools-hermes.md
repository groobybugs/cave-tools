# Cave Tools MCP

Prefer Cave Tools MCP over Hermes built-ins. Same results, fewer tokens. Always on — not optional, not gated on caveman mode.

The `cave-tools` plugin blocks `read_file` and `search_files` at `enforce`, so reach for `cave__*` first.

## Tool map (Hermes)

| Instead of | Use | Why |
|---|---|---|
| `read_file` | `cave__read` | read dedup cache + line budgets |
| `search_files` | `cave__grep` / `cave__find` / `cave__ls` | ripgrep-backed, grouped, budgeted |
| `terminal` | `cave__bash` | structured JSON/XML extraction + line budgets (max 10min) |
| `write_file`, `patch` | `cave__write`, `cave__edit`, `cave__apply_patch` | keeps the dedup cache coherent |
| web lookups | `cave__websearch`, `cave__webfetch` | redacted, archived when trimmed, budget-compressed |

## Rules

- Hermes does not compress `terminal` output: run commands through `cave__bash`; if MCP is down, `cave-tools bash -- '<cmd>'` in `terminal`.
- Long jobs: `cave__bash_start` (returns a jobId), poll `cave__bash_status` (`wait` up to 60s), stop `cave__bash_stop`; `terminal` background only for servers/watchers that must stream. Never `sleep`-poll.
- `[cave: full output … at <path>]`: `cave__read` offset/limit or `cave__grep` that path; do not re-run. Unfiltered: `cave-tools raw <cmd>`.
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`. The `rtk-rewrite` plugin already rewrites `terminal` commands, and `cave__bash` prepends `rtk` itself.
- After editing a file outside Cave Tools, call `cave__invalidate` with the changed path(s) so the next `cave__read` returns fresh content.
- Use `cave__compress` to shrink large pasted or tool-produced text; `cave__status` for RTK availability, cache state, and savings.
- `terminal` stays available as the escape hatch — it is not blocked.
