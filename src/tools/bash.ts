import type { ToolResult } from "../types.js";
import { exec } from "child_process";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  applyBudget,
  extractStructuredData,
  rewriteCommandWithRtk,
} from "../compression/utils.js";
import { redactSecrets } from "../compression/redact.js";
import { classifyCommand } from "../compression/classify.js";
import { archiveIfLarge } from "../compression/archive.js";

function stringifyOutput(value: unknown): string {
  if (Buffer.isBuffer(value)) return value.toString("utf-8");
  return typeof value === "string" ? value : "";
}

function formatCommandError(
  error: unknown,
  command: string,
  rewrittenCommand: string,
): string {
  const err = error as NodeJS.ErrnoException & {
    status?: number;
    code?: number | string;
    signal?: NodeJS.Signals;
    stdout?: unknown;
    stderr?: unknown;
  };
  const message = error instanceof Error ? error.message : String(error);
  const stdout = stringifyOutput(err.stdout).trim();
  const stderr = stringifyOutput(err.stderr).trim();
  const exitCode = typeof err.status === "number"
    ? err.status
    : typeof err.code === "number"
      ? err.code
      : undefined;
  const details = [
    "Command failed",
    `Original command: ${command}`,
    `Executed command: ${rewrittenCommand}`,
    exitCode !== undefined ? `Exit code: ${exitCode}` : undefined,
    err.signal !== undefined ? `Signal: ${err.signal}` : undefined,
    stderr ? `stderr:\n${stderr}` : undefined,
    stdout ? `stdout:\n${stdout}` : undefined,
    !stderr && !stdout ? `Message: ${message}` : undefined,
  ].filter(Boolean);

  return details.join("\n");
}

function execAsync(
  command: string,
  options: { timeout: number; maxBuffer?: number },
): Promise<string> {
  return new Promise((resolve, reject) => {
    exec(command, {
      timeout: options.timeout,
      encoding: "utf-8",
      maxBuffer: options.maxBuffer ?? 10 * 1024 * 1024,
    }, (error: Error | null, stdout: string, stderr: string) => {
      if (error) {
        (error as any).stdout = stdout;
        (error as any).stderr = stderr;
        reject(error);
      } else {
        resolve(stdout);
      }
    });
  });
}

export const bashTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash",
  description:
    "Optimized drop-in replacement for the built-in shell tool. Runs the same command and returns its output, applying RTK command rewriting where available and trimming noisy output to the essentials. Sensitive tokens and keys are redacted from output by default.",
  inputSchema: {
    type: "object",
    properties: {
      command: {
        type: "string",
        description: "The shell command to execute",
      },
      description: {
        type: "string",
        description:
          "Clear, concise description of what the command does (5-10 words)",
      },
      timeout: {
        type: "number",
        description: "Timeout in milliseconds",
        default: 120000,
      },
      allowFailure: {
        type: "boolean",
        description:
          "When true, non-zero exits are returned as normal text instead of MCP errors. Default false.",
        default: false,
      },
      redact_secrets: {
        type: "boolean",
        description:
          "When false, skip secret redaction. Default true.",
        default: true,
      },
    },
    required: ["command", "description"],
  },
  handler: async (args) => {
    const command = String(args.command);
    const timeout = Number(args.timeout) || 120000;
    const allowFailure = args.allowFailure === true;
    const shouldRedact = args.redact_secrets !== false;
    let rewrittenCommand = command;
    const policy = classifyCommand(command);

    try {
      rewrittenCommand = await rewriteCommandWithRtk(command);
      const output = await execAsync(rewrittenCommand, {
        timeout,
      });

      let processed = shouldRedact ? redactSecrets(output) : output;
      const rtkStatus =
        rewrittenCommand !== command
          ? `[RTK: ${command} -> ${rewrittenCommand}]`
          : "[RTK: no rewrite]";

      if (policy === "passthrough") {
        return {
          content: [
            {
              type: "text",
              text: `${rtkStatus}\n${processed}`,
            },
          ],
        };
      }

      const archive = await archiveIfLarge(processed, command);

      if (policy === "verbatim") {
        const lines = processed.split("\n");
        const VERBATIM_MAX = 500;
        if (lines.length > VERBATIM_MAX) {
          processed = [
            ...lines.slice(0, 250),
            `\n... (${lines.length - 500} lines truncated) ...\n`,
            ...lines.slice(-250),
          ].join("\n");
        }
        const archiveNote = archive
          ? `\n\n[Archived: ${archive.id} — use cave__compress expand ${archive.id}]`
          : "";
        return {
          content: [
            {
              type: "text",
              text: `${rtkStatus}\n${processed}${archiveNote}`,
            },
          ],
        };
      }

      // Try structured extraction first
      processed = extractStructuredData(processed, rewrittenCommand);

      // Apply budget compression
      processed = applyBudget(processed, "bash");

      const archiveNote = archive
        ? `\n\n[Archived: ${archive.id} — use cave__compress expand ${archive.id}]`
        : "";

      return {
        content: [
          {
            type: "text",
            text: `${rtkStatus}\n${processed}${archiveNote}`,
          },
        ],
      };
    } catch (error) {
      let errorMessage = formatCommandError(error, command, rewrittenCommand);
      if (shouldRedact) {
        errorMessage = redactSecrets(errorMessage);
      }
      errorMessage = applyBudget(errorMessage, "bash");
      return {
        content: [
          {
            type: "text",
            text: errorMessage,
          },
        ],
        ...(allowFailure ? {} : { isError: true }),
      };
    }
  },
};
