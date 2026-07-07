# Cave Discipline

- Use cave-tools MCP (`cave__read`, `cave__bash`, `cave__grep`, `cave__find`, `cave__ls`, `cave__write`, `cave__edit`, `cave__apply_patch`, `cave__websearch`, `cave__compress`). Prefer over built-in Read/Grep/Glob/Bash.
- Bash outside cave-tools requires RTK: prefix `rtk`. If `rtk` errors, use `rtk proxy <cmd>` (raw, unfiltered).
- Use codebase-memory-mcp (`search_graph`, `trace_path`, `get_code_snippet`, `query_graph`). Index the project at init with `index_repository` before querying.
- If any MCP server is not yet initialized, wait for init before proceeding.
- Any subagent spawned must follow these rules too.
