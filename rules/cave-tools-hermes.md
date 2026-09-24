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
| web lookups | `cave__websearch`, `cave__webfetch` | redacted, archived if large, budget-compressed |

## Rules

- Long jobs (builds, test suites, installs) go to `cave__bash_start`: it returns a jobId immediately. Poll with `cave__bash_status` (`wait` up to 60s); stop with `cave__bash_stop`. Never `sleep`-poll inside `cave__bash`.
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash`. The `rtk-rewrite` plugin already rewrites `terminal` commands, and `cave__bash` prepends `rtk` itself.
- After editing a file outside Cave Tools, call `cave__invalidate` with the changed path(s) so the next `cave__read` returns fresh content.
- Use `cave__compress` to shrink large pasted or tool-produced text; `cave__status` for RTK availability, cache state, and savings.
- `terminal` stays available as the escape hatch — it is not blocked.
