import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache } from "../compression/utils.js";

export const invalidateTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__invalidate",
  description:
    "Invalidate the read dedup cache for one or more paths without touching disk. Call this after editing files outside Cave Tools so the next cave__read returns fresh content instead of an unchanged stub. To write file content, use cave__write.",
  inputSchema: {
    type: "object",
    properties: {
      file_paths: {
        type: "array",
        items: { type: "string" },
        description: "Absolute file paths whose cached reads should be invalidated.",
      },
    },
    required: ["file_paths"],
  },
  handler: async (args) => {
    const paths = Array.isArray(args.file_paths)
      ? args.file_paths.map(String).filter((p) => p.length > 0)
      : [];

    if (paths.length === 0) {
      return {
        content: [{ type: "text", text: "Error: `file_paths` must be a non-empty array" }],
        isError: true,
      };
    }

    for (const filePath of paths) {
      invalidateFileCache(filePath);
    }

    return {
      content: [{ type: "text", text: `Invalidated cache for ${paths.length} file(s)` }],
    };
  },
};
