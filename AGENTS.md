# Cave Tools — token-optimized MCP

Cave Tools wraps shell + file ops with RTK rewriting, Stone Tablet JSON/XML
compression, and Flint Chipper line budgets. Prefer these over built-ins.

## Tools
- `cave__read` instead of Read — dedup cache + line-budget compression.
- `cave__grep`, `cave__find`, `cave__ls` instead of shell/Glob/Grep.
- `cave__bash` instead of Bash for every command — RTK + Stone Tablet + Flint Chipper.
- `cave__compress` to compress large pasted or tool-produced text.
- `cave__status` to inspect savings, cache hit rate, reduction % + budgets.
- After editing a file outside Cave Tools, call `cave__write` to invalidate the read dedup cache.

## Rules
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash` (it already prepends rtk).
- Pass raw file paths / patterns / commands — do not wrap tools in extra scripts.
- Fall back to built-in Bash only for background processes, stream monitors, or hook-sensitive stdin.

## Edit Safety
- In Plan Mode / read-only phase, never call write-capable tools: `Update`, `Edit`, `apply_patch`, `cave__edit`, `cave__write`, or shell commands that modify files.
- Before any file edit outside Plan Mode, read the exact target path first.
- Built-in `Update` / `Edit` requires the same file path to be read earlier in the session; otherwise it fails with `File must be read first`.
- Do not batch read and edit calls in parallel. Read must complete before edit.
- Prefer `cave__read` for inspection, then `cave__edit` or `apply_patch` for edits.
- Use `cave__write` without `content` only to invalidate cache after edits made outside Cave Tools. Passing `content: ""` writes an empty file.

## Savings
Run `cave-tools status` (CLI) or `cave__status` (MCP) for reduction % and an
rtk-gain-style efficiency meter aggregated across live sessions.
