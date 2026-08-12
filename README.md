# Cave Tools

> A caveman needs their tools.

Cave Tools is a standalone MCP server extracted from [Caveman Code](https://github.com/JuliusBrussee/caveman-code). It gives any MCP-capable agent its token-saving file and shell tools — no TUI or agent runtime required.

Published package: [`@groobybugs/cave-tools`](https://www.npmjs.com/package/@groobybugs/cave-tools)

Keep your favorite agent. Give it better tools.

## What This Is

Cave Tools implements the portable token-optimization tool layer from Caveman Code. The MCP tools are optimized drop-in replacements for the built-ins — same results, fewer tokens. Internally that uses these layers:

| Layer         | What it does                                                                                                                 |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| RTK           | Optional Rust command rewriter. `cave__bash` tries `rtk rewrite <command>` when `rtk` is available, with fail-open fallback. |
| Stone Tablet  | Compresses large JSON/XML output while preserving useful structure.                                                          |
| Flint Chipper | Strips ANSI, collapses blank lines, and applies per-tool line budgets.                                                       |
| Read Dedup    | Fingerprints files per session. Re-reading unchanged files returns a stub instead of full content.                           |

## Install

```bash
pnpm add -g @groobybugs/cave-tools
```

Run the MCP server:

```bash
cave-tools mcp
```

For local development from source:

```bash
pnpm install
pnpm run build
node dist/cli.js mcp
```

## MCP Config

Use this command/args pair in MCP clients:

```json
{
  "mcpServers": {
    "cave-tools": {
      "command": "cave-tools",
      "args": ["mcp"]
    }
  }
}
```

For a local checkout instead of the published package, use:

```json
{
  "mcpServers": {
    "cave-tools": {
      "command": "node",
      "args": ["/path/to/cave-tools/dist/cli.js", "mcp"]
    }
  }
}
```

Install/remove global agent wiring from this checkout:

```bash
pnpm run install:agents
pnpm run remove:agents
pnpm run install:agents -- --list
pnpm run install:agents -- --dry-run --verbose --all
pnpm run install:agents -- --agent claude,codex,gemini,grok,zcode
pnpm run install:agents -- --all --with-extra-rules
pnpm run remove:agents -- --dry-run --verbose --agent antigravity,antigravity-backup
pnpm run remove:agents -- --all
pnpm run remove:agents -- --all --with-extra-rules
./install.sh --agent grok --dry-run --verbose
```

Installer/remover detect installed clients and prompt when run in an interactive terminal. Use `--all` for all detected targets, `--agent <name>` for one or more targets, `--dry-run` to print planned writes/removals without changing files, and `--verbose` to print the modified/planned file paths summary. Missing clients are skipped unless explicitly selected, then their installer still respects the target detect path.

**`--with-extra-rules`** (opt-in) also writes a second fenced block — `<!-- cave-discipline-begin -->` — into every selected target's rules file, bundling five rules: (1) prefer cave-tools MCP, (2) prefix non-cave-tools bash with `rtk` (fallback `rtk proxy`), (3) use codebase-memory-mcp and `index_repository` at init, (4) wait for MCP init before proceeding, (5) subagents must follow these rules too. Off by default — personal/team extras layered on top of the standard cave-tools block. Pass the same flag to `remove:agents` to strip only the discipline block (leaves the cave-tools block intact).

Supported targets: `claude`, `codex`, `gemini`, `antigravity`, `antigravity-cli`, `antigravity-ide`, `antigravity-shared`, `antigravity-backup`, `kiro`, `cursor`, `opencode`, `grok`, `zcode`. Run `pnpm run install:agents -- --list` to see which are detected on this machine.

Some clients use a different wrapper key:

| Client      | Global config path                                            | Shape                                          |
| ----------- | ------------------------------------------------------------- | ---------------------------------------------- |
| Claude Code | `~/.claude.json` for user/local scope, or project `.mcp.json` | `mcpServers`                                   |
| Gemini CLI  | `~/.gemini/settings.json`                                     | `mcpServers`                                   |
| Antigravity | `~/.gemini/antigravity/mcp_config.json`                       | `mcpServers`                                   |
| Antigravity CLI | `~/.gemini/antigravity-cli/mcp_config.json`              | `mcpServers`                                   |
| Antigravity IDE alt | `~/.gemini/antigravity-ide/mcp_config.json`          | `mcpServers`                                   |
| Antigravity shared | `~/.gemini/config/mcp_config.json`                    | `mcpServers`                                   |
| Antigravity backup | `~/.gemini/antigravity-backup/mcp_config.json`        | `mcpServers`                                   |
| Kiro CLI    | `~/.kiro/settings/mcp.json`                                   | `mcpServers`                                   |
| Kimi Code CLI | `~/.kimi-code/mcp.json` (user), `.kimi-code/mcp.json` (project) | `mcpServers`                                 |
| Cursor      | `~/.cursor/mcp.json`                                          | `mcpServers`                                   |
| opencode    | `~/.config/opencode/opencode.json` + `plugins/cave-tools/`    | `mcp` + native plugin (per-turn reinforce + tool block) |
| Grok Build CLI | `~/.grok/config.toml`, `~/.grok/hooks/cave-tools.json`     | TOML `[mcp_servers.cave-tools]` + native hooks |
| ZCode       | `~/.agents/mcp.json` import source, plus `~/.zcode/AGENTS.md` | `mcpServers`                                   |

opencode example (installer writes MCP + plugin entry):

```json
{
  "mcp": {
    "cave-tools": {
      "type": "local",
      "command": ["cave-tools", "mcp"],
      "enabled": true
    }
  },
  "plugin": ["./plugins/cave-tools/plugin.js"]
}
```

## Tools

All tool names use the MCP names exported by the server.

| Tool              | Purpose                                                                                   | Caveman Code parity                                                 |
| ----------------- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `cave__read`      | Optimized drop-in replacement for Read (dedup + line budgets). Reads images (`.png/.jpg/.jpeg/.gif/.webp/.bmp/.svg`) as MCP image content (base64, up to 5 MB). | Based on `read`, plus standalone dedup cache + image support.       |
| `cave__bash`      | Optimized drop-in replacement for the shell tool (RTK rewrite + structured extraction + line budgets). Supports opt-in soft failures via `allowFailure`. Max timeout 10min. | Based on `bash` output behavior, not permission/TUI handling.  |
| `cave__bash_start` | Start a long command detached; returns a jobId immediately, output goes to a log file on disk. Jobs survive server restarts. | Cave Tools-specific background jobs. |
| `cave__bash_status` | Poll a background job: state, exit code, duration, log tail. `wait` (up to 60s) returns the moment the job exits. No jobId lists the session's jobs. | Cave Tools-specific background jobs. |
| `cave__bash_stop` | Stop a background job's process group (SIGTERM, SIGKILL after 3s). | Cave Tools-specific background jobs. |
| `cave__grep`      | Search file contents with `rg --json`, match limits, context lines, long-line truncation. | Based on `grep`.                                                    |
| `cave__find`      | Find files by glob with ripgrep-backed search, Node fallback, relative paths, result limit. | Based on `glob` / search.                                           |
| `cave__ls`        | List directory entries, directories first, sorted, directories suffixed with `/`, paginated. | Based on directory read/list behavior.                              |
| `cave__edit`      | Str-replace (fuzzy), line-range (`start_line`/`end_line`/`content`), `delete`, or `insert_before` move. Optional `expected_hash` / `expected_range_checksum`. Multi-file `edits[]`. Auto-invalidates dedup cache. | Cave Tools-specific edit tool.                                      |
| `cave__write`     | Write a single file (create/overwrite, or `truncate` to empty). Auto-invalidates dedup cache. | Cave Tools-specific write tool.                                     |
| `cave__apply_patch` | Apply add/update/delete/move patches with verification before any disk write; preserves UTF-8 BOM and invalidates the read dedup cache for changed paths. Write-time failures after verification may leave partial state. | Based on `apply_patch`, with Cave cache integration. |
| `cave__websearch` | Search current web via Exa/Parallel MCP backends, then redact/archive/budget-compress output. | Based on `websearch`, with Cave compression.                         |
| `cave__webfetch`  | Fetch a URL and return markdown/text/html (or a base64 image block); output is redacted, archived if large, and budget-compressed. | Based on `webfetch`, with Cave compression.                          |
| `cave__invalidate`| Invalidate the read dedup cache for one or more paths without touching disk.              | Cave Tools-specific cache tool.                                    |
| `cave__compress`  | Optimize arbitrary text down to fewer tokens (structured extraction + line budgets).     | Cave Tools-specific helper.                                         |
| `cave__status`    | Show RTK availability, cache stats, and budgets.                                          | Cave Tools-specific helper.                                         |
| `cave__configure` | Change line budgets for the current MCP session.                                          | Cave Tools-specific helper.                                         |

## Usage Guidance For Agents

Use this instruction block in agent rules:

```md
# Cave Tools MCP

- Use `cave__read` instead of the built-in read tool for file reads. Optimized drop-in replacement (read dedup + line budgets).
- Use `cave__grep`, `cave__find`, and `cave__ls` instead of shell commands for file exploration when available. Optimized drop-in replacements that respect ignore rules and trim output to a line budget.
- Use `cave__bash` instead of the built-in shell tool for commands. Optimized drop-in replacement: tries RTK command rewriting when `rtk` is available, then structured JSON/XML extraction and output budgets. Max timeout 10min.
- Use `cave__bash_start` for commands expected to exceed ~2-3min (builds, test suites, installs): runs detached, returns a jobId immediately. Poll with `cave__bash_status` (`wait` up to 60s — returns the moment the job exits); stop with `cave__bash_stop`. Never `sleep`-poll inside `cave__bash`.
- Use `cave__edit` / `cave__write` for single-file changes and `cave__apply_patch` for multi-file add/update/delete/move patches. For medium+ hunks prefer line-range: `cave__read` with `line_numbers=true`, then `cave__edit` with `start_line`/`end_line`/`content` (+ `expected_hash` from the footer).
- Use `cave__websearch` for current web information when a local web search tool is needed; results are redacted, archived if large, and budget-compressed.
- Use `cave__webfetch` to fetch a specific URL and return it as markdown/text/html (or a base64 image block); output is redacted, archived if large, and budget-compressed.
- After using any edit/write tool outside Cave Tools, call `cave__invalidate` with the changed file path(s) to refresh the read dedup cache.
- Use `cave__compress` to optimize large pasted or tool-produced text down to fewer tokens.
- Use `cave__status` to check RTK availability, cache state, and budget settings when the user asks about savings.
- These tools are the optimized replacement layer. Do not wrap them in extra Python/scripts; pass file paths, search patterns, or commands directly.

## Edit Safety
- In Plan Mode / read-only phase, never call write-capable tools: `Update`, `Edit`, `apply_patch`, `cave__edit`, `cave__write`, or shell commands that modify files.
- Before any file edit outside Plan Mode, read the exact target path first.
- Built-in `Update` / `Edit` requires the same file path to be read earlier in the session; otherwise it fails with `File must be read first`.
- Do not batch read and edit calls in parallel. Read must complete before edit.
- Prefer `cave__read` for inspection, then `cave__edit`, `cave__apply_patch`, or `apply_patch` for edits.
- `cave__write` always writes to disk (`content`, or `truncate: true` for an empty file). For cache-only invalidation after external edits, use `cave__invalidate`.
```

## Enforcing Cave Tools (Hooks / Permissions)

Instruction blocks are advisory — agents may still reach for the built-in `Read`, `Grep`, `Glob`, or `Bash` tools out of habit. To make Cave Tools the only viable path, use the host's hook / permission system to block the built-ins. Output flows back through `cave__*` automatically because the agent has nowhere else to go.

### Claude Code (`~/.claude/settings.json`)

Claude Code supports a `PreToolUse` hook that can deny a tool call by exiting with status `2` and emitting a message to stderr. The installer wires a single mode-aware redirect group; this section documents the equivalent manual setup.

1. Register the hooks in `~/.claude/settings.json`:

   ```json
   {
     "hooks": {
       "PreToolUse": [
         {
           "matcher": "Read|Grep|Glob|Edit|Write",
           "hooks": [
             {
               "type": "command",
               "command": "/home/you/.claude/hooks/cave-tools-redirect.sh",
               "timeout": 3
             }
           ]
         }
       ],
       "SessionStart": [
         {
           "hooks": [
             { "type": "command", "command": "cave-tools status --emit-statusline", "timeout": 3, "async": true }
           ]
         }
       ],
       "PostToolUse": [
         {
           "matcher": "mcp__cave-tools__.*",
           "hooks": [
             { "type": "command", "command": "cave-tools status --emit-statusline", "timeout": 3, "async": true }
           ]
         }
       ]
     }
   }
   ```

   The single `PreToolUse` matcher `Read|Grep|Glob|Edit|Write` covers all built-ins Cave Tools replaces. (`cave__bash` already calls `rtk` internally, so there is no separate Bash matcher.)

2. Copy the mode-aware redirect script into place and make it executable:

   ```bash
   cp claude/hooks/cave-tools-redirect.sh ~/.claude/hooks/cave-tools-redirect.sh
   chmod +x ~/.claude/hooks/cave-tools-redirect.sh
   ```

   The script reads `$CLAUDE_CONFIG_DIR/.cave-tools-active` (defaulting to `~/.claude/.cave-tools-active`) to pick a mode:

   | Mode       | Behavior                                                                                  |
   | ---------- | ---------------------------------------------------------------------------------------- |
   | `off`      | exits 0, no blocking (skill dormant)                                                    |
   | `hint`     | exits 0, no blocking (rules injected only; model self-corrects)                         |
   | `enforce`  | blocks `Read`/`Grep`/`Glob` and points the agent at the `cave__*` equivalent (default)   |
   | `strict`   | above + blocks `Edit`/`Write` on a path that was not previously read via `cave__read`    |

   When the flag file is missing the script defaults to `enforce`, preserving the original blocker behavior. `Read` calls for image/PDF/SVG extensions pass through because `cave__read` returns those as MCP `image`/`resource` blocks and some clients handle the built-in path more directly.

3. Restart Claude Code. Built-in `Read`, `Grep`, `Glob`, and (in `strict` mode) `Edit`/`Write` now exit with a helpful error pointing the agent at the `cave__*` equivalent. The harness retries with the suggested tool automatically.

### Kimi Code CLI (`~/.kimi-code/mcp.json` + `~/.kimi-code/config.toml`)

Kimi Code CLI supports `[[hooks]]` in `config.toml` with the same blocking contract as Claude Code (exit status `2` + stderr reason), and its hook stdin payload uses the same `tool_name` / `tool_input` fields — so the Claude redirect script works unmodified. There is no installer target yet; wire it manually:

1. Add the MCP server to `~/.kimi-code/mcp.json` (user level, or `.kimi-code/mcp.json` in a project):

   ```json
   {
     "mcpServers": {
       "cave-tools": {
         "command": "cave-tools",
         "args": ["mcp"]
       }
     }
   }
   ```

2. Register the redirect hook in `~/.kimi-code/config.toml`:

   ```toml
   [[hooks]]
   event = "PreToolUse"
   matcher = "Read|Grep|Glob|Edit|Write|ReadFile|WriteFile|read_file|write_file|search_replace|MultiEdit|list_dir|ListDir"
   command = "/home/you/.claude/hooks/cave-tools-redirect.sh"
   timeout = 3
   ```

   The mode flag is the same file as Claude Code (`$CLAUDE_CONFIG_DIR/.cave-tools-active`, default `~/.claude/.cave-tools-active`; missing = `enforce`).

3. Kimi Code CLI auto-loads generic user skills from `~/.agents/skills/` (shared across tools), kimi-specific skills from `~/.kimi-code/skills/`, and global instructions from `~/.kimi-code/AGENTS.md`. Drop the Cave Tools usage block into that `AGENTS.md` for always-on guidance.

### Grok Build CLI (`~/.grok/config.toml` + `~/.grok/hooks/`)

Grok supports native MCP, skills, project rules, and blocking `PreToolUse` hooks. Install the Grok target from this checkout:

```bash
pnpm run install:agents -- --agent grok --dry-run --verbose
pnpm run install:agents -- --agent grok
```

The installer writes:

- `[mcp_servers.cave-tools]` in `~/.grok/config.toml`
- `~/.grok/AGENTS.md` with the Cave Tools guidance block
- `~/.grok/skills/cave-tools/SKILL.md`
- `~/.grok/hooks/cave-tools.json` plus the small hook scripts it references

Grok ignores `SessionStart` stdout, so always-on guidance lives in `AGENTS.md` and the skill. The Grok hook only uses `SessionStart` for the side effect of writing the cave-tools mode flag, then uses `PreToolUse` to deny built-in `read_file`, `grep`, and `list_dir` with a JSON `deny` reason that points the model to the matching `cave__*` tool. In `strict` mode it also blocks built-in edit/write aliases until the target has been read via `cave__read`.

Verify after install:

```bash
grok mcp list
grok mcp doctor cave-tools
grok inspect
```

### opencode (`~/.config/opencode/`)

opencode has no Claude-style `hooks.json`, but it **does** have a native plugin system ([docs](https://opencode.ai/docs/plugins/)). cave-tools installs the same shape as caveman:

| Piece | Path | Role |
|-------|------|------|
| MCP server | `opencode.json` → `mcp.cave-tools` | Tools (`cave__read`, …) |
| Native plugin | `plugins/cave-tools/plugin.js` | Mode flag, per-turn reinforce, built-in redirect |
| Always-on rules | `AGENTS.md` (fenced block) | Base ruleset every session |
| Skill | `skills/cave-tools/SKILL.md` | Discoverable skill text |
| Slash command | `command/cave-tools.md` (and `commands/` if present) | `/cave-tools off\|hint\|enforce\|strict` |
| Mode flag | `.cave-tools-active` | Written by plugin on `session.created` |

Plugin hooks (opencode ≥ 1.15):

- `event` / `session.created` → write mode flag (default `enforce`, overridable via `CAVE_TOOLS_MODE` or `~/.config/cave-tools/config.json`)
- `chat.message` → parse `/cave-tools …` and natural-language on/off
- `experimental.chat.system.transform` → per-turn reinforcement (Claude `UserPromptSubmit` twin)
- `experimental.session.compacting` → keep rules across compaction
- `tool.execute.before` → block built-in `read`/`grep`/`glob`/`list` in `enforce`/`strict` (throw with `cave__*` redirect); `strict` also requires prior `cave__read` before `edit`/`write`/`apply_patch`. `bash` stays an escape hatch.

Install:

```bash
pnpm run install:agents -- --agent opencode
# or from a published install:
# cave-tools install  # if your install path runs install:agents
```

What the installer writes into `~/.config/opencode/opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "cave-tools": {
      "type": "local",
      "command": ["node", "/path/to/cave-tools/dist/cli.js", "mcp"],
      "enabled": true
    }
  },
  "plugin": ["./plugins/cave-tools/plugin.js"]
}
```

Optional extra hard-deny via opencode `permission` (plugin already blocks in enforce/strict; this is belt-and-suspenders):

```json
"permission": {
  "read": "deny",
  "grep": "deny",
  "glob": "deny",
  "bash": "ask"
}
```

Uninstall strips MCP + plugin entry, plugin dir, skill, command, AGENTS fence, and the mode flag:

```bash
pnpm run remove:agents -- --agent opencode
```

Restart opencode after install so the plugin loads. Verify: `plugin` array contains `./plugins/cave-tools/plugin.js`, `~/.config/opencode/plugins/cave-tools/plugin.js` exists, and a new session writes `~/.config/opencode/.cave-tools-active`.

### Antigravity 2.0 / Gemini CLI

Antigravity and Gemini CLI have no `deny`/hook system — enforcement is the global rules file plus the UI tool toggles.

- Add global rules at `~/.gemini/GEMINI.md` (Antigravity's native global rules — three-dot menu in the Agent chat → **+ Global** creates it) and/or the cross-tool `~/.gemini/AGENTS.md`.
- These files do **not** support `@file` imports — inline the rules directly. A bare `@RTK.md` line is silently ignored, so paste the actual content.
- Optionally disable the built-in `Read`/`Grep`/`Glob`/`Bash` in the IDE tool toggles so the model falls back to `cave__*`.
- Antigravity caches MCP tool schemas under `~/.gemini/antigravity*/mcp/cave-tools/*.json`. Installer does not write those cache files; Antigravity recreates them after it connects to the configured MCP server. Remover deletes the cache dirs so stale tools disappear after uninstall.

### ZCode

ZCode reads user-level instructions from `~/.zcode/AGENTS.md` and user-level skills from `~/.zcode/skills/<skill-name>/SKILL.md`. It can import MCP servers from the generic external-agent config at `~/.agents/mcp.json`.

The installer target `zcode` writes:

- `~/.agents/mcp.json` with `mcpServers.cave-tools`
- `~/.zcode/AGENTS.md` with the Cave Tools guidance block
- `~/.zcode/skills/cave-tools/SKILL.md` with a ZCode-compatible Cave Tools skill

After running `pnpm run install:agents -- --agent zcode`, open ZCode and import the MCP server once:

1. Open **Settings -> MCP Servers**.
2. Click **Import**.
3. Choose **Generic `.agents`**.
4. Select `cave-tools` and import it into the user or workspace scope.

ZCode stores imported servers in its own `.zcode` configuration. The installer intentionally writes only the documented import source instead of guessing that internal file format.

### Other MCP clients

Cursor and Kiro expose tool toggles in their UI rather than a deny config — disable the built-in `Read`/`Grep`/`Glob`/`Bash` there and rely on the instruction block to point the model at `cave__*`.


## Tool Examples

Read a file:

```json
{ "file_path": "/absolute/path/to/file.ts", "offset": 1, "limit": 200 }
```

Search file contents:

```json
{ "pattern": "createServer", "path": "/repo", "glob": "*.ts", "limit": 50 }
```

Find files:

```json
{ "pattern": "**/*.test.ts", "path": "/repo", "limit": 100 }
```

List a directory:

```json
{ "path": "/repo/src", "limit": 100 }
```

Run a shell command:

```json
{
  "command": "git diff --stat",
  "description": "Show git diff stats",
  "timeout": 120000
}
```

Allow a command to fail without returning an MCP tool error:

```json
{
  "command": "test -f build.gradle && sed -n '1,80p' build.gradle",
  "description": "Read Gradle file if present",
  "allowFailure": true
}
```

When `allowFailure` is omitted or `false`, non-zero exits still return `isError: true`. Failure output includes original command, executed command, exit code, stderr, and stdout when available.

Invalidate cache after edits:

```json
{ "file_paths": ["/absolute/path/to/file.ts"] }
```

## Requirements

- Node.js 20+
- `pnpm` for development
- `rg` for `cave__grep`
- Optional: `fd` for faster `cave__find` results with full `.gitignore` behavior
- Optional: `rtk` for command rewriting in `cave__bash`

## Relationship To Caveman Code

Cave Tools is a fork/extraction of the tool layer from [Caveman Code](https://github.com/JuliusBrussee/caveman-code). It does not implement subagents, memory, editing, planning, or the TUI — only MCP tools other agents can call.

Use Caveman Code if you want the full coding agent. Use Cave Tools if you want the optimized drop-in replacement tools inside Claude Code, Gemini CLI, Antigravity, Kiro CLI, Cursor, opencode, Codex, Grok, or another MCP client.

## License

MIT © groobybugs <groobybugs299@hotmail.com>. Forked from [caveman-code](https://github.com/JuliusBrussee/caveman-code).
