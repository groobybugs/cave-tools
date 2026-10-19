import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  type Tool,
} from "@modelcontextprotocol/sdk/types.js";
import { readTool } from "./tools/read.js";
import { bashTool } from "./tools/bash.js";
import { bashStartTool } from "./tools/bash-start.js";
import { bashStatusTool } from "./tools/bash-status.js";
import { bashStopTool } from "./tools/bash-stop.js";
import { writeTool } from "./tools/write.js";
import { invalidateTool } from "./tools/invalidate.js";
import { compressTool } from "./tools/compress.js";
import { statusTool } from "./tools/status.js";
import { configureTool } from "./tools/configure.js";
import { grepTool } from "./tools/grep.js";
import { findTool } from "./tools/find.js";
import { lsTool } from "./tools/ls.js";
import { editTool } from "./tools/edit.js";
import { applyPatchTool } from "./tools/apply-patch.js";
import { websearchTool } from "./tools/websearch.js";
import { webfetchTool } from "./tools/webfetch.js";
import { callTool } from "./dispatch.js";
import { isSubagentContext, pruneDeadSessions } from "./compression/utils.js";
import { pruneJobs } from "./runtime/jobs.js";
import { cleanupArchives } from "./compression/archive.js";
import { readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";

const moduleDir = dirname(fileURLToPath(import.meta.url));
const packageVersion = JSON.parse(
  readFileSync(join(moduleDir, "..", "package.json"), "utf-8"),
).version as string;

const server = new Server(
  {
    name: "cave-tools",
    version: packageVersion,
  },
  {
    capabilities: {
      tools: {},
    },
  },
);

const tools: Tool[] = [
  readTool,
  bashTool,
  bashStartTool,
  bashStatusTool,
  bashStopTool,
  grepTool,
  findTool,
  lsTool,
  writeTool,
  invalidateTool,
  editTool,
  applyPatchTool,
  websearchTool,
  webfetchTool,
  compressTool,
  statusTool,
  configureTool,
];

// CAVE_TOOLS_PROFILE=standard (default) hides rarely used tools from
// tools/list to cut per-session schema tokens. The server still dispatches
// them by name, but clients only expose listed tools, so agents need
// CAVE_TOOLS_PROFILE=full to use them.
const STANDARD_HIDDEN = new Set(["cave__configure", "cave__compress", "cave__status"]);

export function listedTools(profile = process.env.CAVE_TOOLS_PROFILE): Tool[] {
  if ((profile ?? "").trim().toLowerCase() === "full") return tools;
  return tools.filter((tool) => !STANDARD_HIDDEN.has(tool.name));
}

server.setRequestHandler(ListToolsRequestSchema, async () => {
  return { tools: listedTools() };
});

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args } = request.params;

  // Caller session id from MCP _meta (opencode sends sessionID after the
  // mcp/catalog.ts patch). Claude Code and other clients typically omit it,
  // so we fall back to "default". Subagent isolation for those clients is
  // env-based: isSubagentContext() forces fresh reads (never stub).
  const sessionId = String(
    (request.params as { _meta?: { sessionID?: unknown } })._meta?.sessionID ?? "default",
  );
  return callTool(name, args as Record<string, unknown> | undefined, { sessionId });
});

export async function startServer(): Promise<void> {
  // Probe env once so unknown Claude Code / subagent keys log at boot.
  void isSubagentContext();
  await pruneDeadSessions();
  pruneJobs();
  // Archive retention (24h). Fire-and-forget: a large first-run backlog must
  // not delay startup, and failures are ignored.
  void cleanupArchives().catch(() => {});
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("Cave Tools MCP server running on stdio");
}

export { server };
