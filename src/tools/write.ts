import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { writeFile } from "fs/promises";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";

export const writeTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__write",
  description:
    "Write file content and/or invalidate the dedup cache. With `content`: writes to the single file in `file_paths`, then invalidates cache. With `truncate: true`: empties the single file (0 bytes), then invalidates cache. Without `content` or `truncate`: invalidates cache for all listed paths only. Passing `content: \"\"` is treated as cache-only invalidation (same as omitting it).",
  inputSchema: {
    type: "object",
    properties: {
      file_paths: {
        type: "array",
        items: { type: "string" },
        description: "Absolute file paths. When `content` or `truncate` is given, must be exactly one path.",
      },
      content: {
        type: "string",
        description:
          "Optional. If provided and non-empty, written to the single file in `file_paths` (create/overwrite). Omit this field to only invalidate cache. Passing an empty string is treated as cache-only invalidation.",
      },
      truncate: {
        type: "boolean",
        description: "Write an empty file (truncate to 0 bytes). Requires exactly one path.",
        default: false,
      },
    },
    required: ["file_paths"],
  },
  handler: async (args) => {
    const paths = Array.isArray(args.file_paths)
      ? args.file_paths.map(String)
      : [String(args.file_paths)];

    const truncate = args.truncate === true;
    const hasContent = typeof args.content === "string" && args.content.length > 0;

    if (truncate) {
      if (paths.length !== 1) {
        return {
          content: [
            {
              type: "text",
              text: "Error: `truncate` requires exactly one path in `file_paths`",
            },
          ],
          isError: true,
        };
      }
      try {
        await writeFile(paths[0], "", "utf-8");
      } catch {
        return {
          content: [{ type: "text", text: `Error: cannot truncate ${paths[0]}` }],
          isError: true,
        };
      }
      invalidateFileCache(paths[0]);
      recordEdit(paths[0]);
      return {
        content: [
          { type: "text", text: `Truncated ${paths[0]} (0 bytes)` },
        ],
      };
    }

    if (hasContent) {
      const content = String(args.content);
      if (paths.length !== 1) {
        return {
          content: [
            {
              type: "text",
              text: "Error: `content` requires exactly one path in `file_paths`. To invalidate cache for multiple files, omit `content`.",
            },
          ],
          isError: true,
        };
      }
      try {
        await writeFile(paths[0], content, "utf-8");
      } catch {
        return {
          content: [{ type: "text", text: `Error: cannot write ${paths[0]}` }],
          isError: true,
        };
      }
      invalidateFileCache(paths[0]);
      recordEdit(paths[0]);
      return {
        content: [
          { type: "text", text: `Wrote ${content.length} chars to ${paths[0]}` },
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
