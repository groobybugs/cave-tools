# Cave Tools

> A caveman needs their tools.

Cave Tools is a standalone MCP server extracted from [Caveman Code](https://github.com/JuliusBrussee/caveman-code). It gives any MCP-capable agent the token-saving file and shell tools from Caveman Code without adopting the full Caveman Code TUI or agent runtime.

Published package: [`@groobybugs/cave-tools`](https://www.npmjs.com/package/@groobybugs/cave-tools)

Keep your favorite agent. Give it better tools.

## What This Is

Cave Tools implements the portable compression/tool layer from Caveman Code:

| Layer         | What it does                                                                                                                 |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| RTK           | Optional Rust command rewriter. `cave__bash` tries `rtk rewrite <command>` when `rtk` is available, with fail-open fallback. |
| Stone Tablet  | Compresses large JSON/XML output while preserving useful structure.                                                          |
| Flint Chipper | Strips ANSI, collapses blank lines, and applies per-tool line budgets.                                                       |
| Read Dedup    | Fingerprints files per session. Re-reading unchanged files returns a stub instead of full content.                           |

This is not the full Caveman Code agent. It does not implement subagents, memory, editing, planning, or the TUI. It implements MCP tools that other agents can call.

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

Some clients use a different wrapper key:

| Client      | Global config path                                            | Shape                                          |
| ----------- | ------------------------------------------------------------- | ---------------------------------------------- |
| Claude Code | `~/.claude.json` for user/local scope, or project `.mcp.json` | `mcpServers`                                   |
| Gemini CLI  | `~/.gemini/settings.json`                                     | `mcpServers`                                   |
| Antigravity | `~/.gemini/antigravity/mcp_config.json`                       | `mcpServers`                                   |
| Kiro CLI    | `~/.kiro/settings/mcp.json`                                   | `mcpServers`                                   |
| Cursor      | `~/.cursor/mcp.json`                                          | `mcpServers`                                   |
| opencode    | `~/.config/opencode/opencode.json`                            | `mcp` with `type: "local"` and `command` array |

opencode example:

```json
{
  "mcp": {
    "cave-tools": {
      "type": "local",
      "command": ["cave-tools", "mcp"],
      "enabled": true
    }
  }
}
```

## Tools

All tool names use the MCP names exported by the server.

| Tool              | Purpose                                                                                   | Caveman Code parity                                                 |
| ----------------- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `cave__read`      | Read files with dedup + Flint Chipper. Reads images (`.png/.jpg/.jpeg/.gif/.webp/.bmp/.svg`) as MCP image content (base64, up to 5 MB). | Based on `read`, plus standalone dedup cache + image support.       |
| `cave__bash`      | Run shell commands with RTK rewrite + Stone Tablet + Flint Chipper.                       | Based on `bash` compression behavior, not permission/TUI handling.  |
| `cave__grep`      | Search file contents with `rg --json`, match limits, context lines, long-line truncation. | Based on `grep`.                                                    |
| `cave__find`      | Find files by glob with `fd` when available, Node fallback, relative paths, result limit. | Based on `find`.                                                    |
| `cave__ls`        | List directory entries, sorted, directories suffixed with `/`.                            | Based on `ls`.                                                      |
| `cave__write`     | Invalidate read dedup cache after edits/writes.                                           | Cave Tools-specific cache tool, not Caveman Code's file-write tool. |
| `cave__compress`  | Compress arbitrary text through Stone Tablet + Flint Chipper.                             | Cave Tools-specific helper.                                         |
| `cave__status`    | Show RTK availability, cache stats, and budgets.                                          | Cave Tools-specific helper.                                         |
| `cave__configure` | Change line budgets for the current MCP session.                                          | Cave Tools-specific helper.                                         |

## Usage Guidance For Agents

Use this instruction block in agent rules:

```md
# Cave Tools MCP

- Use `cave__read` instead of the built-in read tool for file reads. It applies read dedup and Flint Chipper line-budget compression.
- Use `cave__grep`, `cave__find`, and `cave__ls` instead of shell commands for file exploration when available. They respect ignore rules where the underlying tools do and return compressed output.
- Use `cave__bash` instead of the built-in shell tool for commands. It tries RTK command rewriting when `rtk` is available, then applies Stone Tablet JSON/XML compression and Flint Chipper output budgets.
- After using any edit/write tool outside Cave Tools, call `cave__write` with the changed file path(s) to invalidate the read dedup cache.
- Use `cave__compress` to compress large pasted or tool-produced text.
- Use `cave__status` to check RTK availability, cache state, and budget settings when the user asks about savings.
- These tools are the compression layer. Do not wrap them in extra Python/scripts; pass file paths, search patterns, or commands directly.
```

## Enforcing Cave Tools (Hooks / Permissions)

Instruction blocks are advisory — agents may still reach for the built-in `Read`, `Grep`, `Glob`, or `Bash` tools out of habit. To make Cave Tools the only viable path, use the host's hook / permission system to block the built-ins. Output flows back through `cave__*` automatically because the agent has nowhere else to go.

### Claude Code (`~/.claude/settings.json`)

Claude Code supports a `PreToolUse` hook that can deny a tool call by exiting with status `2` and emitting a message to stderr.

1. Register the hook in `~/.claude/settings.json`:

   ```json
   {
     "hooks": {
       "PreToolUse": [
         {
           "matcher": "Read|Grep|Glob",
           "hooks": [
             {
               "type": "command",
               "command": "/home/you/.claude/hooks/cave-tools-redirect.sh",
               "timeout": 3
             }
           ]
         },
         {
           "matcher": "Bash",
           "hooks": [
             { "type": "command", "command": "rtk hook claude" }
           ]
         }
       ]
     }
   }
   ```

   The `Bash` matcher is optional — it preserves RTK rewriting on built-in Bash. `cave__bash` already calls `rtk` internally, so falling all the way back to `cave__bash` is even better.

2. Drop this hook script at `~/.claude/hooks/cave-tools-redirect.sh` and make it executable (`chmod +x`):

   ```bash
   #!/usr/bin/env bash
   INPUT=$(cat)

   TOOL=$(echo "$INPUT" | jq -r '.tool_name // empty')
   FPATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')

   case "$TOOL" in
     Read)
       # Allow built-in Read for binary formats cave__read returns as base64 image blocks.
       case "$FPATH" in
         *.png|*.jpg|*.jpeg|*.gif|*.webp|*.bmp|*.svg|*.pdf) exit 0 ;;
       esac
       echo "BLOCKED: Use cave__read instead of Read. Cave-tools provides dedup + Flint Chipper compression." >&2
       exit 2
       ;;
     Grep)
       echo "BLOCKED: Use cave__grep instead of Grep. Cave-tools provides ripgrep with Flint Chipper budgets." >&2
       exit 2
       ;;
     Glob)
       echo "BLOCKED: Use cave__find instead of Glob. Cave-tools provides fd-based search with compression." >&2
       exit 2
       ;;
   esac

   exit 0
   ```

   Since `0.2.0`, `cave__read` returns image files as MCP `image` content blocks; you can drop the image allowlist branch if you want every read to flow through Cave Tools.

3. Restart Claude Code. Built-in `Read`, `Grep`, and `Glob` now exit with a helpful error pointing the agent at `cave__read` / `cave__grep` / `cave__find`. The harness retries with the suggested tool automatically.

### opencode (`~/.config/opencode/opencode.json`)

opencode does not have hooks, but its `permission` config can `deny` built-in tools. Combine with the MCP server registration shown above:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "cave-tools": {
      "type": "local",
      "command": ["cave-tools", "mcp"],
      "enabled": true
    }
  },
  "permission": {
    "read": "deny",
    "grep": "deny",
    "glob": "deny",
    "bash": "ask"
  },
  "instructions": ["~/.config/opencode/AGENTS.md"]
}
```

Then drop the same instruction block from "Usage Guidance For Agents" into `~/.config/opencode/AGENTS.md` so the model knows what to use instead. `bash` is set to `ask` rather than `deny` so the agent can still escape-hatch when `cave__bash` cannot handle a case (background processes, interactive stdin); flip to `"deny"` if you want hard enforcement.

opencode evaluates patterns last-match-wins, so you can grant exceptions:

```json
"permission": {
  "read": {
    "*": "deny",
    "*.png": "allow",
    "*.jpg": "allow"
  }
}
```

### Other MCP clients

Cursor, Kiro, Gemini CLI, and Antigravity expose tool toggles in their UI rather than a deny config — disable the built-in `Read`/`Grep`/`Glob`/`Bash` there and rely on the instruction block to point the model at `cave__*`.


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

Cave Tools is a fork/extraction of the tool layer from [Caveman Code](https://github.com/JuliusBrussee/caveman-code).

Use Caveman Code if you want the full coding agent. Use Cave Tools if you want the compression tools inside Claude Code, Gemini CLI, Antigravity, Kiro CLI, Cursor, opencode, Codex, or another MCP client.

## License

MIT © groobybugs <groobybugs299@hotmail.com>. Forked from [caveman-code](https://github.com/JuliusBrussee/caveman-code).
