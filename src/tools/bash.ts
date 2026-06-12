import type { ToolResult } from "../types.js";
import { execSync } from "child_process";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  applyBudget,
  extractStructuredData,
  rewriteCommandWithRtk,
} from "../compression/utils.js";

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
    signal?: NodeJS.Signals;
    stdout?: unknown;
    stderr?: unknown;
  };
  const message = error instanceof Error ? error.message : String(error);
  const stdout = stringifyOutput(err.stdout).trim();
  const stderr = stringifyOutput(err.stderr).trim();
  const details = [
    "Command failed",
    `Original command: ${command}`,
    `Executed command: ${rewrittenCommand}`,
    err.status !== undefined ? `Exit code: ${err.status}` : undefined,
    err.signal !== undefined ? `Signal: ${err.signal}` : undefined,
    stderr ? `stderr:\n${stderr}` : undefined,
    stdout ? `stdout:\n${stdout}` : undefined,
    !stderr && !stdout ? `Message: ${message}` : undefined,
  ].filter(Boolean);

  return details.join("\n");
}

export const bashTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__bash",
  description:
    "Run a shell command with RTK rewriting + Stone Tablet + Flint Chipper compression. Tries RTK command rewriting internally where applicable.",
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
    },
    required: ["command", "description"],
  },
  handler: async (args) => {
    const command = String(args.command);
    const timeout = Number(args.timeout) || 120000;
    const allowFailure = args.allowFailure === true;
    let rewrittenCommand = command;

    try {
      rewrittenCommand = rewriteCommandWithRtk(command);
      const output = execSync(rewrittenCommand, {
        timeout,
        encoding: "utf-8",
        stdio: ["pipe", "pipe", "pipe"],
      });

      // Try structured extraction first
      let processed = extractStructuredData(output, rewrittenCommand);

      // Apply budget compression
      processed = applyBudget(processed, "bash");

      const rtkStatus =
        rewrittenCommand !== command
          ? `[RTK: ${command} -> ${rewrittenCommand}]`
          : "[RTK: no rewrite]";

      return {
        content: [
          {
            type: "text",
            text: `${rtkStatus}\n${processed}`,
          },
        ],
      };
    } catch (error) {
      const errorMessage = applyBudget(
        formatCommandError(error, command, rewrittenCommand),
        "bash",
      );
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
