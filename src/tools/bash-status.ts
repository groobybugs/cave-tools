import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { getJobInfo, listSessionJobs, tailFileBytes, waitForJob } from "../runtime/jobs.js";
import { redactSecrets } from "../compression/redact.js";
import { applyBudget } from "../compression/utils.js";
import type { JobRecord } from "../storage/db.js";

const DEFAULT_TAIL_LINES = 50;
const MAX_WAIT_SECONDS = 60;
const TAIL_READ_BYTES = 64 * 1024;

function durationMs(job: JobRecord): number {
  return (job.endedAt ?? Date.now()) - job.startedAt;
}

function formatState(job: JobRecord): string {
  const secs = Math.round(durationMs(job) / 1000);
  switch (job.state) {
    case "running":
      return `running (${secs}s elapsed, pid ${job.pid})`;
    case "exited":
      return `exited (code ${job.exitCode ?? "?"}${job.signal ? `, signal ${job.signal}` : ""}) after ${secs}s`;
    case "killed":
      return `killed (${job.signal ?? "SIGTERM"}) after ${secs}s`;
    case "lost":
      return "lost (server restarted; exit code unrecoverable — log tail below)";
  }
}

function truncateCommand(command: string): string {
  const oneLine = command.replace(/\s+/g, " ").trim();
  return oneLine.length > 120 ? `${oneLine.slice(0, 117)}...` : oneLine;
}

export const bashStatusTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash_status",
  description:
    "Checks a background job started with cave__bash_start. Returns state (running/exited/killed/lost), exit code, duration, and a log tail. Optional 'wait' blocks server-side up to 60 seconds, returning the moment the job exits — use it instead of sleep-polling. Called without jobId, lists the session's jobs.",
  inputSchema: {
    type: "object",
    properties: {
      jobId: {
        type: "string",
        description: "Job ID from cave__bash_start. Omit to list this session's jobs.",
      },
      tail_lines: {
        type: "number",
        description: `Log lines to return (default ${DEFAULT_TAIL_LINES})`,
        default: DEFAULT_TAIL_LINES,
      },
      wait: {
        type: "number",
        description: `Seconds to block until the job exits (max ${MAX_WAIT_SECONDS}). Returns immediately if already finished.`,
      },
      redact_secrets: {
        type: "boolean",
        description: "When false, skip secret redaction on the log tail. Default true.",
        default: true,
      },
    },
  },
  handler: async (args) => {
    const sessionId = String(args.__sessionId ?? "default");
    const shouldRedact = args.redact_secrets !== false;

    if (args.jobId === undefined) {
      const jobs = listSessionJobs(sessionId);
      if (jobs.length === 0) {
        return { content: [{ type: "text", text: "No background jobs for this session." }] };
      }
      const lines = jobs.map(
        (job) => `${job.jobId}  ${formatState(job)}  ${truncateCommand(job.command)}`,
      );
      return { content: [{ type: "text", text: applyBudget(lines.join("\n"), "bash") }] };
    }

    const jobId = String(args.jobId);
    let job = getJobInfo(jobId);
    if (!job) {
      return {
        content: [{ type: "text", text: `Unknown job: ${jobId}` }],
        isError: true,
      };
    }

    if (job.state === "running" && args.wait !== undefined) {
      const waitSeconds = Math.max(0, Math.min(MAX_WAIT_SECONDS, Number(args.wait) || 0));
      job = (await waitForJob(jobId, waitSeconds * 1000)) ?? job;
    }

    const tailLines = Math.max(1, Number(args.tail_lines) || DEFAULT_TAIL_LINES);
    let tail = await tailFileBytes(job.logPath, TAIL_READ_BYTES);
    const lines = tail.split("\n");
    if (lines.length > tailLines) tail = lines.slice(-tailLines).join("\n");
    if (shouldRedact) tail = redactSecrets(tail);

    const text = applyBudget(
      [
        `${job.jobId}  ${formatState(job)}`,
        `command: ${truncateCommand(job.command)}`,
        `log: ${job.logPath}`,
        ``,
        tail.trimEnd() || "(log empty)",
      ].join("\n"),
      "bash",
    );

    return {
      content: [{ type: "text", text }],
      ...(job.state === "exited" && job.exitCode !== 0 ? { isError: true } : {}),
    };
  },
};
