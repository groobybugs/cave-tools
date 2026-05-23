import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache } from "../compression/utils.js";

export const writeTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__write",
  description:
    "Invalidate the dedup cache after file edits. Call this after editing or writing a file so subsequent cave__read calls return fresh content.",
  inputSchema: {
    type: "object",
    properties: {
      file_paths: {
        type: "array",
        items: { type: "string" },
        description: "Array of absolute file paths to invalidate in the cache",
      },
    },
    required: ["file_paths"],
  },
  handler: async (args) => {
    const paths = Array.isArray(args.file_paths)
      ? args.file_paths.map(String)
      : [String(args.file_paths)];

    for (const filePath of paths) {
      invalidateFileCache(filePath);
    }

    return {
      content: [
        {
          type: "text",
          text: `Invalidated cache for ${paths.length} file(s)`,
        },
      ],
    };
  },
};
