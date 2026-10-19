import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { stopJob } from "../runtime/jobs.js";

const SIGNALS = new Set(["SIGTERM", "SIGINT", "SIGKILL", "SIGHUP"]);

export const bashStopTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash_stop",
  description:
    "Stop a cave__bash_start job by signalling its process group; escalates to SIGKILL after 3s.",
  inputSchema: {
    type: "object",
    properties: {
      jobId: {
        type: "string",
      },
      signal: {
        type: "string",
        description: "SIGTERM, SIGINT, SIGHUP, or SIGKILL",
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
