import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { writeFileSync } from "fs";
import { invalidateFileCache } from "../compression/utils.js";

export const writeTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__write",
  description:
    "Write file content and/or invalidate the dedup cache. With `content`, writes (creates/overwrites) the single file in `file_paths` then invalidates it. Without `content`, only invalidates the cache for the listed paths. Omit `content` for cache invalidation; `content: \"\"` writes an empty file.",
  inputSchema: {
    type: "object",
    properties: {
      file_paths: {
        type: "array",
        items: { type: "string" },
        description: "Absolute file paths. When `content` is given, must be exactly one path.",
      },
      content: {
        type: "string",
        description:
          "Optional. If provided, written to the single file in `file_paths` (create/overwrite). Omit this field to only invalidate cache. Passing an empty string writes an empty file.",
      },
    },
    required: ["file_paths"],
  },
  handler: async (args) => {
    const paths = Array.isArray(args.file_paths)
      ? args.file_paths.map(String)
      : [String(args.file_paths)];

    if (typeof args.content === "string") {
      if (paths.length !== 1) {
        return {
          content: [
            {
              type: "text",
              text: "Error: `content` requires exactly one path in `file_paths`",
            },
          ],
          isError: true,
        };
      }
      try {
        writeFileSync(paths[0], args.content, "utf-8");
      } catch {
        return {
          content: [{ type: "text", text: `Error: cannot write ${paths[0]}` }],
          isError: true,
        };
      }
      invalidateFileCache(paths[0]);
      return {
        content: [
          { type: "text", text: `Wrote ${args.content.length} chars to ${paths[0]}` },
        ],
      };
    }

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
