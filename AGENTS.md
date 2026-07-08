# Cave Tools — token-optimized MCP

Cave Tools provides optimized drop-in replacements for shell + file ops (RTK
rewriting, structured JSON/XML extraction, per-tool line budgets). They return the
same results for fewer tokens. Prefer these over built-ins.

## Tools
- `cave__read` instead of Read — optimized drop-in replacement (dedup cache + line budgets).
- `cave__grep`, `cave__find`, `cave__ls` instead of shell/Glob/Grep.
- `cave__bash` instead of Bash for every command — optimized drop-in replacement (RTK + structured extraction + line budgets).
- `cave__write` to create/overwrite a single file; `cave__edit` for string replacement (fuzzy whitespace-tolerant matching).
- `cave__apply_patch` for multi-file add/update/delete/move patches.
- `cave__websearch` to search the web (Exa/Parallel) for current information.
- `cave__webfetch` to fetch a specific URL and return markdown/text/html (or an image block).
- `cave__compress` to optimize large pasted or tool-produced text down to fewer tokens.
- `cave__status` to inspect savings, cache hit rate, reduction % + budgets.
- `cave__configure` to set per-tool budgets, reset the dedup cache, or reset statistics.
- After editing a file outside Cave Tools, call `cave__invalidate` with the changed path(s) to refresh the read dedup cache.

## Rules
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash` (it already prepends rtk).
- Pass raw file paths / patterns / commands — do not wrap tools in extra scripts.
- Fall back to built-in Bash only for background processes, stream monitors, or hook-sensitive stdin.

## Edit Safety
- In Plan Mode / read-only phase, never call write-capable tools: `Update`, `Edit`, `apply_patch`, `cave__edit`, `cave__write`, or shell commands that modify files.
- Before any file edit outside Plan Mode, read the exact target path first.
- Built-in `Update` / `Edit` requires the same file path to be read earlier in the session; otherwise it fails with `File must be read first`.
- Do not batch read and edit calls in parallel. Read must complete before edit.
- Prefer `cave__read` for inspection, then `cave__edit` or `cave__apply_patch` for edits.
- `cave__write` always writes to disk (`content`, or `truncate: true` for an empty file). For cache-only invalidation after external edits, use `cave__invalidate`.

## Savings
Run `cave-tools status` (CLI) or `cave__status` (MCP) for reduction % and an
rtk-gain-style efficiency meter aggregated across live sessions.
