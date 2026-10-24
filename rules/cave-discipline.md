# Cave Discipline

- Use cave-tools MCP (`cave__read`, `cave__bash`, `cave__grep`, `cave__find`, `cave__ls`, `cave__write`, `cave__edit`, `cave__apply_patch`, `cave__websearch`, `cave__webfetch`, `cave__compress`). Prefer over built-in Read/Grep/Glob (native shell: see below).
- `cave__apply_patch` applies add/update/delete **and move** patches; verify before any disk write.
- `cave__webfetch` fetches a single URL → markdown/text/html (or a base64 image block); `cave__websearch` queries Exa/Parallel.
- Native shell on Claude Code / opencode is auto-compressed after it runs: never prefix `rtk` or `cave-tools`. Other hosts: shell outside cave-tools needs `rtk` prefix. Raw, unfiltered: `cave-tools raw <cmd>` (or `rtk proxy <cmd>`).
- Use codebase-memory-mcp (`search_graph`, `trace_path`, `get_code_snippet`, `query_graph`). Index the project at init with `index_repository` before querying.
- If any MCP server is not yet initialized, wait for init before proceeding.
- Any subagent spawned must follow these rules too.
