---
name: cave-tools
description: >
  Token-saving file/shell tool layer (cave__read / cave__bash / cave__grep / cave__find /
  cave__ls / cave__write / cave__compress / cave__status). Auto-activates every session.
  Use when the user says "use cave-tools", invokes /cave-tools, asks to save tokens,
  or whenever file/shell operations are needed.
---

Prefer cave-tools over built-in Read/Grep/Glob/Bash. RTK rewriting + Stone Tablet JSON/XML compression + Flint Chipper line budgets + read dedup cache.

## Persistence

ACTIVE EVERY RESPONSE. No drift after many turns. Rules survive compaction via UserPromptSubmit reinforcement. Off only: `/cave-tools off` or "stop cave-tools".

Default level: **enforce**. Switch: `/cave-tools off|hint|enforce|strict`.

## Rules

- Use `cave__read` instead of Read — dedup cache + Flint Chipper line-budget compression. Since 0.2.0 also returns image files (png/jpg/jpeg/gif/webp/bmp/svg up to 5 MB) as MCP image blocks.
- Use `cave__grep`, `cave__find`, `cave__ls` instead of shell/Glob/Grep.
- Use `cave__bash` instead of Bash. Tries `rtk rewrite <cmd>` when RTK available, then applies Stone Tablet JSON/XML compression + Flint Chipper line budgets.
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash` (it already prepends rtk).
- After editing a file outside Cave Tools, call `cave__write` with the changed path(s) — no `content` — to invalidate the read dedup cache.
- Use `cave__compress` for large pasted or tool-produced text.
- Use `cave__status` to check RTK availability, cache state, savings %, budgets.
- Pass raw file paths / patterns / commands. Do not wrap cave-tools calls in extra Python/shell scripts.
- Fall back to built-in Bash only for background processes, stream monitors, or hook-sensitive stdin.

## Intensity

| Level | What it does |
|-------|--------------|
| **off** | Skill dormant. No SessionStart injection, no per-turn reinforcement, no PreToolUse blocking |
| **hint** | Rules injected at SessionStart + reinforced every turn. No blocking — Claude self-corrects |
| **enforce** | Above + PreToolUse blocks Read/Grep/Glob with error pointing to cave-tools equivalent. Default |
| **strict** | Above + blocks built-in Edit/Write when target was not first read via cave__read. Tightest |

## Edit Safety

- Before any file edit, read the exact target path first in the current session/context.
- Built-in `Update` / `Edit` tools require the same file path to be read earlier; otherwise they fail with `File must be read first`.
- In Plan Mode / read-only phase, never call write-capable tools: `Update`, `Edit`, `apply_patch`, `cave__edit`, `cave__write`, or shell commands that modify files.
- Do not batch read and edit calls in parallel. Read must complete before edit.
- Prefer `cave__read` for inspection, then `cave__edit` or built-in `Edit` for changes.
- Use `cave__write` without `content` only to invalidate cache after edits made outside Cave Tools. Passing `content: ""` writes an empty file.

## Examples

Not: `Read("/etc/hosts")`
Yes: `cave__read({file_path: "/etc/hosts"})`

Not: `Bash("git status && git diff")`
Yes: `cave__bash({command: "git status && git diff"})`

Not: `Grep("pattern", "src/")`
Yes: `cave__grep({pattern: "pattern", path: "src/"})`

Not: editing `foo.ts` with built-in Edit after `cave__read` saw a cached stub
Yes: `cave__read` returns stub → edit with built-in Edit → call `cave__write({file_paths:["/abs/foo.ts"]})` to invalidate cache

## Savings

Run `cave-tools status` (CLI) or `cave__status` (MCP) for reduction %, hit rate, RTK rewrites, budgets. The statusline badge shows the live summary as `[CAVE-TOOLS] ↓X% • Yk tok`.
