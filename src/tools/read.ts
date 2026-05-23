import type { ToolResult } from "../types.js";
import { readFileSync } from "fs";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  isFileUnchanged,
  updateFileCache,
  applyBudget,
} from "../compression/utils.js";

export const readTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__read",
  description:
    "Read a file with dedup + Flint Chipper compression. Returns a stub if the file hasn't changed since the last read in the same session.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to read",
      },
      offset: {
        type: "number",
        description: "Line number to start reading from (1-indexed)",
        default: 1,
      },
      limit: {
        type: "number",
        description: "Maximum number of lines to read",
        default: 200,
      },
    },
    required: ["file_path"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path);
    const offset = Number(args.offset) || 1;
    const limit = Number(args.limit) || 200;

    if (isFileUnchanged(filePath)) {
      return {
        content: [
          {
            type: "text",
            text: "<file unchanged since last read>",
          },
        ],
      };
    }

    try {
      const content = readFileSync(filePath, "utf-8");
      const lines = content.split("\n");
      const start = Math.max(0, offset - 1);
      const end = Math.min(lines.length, start + limit);
      const selected = lines.slice(start, end).join("\n");

      updateFileCache(filePath);

      const compressed = applyBudget(selected, "read");

      return {
        content: [
          {
            type: "text",
            text: compressed,
          },
        ],
      };
    } catch (error) {
      return {
        content: [
          {
            type: "text",
            text: `Error reading file: ${error instanceof Error ? error.message : String(error)}`,
          },
        ],
        isError: true,
      };
    }
  },
};
