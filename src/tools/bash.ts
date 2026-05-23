import type { ToolResult } from "../types.js";
import { execSync } from "child_process";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  applyBudget,
  extractStructuredData,
  rewriteCommandWithRtk,
} from "../compression/utils.js";

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
    },
    required: ["command", "description"],
  },
  handler: async (args) => {
    const command = String(args.command);
    const timeout = Number(args.timeout) || 120000;

    try {
      const rewrittenCommand = rewriteCommandWithRtk(command);
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
      const errorMessage =
        error instanceof Error ? error.message : String(error);
      return {
        content: [
          {
            type: "text",
            text: `Error: ${errorMessage}`,
          },
        ],
        isError: true,
      };
    }
  },
};
