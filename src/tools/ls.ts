import { existsSync, readdirSync, statSync } from "fs";
import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget } from "../compression/utils.js";
import type { ToolResult } from "../types.js";

const DEFAULT_LIMIT = 500;

export const lsTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__ls",
  description:
    "List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is compressed with Flint Chipper budgets.",
  inputSchema: {
    type: "object",
    properties: {
      path: {
        type: "string",
        description: "Directory to list (default: current directory)",
      },
      limit: {
        type: "number",
        description: "Maximum number of entries to return (default: 500)",
      },
    },
  },
  handler: async (args) => {
    const dirPath = path.resolve(args.path ? String(args.path) : ".");
    const limit = Math.max(1, Number(args.limit) || DEFAULT_LIMIT);

    if (!existsSync(dirPath)) {
      return {
        content: [{ type: "text", text: `Path not found: ${dirPath}` }],
        isError: true,
      };
    }
    if (!statSync(dirPath).isDirectory()) {
      return {
        content: [{ type: "text", text: `Not a directory: ${dirPath}` }],
        isError: true,
      };
    }

    try {
      const entries = readdirSync(dirPath).sort((a, b) =>
        a.toLowerCase().localeCompare(b.toLowerCase()),
      );
      const results: string[] = [];
      let limitReached = false;

      for (const entry of entries) {
        if (results.length >= limit) {
          limitReached = true;
          break;
        }
        const fullPath = path.join(dirPath, entry);
        try {
          results.push(statSync(fullPath).isDirectory() ? `${entry}/` : entry);
        } catch {
          // Skip entries that disappeared or cannot be statted.
        }
      }

      if (results.length === 0)
        return { content: [{ type: "text", text: "(empty directory)" }] };

      let output = results.join("\n");
      if (limitReached)
        output += `\n\n[${limit} entries limit reached. Use limit=${limit * 2} for more]`;

      return { content: [{ type: "text", text: applyBudget(output, "ls") }] };
    } catch (error) {
      return {
        content: [
          {
            type: "text",
            text: `Cannot read directory: ${error instanceof Error ? error.message : String(error)}`,
          },
        ],
        isError: true,
      };
    }
  },
};
