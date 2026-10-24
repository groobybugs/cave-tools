---
name: cave-tools
description: >
  Token-saving file/shell/web tool layer (cave__read / cave__bash / cave__grep / cave__find /
  cave__ls / cave__write / cave__edit / cave__apply_patch / cave__websearch / cave__webfetch / cave__compress / cave__status). Auto-activates every session.
  Use when the user says "use cave-tools", invokes /cave-tools, asks to save tokens,
  or whenever file/shell operations are needed.
hooks:
  # Fallback enforcement: registering the redirect from skill frontmatter keeps
  # PreToolUse blocking alive for the rest of the session even where neither
  # settings.json nor the plugin wired it (cloud sessions, fresh machines).
  # --from-skill makes it exit 0 when the normal wiring is already present.
  PreToolUse:
    - matcher: Read|Grep|Glob|Edit|Write|WebFetch|WebSearch|Bash
      hooks:
        - type: command
          command: bash "${CLAUDE_PLUGIN_ROOT}/hooks/cave-tools-redirect.sh" --from-skill
          timeout: 3
---

Prefer cave-tools over built-in Read/Grep/Glob (and Bash on hosts that don't compress native shell output) and use Cave Tools for compact web search when available. They are optimized drop-in replacements that return the same results for fewer tokens (RTK rewriting + structured JSON/XML extraction + per-tool line budgets + read dedup cache).

## Persistence

ACTIVE EVERY RESPONSE. No drift after many turns. Rules survive compaction via SessionStart re-injection + UserPromptSubmit reminder in hint mode (Claude Code) / `experimental.chat.system.transform` + `experimental.session.compacting` (opencode native plugin). Off only: `/cave-tools off` or "stop cave-tools".

Default level: **enforce**. Switch: `/cave-tools off|hint|enforce|strict`.

## Rules

- Use `cave__read` instead of Read — optimized drop-in replacement (dedup cache + line budgets). Since 0.2.0 also returns image files (png/jpg/jpeg/gif/webp/bmp/svg up to 5 MB) as MCP image blocks.
- Use `cave__grep`, `cave__find`, `cave__ls` instead of shell/Glob/Grep.
- Shell, Claude Code / opencode: native Bash/shell is the default in every mode. Output is auto-compressed through the CLI filter (Claude: successful commands only, via PostToolUse; opencode: completed results via `execute.after`). Never prefix `cave-tools` or `rtk` yourself. Use `cave__bash` for RTK pre-rewrite or guaranteed failure compression (max timeout 10min).
- Shell, other hosts (Codex, Grok, Gemini, Antigravity, Kiro, Hermes, Cursor, ZCode): native shell output is NOT compressed. Use `cave__bash`; if MCP is down, run `cave-tools bash -- '<cmd>'` in the native shell.
- Long jobs: native background (`run_in_background`, Monitor) for servers, watchers and streams — its output is never compressed. Use `cave__bash_start` when a long job's final output should be compressed: returns a jobId, poll `cave__bash_status` (`wait` up to 60s), stop `cave__bash_stop`. Jobs survive server restarts; `cave-tools jobs` lists/kills them. Never `sleep`-poll.
- `[cave: full output … at <path>]` means output was trimmed: `cave__read` offset/limit or `cave__grep` that path; don't re-run the command.
- Unfiltered output: `cave-tools raw <cmd>` or `CAVE_RAW=1`. Turn the native filter off with `CAVE_TOOLS_FILTER_BASH=0`. `CAVE_TOOLS_WRAP_BASH=1` rewrites native commands to `cave-tools bash` before they run — warning: permission allow/deny rules then match the rewritten command.
- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash` (it already prepends rtk).
- Use `cave__write` to create/overwrite a single file; `cave__edit` for str-replace **or** line-range edits; `cave__apply_patch` for multi-file add/update/delete/move patches.
- **Line-range (token-cheap):** `cave__read` with `line_numbers=true` (lines tagged `N:tag|`, footer has `file_hash` + `range_checksum`). Then `cave__edit` with `start_line`/`end_line`/`content` (new body only). Pass `expected_hash` and/or `expected_range_checksum`. `delete:true` removes lines; `insert_before` moves a block; batch `edits[]` may set per-item `file_path`. Insert before N: `end_line=start_line-1`. STRICT (`CAVE_TOOLS_MODE=strict`) requires a hash/checksum proof for range/move. Tiny one-liners may still use `old_string`/`new_string`.
- Use `cave__websearch` for current web information when a local web search tool is needed; results are redacted, archived when trimmed, and budget-compressed.
- Use `cave__webfetch` to fetch a specific URL and return markdown/text/html (or a base64 image block); output is redacted, archived when trimmed, and budget-compressed.
- After editing a file outside Cave Tools, call `cave__invalidate` with the changed path(s) so the next `cave__read` returns fresh content (not a stub).
- Use `cave__compress` to optimize large pasted or tool-produced text down to fewer tokens.
- Use `cave__status` to check RTK availability, cache state, savings %, budgets.
- Pass raw file paths / patterns / commands. Do not wrap cave-tools calls in extra Python/shell scripts.
- Interactive commands (vim, less, ssh, `git add -p`) and hook-sensitive stdin: native shell.

## Intensity

| Level | What it does |
|-------|--------------|
| **off** | Skill dormant. No SessionStart injection, no per-turn reinforcement, no PreToolUse blocking |
| **hint** | Rules injected at SessionStart + reinforced every turn. No blocking — Claude self-corrects |
| **enforce** | Above + PreToolUse blocks Read/Grep/Glob/WebFetch/WebSearch with error pointing to cave-tools equivalent. Default |
| **strict** | Above + blocks Edit/Write when target was not first read via cave__read. Native Bash stays allowed on Claude Code and opencode (output filtered); hosts without the filter (Grok, Gemini, Antigravity) block it except background jobs and stream monitors. Tightest |

## Edit Safety

- Before any file edit, read the exact target path first in the current session/context.
- Built-in `Update` / `Edit` tools require the same file path to be read earlier; otherwise they fail with `File must be read first`.
- In Plan Mode / read-only phase, never call write-capable tools: `Update`, `Edit`, `apply_patch`, `cave__edit`, `cave__write`, or shell commands that modify files.
- Do not batch read and edit calls in parallel. Read must complete before edit.
- Prefer `cave__read` for inspection, then `cave__edit`, `cave__apply_patch`, or built-in `Edit` for changes.
- `cave__write` always writes to disk (`content`, or `truncate: true` for an empty file). For cache-only invalidation after external edits, use `cave__invalidate` — it never touches disk.

## Examples

Not: `Read("/etc/hosts")`
Yes: `cave__read({file_path: "/etc/hosts"})`

Not: `Bash("cave-tools bash -- 'git status'")` or `Bash("rtk git status")` on Claude Code / opencode
Yes: `Bash("git status")` by default (auto-compressed); use `cave__bash({command: "git status"})` for RTK/failure compression

Not: `Grep("pattern", "src/")`
Yes: `cave__grep({pattern: "pattern", path: "src/"})`

Not: editing `foo.ts` with built-in Edit after `cave__read` saw a cached stub
Yes: `cave__read` returns stub → edit with built-in Edit → call `cave__invalidate({file_paths:["/abs/foo.ts"]})` to refresh cache

## Savings

Run `cave-tools status` (CLI) or `cave__status` (MCP) for reduction %, hit rate, RTK rewrites, budgets. The statusline badge shows the live summary as `[CAVE-TOOLS] ↓X% • Yk tok` (or `[CAVE-TOOLS] ↓X% • Ncache` on a fresh session with no tokens saved yet).

## Compact ruleset

The hooks inject only the block below at SessionStart/SubagentStart (keep it short; it costs tokens every session).

<!-- cave-tools-compact-begin -->
Prefer cave file/web/edit tools over built-ins (same results, fewer tokens):
- Read→`cave__read`, Grep→`cave__grep`, Glob→`cave__find`, ls→`cave__ls`, WebSearch/WebFetch→`cave__websearch`/`cave__webfetch`.
- Native Bash default (CLI-filtered); `cave__bash` for RTK/failures; never prefix `cave-tools`/`rtk`.
- Background for servers/watchers; long jobs: `cave__bash_start`/`_status` (`wait` ≤60s). No sleep-poll.
- `[cave: full output … at <path>]`: `cave__read`/`cave__grep` it.
- Edit: `cave__edit` old_string/new_string, or line range after `cave__read` `line_numbers=true` (pass `expected_hash`/`expected_range_checksum`). `cave__write` whole files, `cave__apply_patch` multi-file.
- `cave__invalidate` after outside edits. Read before edit, never parallel. No writes in Plan Mode.
| **hint** | Advisory; built-ins allowed. |
| **enforce** | Built-in Read/Grep/Glob/WebFetch/WebSearch are blocked. |
| **strict** | Above + Edit/Write need prior cave__read; range/move need hash. |
<!-- cave-tools-compact-end -->
