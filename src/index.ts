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
import { writeTool } from "./tools/write.js";
import { invalidateTool } from "./tools/invalidate.js";
import { compressTool } from "./tools/compress.js";
import { statusTool } from "./tools/status.js";
import { configureTool } from "./tools/configure.js";
import { grepTool } from "./tools/grep.js";
import { findTool } from "./tools/find.js";
import { lsTool } from "./tools/ls.js";
import { editTool } from "./tools/edit.js";
import { pruneDeadSessions } from "./compression/utils.js";

const server = new Server(
  {
    name: "cave-tools",
    version: "0.1.0",
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
  grepTool,
  findTool,
  lsTool,
  writeTool,
  invalidateTool,
  editTool,
  compressTool,
  statusTool,
  configureTool,
];

server.setRequestHandler(ListToolsRequestSchema, async () => {
  return { tools };
});

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args } = request.params;

  switch (name) {
    case "cave__read":
      return readTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
    case "cave__bash":
      return bashTool.handler(
        args as Record<string, unknown>,
      ) as Promise<CallToolResult>;
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
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("Cave Tools MCP server running on stdio");
}

export { server };
