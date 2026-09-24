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

Supported targets: `claude`, `codex`, `gemini`, `antigravity`, `antigravity-cli`, `antigravity-ide`, `antigravity-shared`, `antigravity-backup`, `kiro`, `cursor`, `opencode`, `grok`, `zcode`, `hermes`. Run `pnpm run install:agents -- --list` to see which are detected on this machine.

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
| Hermes Agent | `~/.hermes/config.yaml` + `plugins/cave-tools/`               | YAML `mcp_servers` + native plugin (`pre_tool_call` block) |

opencode example (installer writes MCP + plugin entry; OpenCode 2 shape, see the opencode section below for why `codemode: false`):

```json
{
  "mcp": {
    "servers": {
      "cave-tools": {
        "type": "local",
        "command": ["cave-tools", "mcp"],
        "disabled": false,
        "codemode": false
      }
    }
  },
  "plugin": ["/home/you/.config/opencode/plugins/cave-tools"]
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
| `cave__find`      | Find files by glob (`fd` if present, else `rg --files`, else Node). Relative paths, result limit. | Based on `glob` / search.                                           |
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

1. Register the hooks in `~/.claude/settings.json` (`pnpm run install:agents` does this). Plugin installs load the same events from `claude/hooks/hooks.json` via `claude/.claude-plugin/plugin.json` (`"hooks": "./hooks/hooks.json"`); that file's event map must stay wrapped in a top-level `hooks` key, or the plugin registers nothing. Claude Code does support an `args` array (exec form, recommended for `${CLAUDE_PLUGIN_ROOT}` paths), but both wirings emit a single `command` string so every Node hook runs through the `cave-node` launcher shim, which resolves a node >= 20 instead of whatever is first on `PATH`: the installer points at the shim's absolute path, and `hooks.json` guards on `command -v cave-node` and falls back to a bare `node` where the shim is absent. The statusline hooks in `hooks.json` are guarded the same way (`command -v cave-tools`), so a plugin install without the npm bin on `PATH` is a no-op instead of a per-session hook error.

   ```json
   {
     "hooks": {
       "SessionStart": [
         {
           "hooks": [
             { "type": "command", "command": "node \"/home/you/.claude/hooks/cave-tools-activate.js\"", "timeout": 5 },
             { "type": "command", "command": "cave-tools status --emit-statusline", "timeout": 3, "async": true }
           ]
         }
       ],
       "SubagentStart": [
         {
           "hooks": [
             { "type": "command", "command": "node \"/home/you/.claude/hooks/cave-tools-subagent.js\"", "timeout": 5 }
           ]
         }
       ],
       "UserPromptSubmit": [
         {
           "hooks": [
             { "type": "command", "command": "node \"/home/you/.claude/hooks/cave-tools-mode-tracker.js\"", "timeout": 5 }
           ]
         }
       ],
       "PreToolUse": [
         {
           "matcher": "Read|Grep|Glob|Edit|Write|WebFetch|WebSearch|Bash",
           "hooks": [
             {
               "type": "command",
               "command": "/home/you/.claude/hooks/cave-tools-redirect.sh",
               "timeout": 3
             }
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
       ],
       "PostToolUseFailure": [
         {
           "matcher": "mcp__cave-tools__.*",
           "hooks": [
             { "type": "command", "command": "node \"/home/you/.claude/hooks/cave-tools-recover.js\"", "timeout": 5 }
           ]
         }
       ],
       "PermissionDenied": [
         {
           "matcher": "mcp__cave-tools__.*",
           "hooks": [
             { "type": "command", "command": "node \"/home/you/.claude/hooks/cave-tools-recover.js\"", "timeout": 5 }
           ]
         }
       ]
     }
   }
   ```

   The single `PreToolUse` matcher `Read|Grep|Glob|Edit|Write|WebFetch|WebSearch|Bash` covers every built-in Cave Tools replaces. It is letters plus `|` only, which keeps Claude Code on its exact-match path rather than evaluating it as a regex. `Bash` is only blocked in `strict` mode — at `enforce` the built-in stays usable because `rtk hook claude` already rewrites it — and even in `strict` these still pass through:

   - `run_in_background: true` (use `cave__bash_start` for long jobs instead)
   - stream monitors: `tail -f`, `watch `, `journalctl -f`, `--watch`
   - `CAVE_TOOLS_ALLOW_BASH=1` in the environment

   SessionStart / SubagentStart / UserPromptSubmit emit `hookSpecificOutput.additionalContext` JSON so Claude injects the ruleset. `PostToolUseFailure` and `PermissionDenied` run `cave-tools-recover.js`, which explains the cave-tools retry after a failed `cave__*` call and returns `retry: true` after a classifier denial — otherwise the model tends to fall back to the built-ins it was just blocked from.

   The `/cave-tools` skill also declares the same `PreToolUse` group in its frontmatter with `--from-skill`, so enforcement still applies on hosts where nothing wired `settings.json` (cloud sessions, fresh machines). That copy exits 0 as soon as `~/.claude/.cave-tools-wired` exists, so it never double-fires next to the settings copy.

2. Copy the hook scripts into place and make them executable:

   ```bash
   cp claude/hooks/cave-tools-redirect.sh ~/.claude/hooks/cave-tools-redirect.sh
   cp claude/hooks/cave-tools-recover.js ~/.claude/hooks/cave-tools-recover.js
   chmod +x ~/.claude/hooks/cave-tools-redirect.sh ~/.claude/hooks/cave-tools-recover.js
   touch ~/.claude/.cave-tools-wired
   ```

   The `.cave-tools-wired` marker is what keeps the skill-frontmatter copy of the redirect from
   double-firing; `pnpm run install:agents` writes it for you.

   The script reads `$CLAUDE_CONFIG_DIR/.cave-tools-active` (defaulting to `~/.claude/.cave-tools-active`) to pick a mode:

   | Mode       | Behavior                                                                                  |
   | ---------- | ---------------------------------------------------------------------------------------- |
   | `off`      | exits 0, no blocking (skill dormant)                                                    |
   | `hint`     | exits 0, no blocking (rules injected only; model self-corrects)                         |
   | `enforce`  | blocks `Read`/`Grep`/`Glob`/`WebFetch`/`WebSearch` and points the agent at the `cave__*` equivalent (default) |
   | `strict`   | above + blocks `Bash` (except background jobs and stream monitors) + `Edit`/`Write` on a path that was not previously read via `cave__read` |

   When the flag file is missing the script defaults to `enforce`, preserving the original blocker behavior. `Read` calls for image/PDF/SVG extensions pass through because `cave__read` returns those as MCP `image`/`resource` blocks and some clients handle the built-in path more directly.

3. Restart Claude Code. Built-in `Read`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, and (in `strict` mode) `Bash` plus `Edit`/`Write` now exit with a helpful error pointing the agent at the `cave__*` equivalent. The harness retries with the suggested tool automatically.

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
   matcher = "Read|Grep|Glob|Edit|Write|WebFetch|WebSearch|Bash|ReadFile|WriteFile|read_file|write_file|search_replace|MultiEdit|list_dir|ListDir|web_fetch|web_search|run_terminal_cmd"
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

Grok ignores `SessionStart` stdout, so always-on guidance lives in `AGENTS.md` and the skill. The Grok hook only uses `SessionStart` for the side effect of writing the cave-tools mode flag, then uses `PreToolUse` to deny built-in `read_file`, `grep`, `list_dir`, `web_fetch`, and `web_search` with a JSON `deny` reason that points the model to the matching `cave__*` tool. In `strict` mode it also blocks `run_terminal_cmd` (background jobs and stream monitors excepted) and the built-in edit/write aliases until the target has been read via `cave__read`.

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
| MCP server | `opencode.json` → `mcp.servers.cave-tools` (V2, `codemode: false`) or `mcp.cave-tools` (V1) | Tools (`cave__read`, …) |
| Native plugin | `plugins/cave-tools/index.js` → `plugin.js` | Mode flag, per-turn reinforce, built-in redirect. V2 resolves a plugin directory only via `<dir>/server` or `<dir>/index` (ignores `package.json` `main`/`exports`), so `index.js` is required |
| Always-on rules | `AGENTS.md` (fenced block) | Base ruleset every session |
| Skill | `skills/cave-tools/SKILL.md` | Discoverable skill text |
| Slash command | `command/cave-tools.md` (and `commands/` if present) | `/cave-tools off\|hint\|enforce\|strict` |
| Mode flag | `.cave-tools-active` | Written by plugin on `session.created` |

Plugin hooks (OpenCode 2; V1 `server()` hooks still exported):

- `setup` / `session.created` → write mode flag (default `enforce`, overridable via `CAVE_TOOLS_MODE` or `~/.config/cave-tools/config.json`)
- `session.hook("prompt")` → parse `/cave-tools …` and natural-language on/off
- `session.hook("context")` → per-turn reinforcement (Claude `UserPromptSubmit` twin)
- `session.hook("compaction")` → keep rules across compaction
- `tool.hook("execute.before")` → block built-in `read`/`grep`/`glob`/`list` in `enforce`/`strict` (throw with `cave__*` redirect); `strict` also requires prior `cave__read` before `edit`/`write`/`apply_patch`. `bash` stays an escape hatch.

The installer writes an **absolute** plugin path. Relative `./plugins/cave-tools/plugin.js` fails when Orca remaps `OPENCODE_CONFIG_DIR` to `~/.config/orca/opencode-hooks/shared`.

Install:

```bash
pnpm run install:agents -- --agent opencode
# or from a published install:
# cave-tools install  # if your install path runs install:agents
```

What the installer writes into `~/.config/opencode/opencode.json` on OpenCode 2 (detected via `opencode --version` or an existing `mcp.servers`):

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "cave-tools": {
        "type": "local",
        "command": ["node", "/path/to/cave-tools/dist/cli.js", "mcp"],
        "disabled": false,
        "codemode": false
      }
    }
  },
  "plugin": ["/home/you/.config/opencode/plugins/cave-tools"]
}
```

`codemode: false` matters. OpenCode 2 defaults every MCP server to Code Mode, which hides its tools behind the `execute` JavaScript dispatcher (`tools["cave-tools"].cave__read(...)`) instead of the native tool list, so agents see built-in `read`/`grep`/`glob` first. `codemode` is ignored inside a V1-shaped `mcp.<name>` entry, hence the native `mcp.servers` shape. On OpenCode 1 the installer keeps the V1 shape (`mcp.cave-tools` with `enabled: true`).

Optional extra hard-deny via opencode `permission` (plugin already blocks in enforce/strict; this is belt-and-suspenders):

```json
"permission": {
  "read": "deny",
  "grep": "deny",
  "glob": "deny",
  "bash": "ask"
}
```

OpenCode 2's built-in `explore` subagent denies every tool except `read`/`glob`/`grep`/`webfetch`/`websearch`, so it can never reach cave-tools. Append allows for the read-only tools (agent rules append; last match wins):

```json
"agents": {
  "explore": {
    "permissions": [
      { "action": "cave-tools_cave__read", "resource": "*", "effect": "allow" },
      { "action": "cave-tools_cave__grep", "resource": "*", "effect": "allow" },
      { "action": "cave-tools_cave__find", "resource": "*", "effect": "allow" },
      { "action": "cave-tools_cave__ls", "resource": "*", "effect": "allow" }
    ]
  }
}
```

Uninstall strips MCP + plugin entry, plugin dir, skill, command, AGENTS fence, and the mode flag:

```bash
pnpm run remove:agents -- --agent opencode
```

Restart opencode after install so the plugin loads (V2 also auto-reloads on config changes). Verify: `plugin` array contains the absolute `.../plugins/cave-tools` directory, `index.js` + `plugin.js` exist inside it, `opencode.log` shows `loading plugin id=.../plugins/cave-tools entrypoint=.../index.js`, and a new session writes `~/.config/opencode/.cave-tools-active`.

### Antigravity 2.0 / Gemini CLI

Antigravity 2.0 (IDE and CLI) runs `PreToolUse` hooks from `~/.gemini/config/hooks.json`, a map of named groups. The installer adds a `cave-tools` group that runs the shared redirect script (copied to `~/.gemini/config/hooks/cave-tools-redirect.sh`) on `view_file`, `grep_search`, `find_by_name`, `list_dir`, `search_web`, `read_url_content`, plus `run_command` and the edit tools for `strict`. The script reads Antigravity's `toolCall {name, args}` input and answers with the flat `{"decision":"deny","reason":...}`. The mode comes from the Claude flag file (`~/.claude/.cave-tools-active`, default `enforce`). Other groups (e.g. `orca-status`) are left alone.

Antigravity CLI defaults every MCP tool to Ask, and a subagent may not be able to answer. The installer adds `mcp(cave-tools/<tool>)` to `permissions.allow` in `~/.gemini/antigravity-cli/settings.json` for the read-only tools only (read, grep, find, ls, status, compress, invalidate, bash_status, webfetch, websearch); the CLI syncs permissions to the IDE. Antigravity calls MCP tools through a generic `call_mcp_tool` dispatcher rather than as native tools.

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
- Search CLIs resolve in order: user `PATH` (`rg`, `fd` / Debian `fdfind`) → downloaded cache (`~/.local/share/cave-tools/bin`) → Node walker for `cave__find`. `cave__grep` needs `rg`.
- Optional: `rtk` for command rewriting in `cave__bash`
- `cave-tools bins` (or `pnpm run install:agents`) downloads pinned `rg` 15.1.0 and `fd` 10.3.0 when PATH misses them

## Relationship To Caveman Code

Cave Tools is a fork/extraction of the tool layer from [Caveman Code](https://github.com/JuliusBrussee/caveman-code). It does not implement subagents, memory, editing, planning, or the TUI — only MCP tools other agents can call.

Use Caveman Code if you want the full coding agent. Use Cave Tools if you want the optimized drop-in replacement tools inside Claude Code, Gemini CLI, Antigravity, Kiro CLI, Cursor, opencode, Codex, Grok, or another MCP client.

## License

MIT © groobybugs <groobybugs299@hotmail.com>. Forked from [caveman-code](https://github.com/JuliusBrussee/caveman-code).
