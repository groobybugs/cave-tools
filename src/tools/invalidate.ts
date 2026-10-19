import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache } from "../compression/utils.js";

export const invalidateTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__invalidate",
  annotations: { readOnlyHint: true },
  description:
    "Drop cave__read cache entries (no disk write). Call after editing files outside Cave Tools.",
  inputSchema: {
    type: "object",
    properties: {
      file_paths: {
        type: "array",
        items: { type: "string" },
        description: "Absolute paths",
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
