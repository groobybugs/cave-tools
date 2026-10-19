import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  type Tool,
} from "@modelcontextprotocol/sdk/types.js";
import { filterByProfile } from "./profile.js";
import { daemonEnabled, getDaemonClient } from "./daemon-client.js";
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

// Tool modules and dispatch load lazily: with CAVE_TOOLS_DAEMON=1 this
// process is a thin client and only loads them when falling back.
async function localTools(): Promise<Tool[]> {
  return (await import("./tool-list.js")).allTools;
}

server.setRequestHandler(ListToolsRequestSchema, async () => {
  const tools = (daemonEnabled() ? await getDaemonClient().list() : null) ?? (await localTools());
  return { tools: filterByProfile(tools) };
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
  const toolArgs = args as Record<string, unknown> | undefined;
  if (daemonEnabled()) {
    const outcome = await getDaemonClient().call(name, toolArgs, {
      sessionId,
      cwd: process.cwd(),
      env: process.env,
    });
    if (outcome.sent) return outcome.result;
  }
  const { callTool } = await import("./dispatch.js");
  return callTool(name, toolArgs, { sessionId });
});

export async function startServer(): Promise<void> {
  // With the daemon, housekeeping runs there; this process stays thin.
  if (!daemonEnabled()) {
    const { isSubagentContext, pruneDeadSessions } = await import("./compression/utils.js");
    const { pruneJobs } = await import("./runtime/jobs.js");
    const { cleanupArchives } = await import("./compression/archive.js");
    // Probe env once so unknown Claude Code / subagent keys log at boot.
    void isSubagentContext();
    await pruneDeadSessions();
    pruneJobs();
    // Archive retention (24h). Fire-and-forget: a large first-run backlog must
    // not delay startup, and failures are ignored.
    void cleanupArchives().catch(() => {});
  }
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("Cave Tools MCP server running on stdio");
}

export { server };
