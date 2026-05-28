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

## Savings
Run `cave-tools status` (CLI) or `cave__status` (MCP) for reduction % and an
rtk-gain-style efficiency meter aggregated across live sessions.
