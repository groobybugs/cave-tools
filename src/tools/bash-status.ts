import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  fileByteSize,
  getJobInfo,
  listSessionJobs,
  readFileChunk,
  waitForJob,
} from "../runtime/jobs.js";
import { redactSecrets } from "../compression/redact.js";
import { archiveHandle } from "../compression/archive.js";
import { applyBudget, getBudget } from "../compression/utils.js";
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

function nonnegativeInteger(value: unknown, fallback: number): number | null {
  if (value === undefined) return fallback;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return null;
  return Math.floor(parsed);
}

function limitIncrementalLines(
  text: string,
  maxLines: number,
): { text: string; consumedBytes: number; limited: boolean } {
  let end = 0;
  for (let line = 0; line < maxLines; line++) {
    const newline = text.indexOf("\n", end);
    if (newline === -1) {
      return { text, consumedBytes: Buffer.byteLength(text, "utf-8"), limited: false };
    }
    end = newline + 1;
  }
  if (end >= text.length) {
    return { text, consumedBytes: Buffer.byteLength(text, "utf-8"), limited: false };
  }
  const limitedText = text.slice(0, end);
  return {
    text: limitedText,
    consumedBytes: Buffer.byteLength(limitedText, "utf-8"),
    limited: true,
  };
}

export const bashStatusTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash_status",
  annotations: { readOnlyHint: true },
  description:
    "Check a cave__bash_start job: state, exit code, duration, log output.",
  inputSchema: {
    type: "object",
    properties: {
      jobId: {
        type: "string",
        description: "Omit to list session jobs",
      },
      tail_lines: {
        type: "number",
        minimum: 0,
        description: "Log lines; 0 = status only",
        default: DEFAULT_TAIL_LINES,
      },
      cursor: {
        type: "number",
        minimum: 0,
        description: "Prior next_cursor for incremental output; omit for tail",
      },
      wait: {
        type: "number",
        description: `Seconds to block until exit (max ${MAX_WAIT_SECONDS})`,
      },
      redact_secrets: {
        type: "boolean",
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
      const listing = lines.join("\n");
      return {
        content: [
          {
            type: "text",
            text: applyBudget(shouldRedact ? redactSecrets(listing) : listing, "bash"),
          },
        ],
      };
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

    const tailLines = nonnegativeInteger(args.tail_lines, DEFAULT_TAIL_LINES);
    const cursor = nonnegativeInteger(args.cursor, 0);
    if (tailLines === null || cursor === null) {
      return {
        content: [{ type: "text", text: "tail_lines and cursor must be non-negative numbers" }],
        isError: true,
      };
    }

    const header = [
      `${job.jobId}  ${formatState(job)}`,
      `command: ${truncateCommand(job.command)}`,
      `log: ${job.logPath}`,
    ];

    let rawText: string;
    if (tailLines === 0) {
      const logSize = fileByteSize(job.logPath);
      rawText = [
        ...header,
        `log_size: ${logSize} bytes`,
        `next_cursor: ${logSize}`,
        ``,
        "(log output omitted; tail_lines=0 read no log bytes; use cursor=0 to read existing output)",
      ].join("\n");
    } else if (args.cursor !== undefined) {
      const chunk = await readFileChunk(job.logPath, cursor, TAIL_READ_BYTES);
      const budgetLines = Math.max(1, getBudget("bash").maxLines - 8);
      const limited = limitIncrementalLines(chunk.text, Math.min(tailLines, budgetLines));
      const nextCursor = chunk.startCursor + limited.consumedBytes;
      const hasMore = nextCursor < chunk.fileSize;
      const notes: string[] = [];
      if (chunk.cursorReset) {
        notes.push(`cursor_reset: ${chunk.requestedCursor} exceeded log size; resumed at 0`);
      }
      if (hasMore) {
        const reason = limited.limited ? "line limit" : `${TAIL_READ_BYTES}-byte read limit`;
        notes.push(`output_truncated: more log bytes remain (${reason}); continue with cursor=${nextCursor}`);
      }
      rawText = [
        ...header,
        `cursor: ${chunk.startCursor}-${nextCursor} of ${chunk.fileSize} bytes`,
        `next_cursor: ${nextCursor}`,
        ...notes,
        ``,
        limited.text.trimEnd() || "(no new log output)",
      ].join("\n");
    } else {
      const logSize = fileByteSize(job.logPath);
      const chunk = await readFileChunk(
        job.logPath,
        Math.max(0, logSize - TAIL_READ_BYTES),
        TAIL_READ_BYTES,
      );
      let tail = chunk.text;
      const lines = tail.split("\n");
      let lineTruncated = false;
      if (lines.length > tailLines) {
        tail = lines.slice(-tailLines).join("\n");
        lineTruncated = true;
      }
      const tailTruncated = chunk.startCursor > 0 || lineTruncated;
      rawText = [
        ...header,
        `next_cursor: ${chunk.fileSize}`,
        ...(tailTruncated
          ? ["tail_truncated: earlier log output omitted; use cursor=0 for incremental output"]
          : []),
        ``,
        tail.trimEnd() || "(log empty)",
      ].join("\n");
    }

    const protectedText = shouldRedact ? redactSecrets(rawText) : rawText;
    const text = applyBudget(protectedText, "bash");
    const handle = await archiveHandle(protectedText, text, `bash_status:${job.jobId}`);

    return {
      content: [{ type: "text", text: `${text}${handle}` }],
      ...(job.state === "exited" && job.exitCode !== 0 ? { isError: true } : {}),
    };
  },
};
