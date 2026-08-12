import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { stopJob } from "../runtime/jobs.js";

const SIGNALS = new Set(["SIGTERM", "SIGINT", "SIGKILL", "SIGHUP"]);

export const bashStopTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash_stop",
  description:
    "Stops a background job started with cave__bash_start by signalling its process group. Sends SIGTERM by default and escalates to SIGKILL after 3s if the process is still alive.",
  inputSchema: {
    type: "object",
    properties: {
      jobId: {
        type: "string",
        description: "Job ID from cave__bash_start",
      },
      signal: {
        type: "string",
        description: "Signal to send first: SIGTERM (default), SIGINT, SIGHUP, or SIGKILL.",
        default: "SIGTERM",
      },
    },
    required: ["jobId"],
  },
  handler: async (args) => {
    const jobId = String(args.jobId);
    const requested = String(args.signal ?? "SIGTERM");
    if (!SIGNALS.has(requested)) {
      return {
        content: [
          { type: "text", text: `Unsupported signal: ${requested}. Use one of: ${[...SIGNALS].join(", ")}` },
        ],
        isError: true,
      };
    }

    const job = stopJob(jobId, requested as NodeJS.Signals);
    if (!job) {
      return {
        content: [{ type: "text", text: `Unknown job: ${jobId}` }],
        isError: true,
      };
    }
    if (job.state !== "killed") {
      return {
        content: [
          {
            type: "text",
            text: `${job.jobId} already finished (state: ${job.state}${job.exitCode !== null ? `, exit ${job.exitCode}` : ""}).`,
          },
        ],
      };
    }
    return {
      content: [
        {
          type: "text",
          text: `${job.jobId} killed (sent ${requested} to process group ${job.pid}; SIGKILL follows in 3s if needed). Log: ${job.logPath}`,
        },
      ],
    };
  },
};
