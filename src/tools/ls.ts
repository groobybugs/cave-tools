import { readdir, stat, access } from "fs/promises";
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
    "Optimized drop-in replacement for listing a directory. Returns entries sorted alphabetically, with '/' suffix for directories, includes dotfiles, and trims output to a line budget.",
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

    try {
      await access(dirPath);
    } catch {
      return {
        content: [{ type: "text", text: `Path not found: ${dirPath}` }],
        isError: true,
      };
    }

    try {
      const dirStat = await stat(dirPath);
      if (!dirStat.isDirectory()) {
        return {
          content: [{ type: "text", text: `Not a directory: ${dirPath}` }],
          isError: true,
        };
      }
    } catch {
      return {
        content: [{ type: "text", text: `Not a directory: ${dirPath}` }],
        isError: true,
      };
    }

    try {
      const entries = (await readdir(dirPath)).sort((a, b) =>
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
          const entryStat = await stat(fullPath);
          results.push(entryStat.isDirectory() ? `${entry}/` : entry);
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
