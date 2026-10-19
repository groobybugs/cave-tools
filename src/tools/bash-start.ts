import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { resolveExistingDirectory } from "../runtime/path.js";
import { startJob } from "../runtime/jobs.js";
import { redactSecrets } from "../compression/redact.js";

export const bashStartTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash_start",
  description:
    "Run a command over ~2 min (builds, tests, installs) detached; returns a jobId, output goes to a log file. Poll with cave__bash_status, stop with cave__bash_stop. Never sleep-poll in cave__bash.",
  inputSchema: {
    type: "object",
    properties: {
      command: {
        type: "string",
      },
      description: {
        type: "string",
        description: "5-10 word summary",
      },
      workdir: {
        type: "string",
        description: "Use instead of cd",
      },
    },
    required: ["command", "description"],
  },
  handler: async (args) => {
    const command = String(args.command);
    const description = String(args.description ?? "");
    const sessionId = String(args.__sessionId ?? "default");

    let cwd: string | undefined;
    if (args.workdir) {
      try {
        cwd = await resolveExistingDirectory(String(args.workdir));
      } catch {
        return {
          content: [
            { type: "text", text: `Path not found or not a directory: ${String(args.workdir)}` },
          ],
          isError: true,
        };
      }
    }

    try {
      const job = await startJob({ command, sessionId, workdir: cwd });
      const commandText = redactSecrets(command);
      return {
        content: [
          {
            type: "text",
            text: [
              `Background job started: ${description || commandText}`,
              `jobId: ${job.jobId}`,
              `pid: ${job.pid}`,
              `logPath: ${job.logPath}`,
              ``,
              `Poll with cave__bash_status { "jobId": "${job.jobId}", "wait": 60 } — wait returns on exit. Use tail_lines=0 for status only, or reuse next_cursor for new output only. Stop with cave__bash_stop.`,
            ].join("\n"),
          },
        ],
      };
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return {
        content: [
          {
            type: "text",
            text: redactSecrets(`Failed to start background job: ${message}`),
          },
        ],
        isError: true,
      };
    }
  },
};
