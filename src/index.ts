import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  type CallToolResult,
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
import { pruneDeadSessions } from "./compression/utils.js";
import { pruneJobs } from "./runtime/jobs.js";
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

server.setRequestHandler(ListToolsRequestSchema, async () => {
  return { tools };
});

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args } = request.params;

  // Caller session id from MCP _meta (see cave__read case). Shared by the
  // background-job tools so job listing is scoped per-session.
  const requestSessionId = String(
    (request.params as { _meta?: { sessionID?: unknown } })._meta?.sessionID ?? "default",
  );

  switch (name) {
    case "cave__read": {
      // Read the caller's session id from MCP _meta (sent by opencode after the
      // mcp/catalog.ts patch). Used to key the dedup cache per-session so
      // subagents (which share this MCP process) get fresh content. Falls back
      // to "default" for clients that don't send _meta.
      const meta = (request.params as { _meta?: { sessionID?: unknown } })
        ?._meta;
      const sessionId = String(meta?.sessionID ?? "default");
      return readTool.handler({
        ...(args as Record<string, unknown>),
        __sessionId: sessionId,
      }) as Promise<CallToolResult>;
    }
    case "cave__bash":
      return bashTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__bash_start":
      return bashStartTool.handler({
        ...(args as Record<string, unknown>),
        __sessionId: requestSessionId,
      }) as Promise<CallToolResult>;
    case "cave__bash_status":
      return bashStatusTool.handler({
        ...(args as Record<string, unknown>),
        __sessionId: requestSessionId,
      }) as Promise<CallToolResult>;
    case "cave__bash_stop":
      return bashStopTool.handler({
        ...(args as Record<string, unknown>),
        __sessionId: requestSessionId,
      }) as Promise<CallToolResult>;
    case "cave__write":
      return writeTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__invalidate":
      return invalidateTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__edit":
      return editTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__apply_patch":
      return applyPatchTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__websearch":
      return websearchTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__webfetch":
      return webfetchTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__compress":
      return compressTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__status":
      return statusTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__configure":
      return configureTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__grep":
      return grepTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__find":
      return findTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__ls":
      return lsTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    default:
      throw new Error(`Unknown tool: ${name}`);
  }
});

export async function startServer(): Promise<void> {
  await pruneDeadSessions();
  pruneJobs();
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("Cave Tools MCP server running on stdio");
}

export { server };
