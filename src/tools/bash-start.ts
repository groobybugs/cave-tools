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
    "Starts a long-running shell command in the background and returns immediately with a jobId. Use for builds, test suites, installs, or anything expected to exceed a couple of minutes. Output is written to a log file on disk, never to the conversation. Poll with cave__bash_status (supports wait up to 60s); stop with cave__bash_stop. Prefer this over cave__bash with sleep-polling loops.",
  inputSchema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "The shell command to execute in the background",
      },
      description: {
        type: "string",
        description: "Clear, concise description of what the command does (5-10 words)",
      },
      workdir: {
        type: "string",
        description: "Working directory to run the command in. Use this instead of 'cd'.",
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
              `Poll with cave__bash_status { "jobId": "${job.jobId}", "wait": 60 } — wait blocks up to 60s and returns the moment the job exits. Stop with cave__bash_stop.`,
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
