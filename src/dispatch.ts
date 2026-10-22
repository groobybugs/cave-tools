import type { CallToolResult, Tool } from "@modelcontextprotocol/sdk/types.js";
import path from "path";
import type { ToolResult } from "./types.js";
import { runWithContext, type CallContext } from "./runtime/context.js";
import { observeToolCall, setKnownToolNames } from "./runtime/tool-errors.js";

export type { CallContext } from "./runtime/context.js";

// Shared CallTool dispatch for the MCP server and the CLI. Tool modules are
// imported lazily so a CLI call only loads the tool it runs.

export type ToolDefinition = Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
};

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

// Fixed allowlist for the error log: raw caller-supplied tool names are
// never persisted (they could be secrets); unknown names log as
// "unknown_tool".
setKnownToolNames(TOOL_NAMES);

export function isToolName(name: string): boolean {
  return Object.prototype.hasOwnProperty.call(loaders, name);
}

export async function loadTool(name: string): Promise<ToolDefinition> {
  if (!isToolName(name)) throw new Error(`Unknown tool: ${name}`);
  return loaders[name]();
}

// Path params made absolute against the caller's cwd, so cache, snapshot and
// edit-registry keys never depend on which client (or cwd) made the call.
const PATH_PARAMS: Record<string, string[]> = {
  cave__read: ["file_path"],
  cave__write: ["file_path"],
  cave__edit: ["file_path"],
  cave__grep: ["path"],
  cave__find: ["path"],
  cave__ls: ["path"],
};

function absolutize(value: unknown, cwd: string): unknown {
  return typeof value === "string" && value !== "" && !path.isAbsolute(value) ? path.resolve(cwd, value) : value;
}

function normalizePaths(name: string, args: Record<string, unknown> | undefined, cwd: string) {
  if (!args) return args;
  const out = { ...args };
  for (const key of PATH_PARAMS[name] ?? []) {
    if (key in out) out[key] = absolutize(out[key], cwd);
  }
  if (name === "cave__edit" && Array.isArray(out.edits)) {
    out.edits = out.edits.map((item) =>
      item && typeof item === "object" && "file_path" in item
        ? { ...item, file_path: absolutize((item as Record<string, unknown>).file_path, cwd) }
        : item,
    );
  }
  if (name === "cave__invalidate" && Array.isArray(out.file_paths)) {
    out.file_paths = out.file_paths.map((p) => absolutize(p, cwd));
  }
  return out;
}

// Missing ctx fields default to this process (in-process calls). Daemon
// clients get their session ids namespaced by clientId, so two clients never
// share read stubs, diffs or job listings.
export async function callTool(
  name: string,
  args: Record<string, unknown> | undefined,
  partial: Partial<CallContext> & { sessionId: string },
): Promise<CallToolResult> {
  const ctx: CallContext = {
    ...partial,
    cwd: partial.cwd ?? process.cwd(),
    env: partial.env ?? process.env,
    clientId: partial.clientId ?? "local",
  };
  const startedAt = Date.now();
  const namespace = ctx.sessionNamespace ?? ctx.clientId;
  const sessionId = namespace === "local" ? ctx.sessionId : `${namespace}:${ctx.sessionId}`;
  let tool: ToolDefinition;
  try {
    tool = await loadTool(name);
  } catch (error) {
    // Unknown tool: no handler ran. Observe with the same caller context
    // (env + namespaced session id) as normal failures, so a daemon
    // client's CAVE_TOOLS_ERROR_LOG=0 is honored here too. Throw unchanged.
    runWithContext({ ...ctx, sessionId }, () =>
      observeToolCall({ tool: name, args, schemaProps: [], sessionId, startedAt, env: ctx.env, thrown: error }),
    );
    throw error;
  }
  const normalized = normalizePaths(name, args, ctx.cwd);
  const toolArgs = SESSION_TOOLS.has(name)
    ? { ...(normalized ?? {}), __sessionId: sessionId }
    : (normalized as Record<string, unknown>);
  const schemaProps =
    tool.inputSchema !== null &&
    typeof tool.inputSchema === "object" &&
    (tool.inputSchema as { properties?: unknown }).properties !== null &&
    typeof (tool.inputSchema as { properties?: unknown }).properties === "object"
      ? Object.keys((tool.inputSchema as { properties: Record<string, unknown> }).properties)
      : [];
  // observeToolCall is best-effort and never mutates results: soft (returned
  // isError) and thrown outcomes are logged, successes are ignored, and
  // both result and throw behavior stay exactly as before.
  return runWithContext({ ...ctx, sessionId }, async () => {
    try {
      const result = (await tool.handler(toolArgs)) as CallToolResult;
      observeToolCall({
        tool: name,
        args: toolArgs,
        schemaProps,
        sessionId,
        startedAt,
        env: ctx.env,
        result,
      });
      return result;
    } catch (error) {
      observeToolCall({
        tool: name,
        args: toolArgs,
        schemaProps,
        sessionId,
        startedAt,
        env: ctx.env,
        thrown: error,
      });
      throw error;
    }
  });
}
