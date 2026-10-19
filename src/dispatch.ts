import type { CallToolResult, Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "./types.js";

// Shared CallTool dispatch for the MCP server and the CLI. Tool modules are
// imported lazily so a CLI call only loads the tool it runs.

export type ToolDefinition = Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
};

// Per-call context. Extensible: the planned daemon adds cwd/env here.
export interface CallContext {
  sessionId: string;
}

const loaders: Record<string, () => Promise<ToolDefinition>> = {
  cave__read: async () => (await import("./tools/read.js")).readTool,
  cave__bash: async () => (await import("./tools/bash.js")).bashTool,
  cave__bash_start: async () => (await import("./tools/bash-start.js")).bashStartTool,
  cave__bash_status: async () => (await import("./tools/bash-status.js")).bashStatusTool,
  cave__bash_stop: async () => (await import("./tools/bash-stop.js")).bashStopTool,
  cave__grep: async () => (await import("./tools/grep.js")).grepTool,
  cave__find: async () => (await import("./tools/find.js")).findTool,
  cave__ls: async () => (await import("./tools/ls.js")).lsTool,
  cave__write: async () => (await import("./tools/write.js")).writeTool,
  cave__invalidate: async () => (await import("./tools/invalidate.js")).invalidateTool,
  cave__edit: async () => (await import("./tools/edit.js")).editTool,
  cave__apply_patch: async () => (await import("./tools/apply-patch.js")).applyPatchTool,
  cave__websearch: async () => (await import("./tools/websearch.js")).websearchTool,
  cave__webfetch: async () => (await import("./tools/webfetch.js")).webfetchTool,
  cave__compress: async () => (await import("./tools/compress.js")).compressTool,
  cave__status: async () => (await import("./tools/status.js")).statusTool,
  cave__configure: async () => (await import("./tools/configure.js")).configureTool,
};

// Tools whose handlers read the caller session id (dedup cache, job scoping).
const SESSION_TOOLS = new Set(["cave__read", "cave__bash_start", "cave__bash_status", "cave__bash_stop"]);

export const TOOL_NAMES = Object.keys(loaders);

export function isToolName(name: string): boolean {
  return Object.prototype.hasOwnProperty.call(loaders, name);
}

export async function loadTool(name: string): Promise<ToolDefinition> {
  if (!isToolName(name)) throw new Error(`Unknown tool: ${name}`);
  return loaders[name]();
}

export async function callTool(
  name: string,
  args: Record<string, unknown> | undefined,
  ctx: CallContext,
): Promise<CallToolResult> {
  const tool = await loadTool(name);
  const toolArgs = SESSION_TOOLS.has(name)
    ? { ...(args ?? {}), __sessionId: ctx.sessionId }
    : (args as Record<string, unknown>);
  return tool.handler(toolArgs) as Promise<CallToolResult>;
}
