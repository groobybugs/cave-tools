import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";
import { writeFile } from "fs/promises";

export const writeTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__write",
  description:
    "Write a single file (create or overwrite), then invalidate the dedup cache. Provide `content` to write text, or `truncate: true` to write an empty file (0 bytes). `content: \"\"` also writes an empty file. To only invalidate the cache after editing files outside Cave Tools (no disk write), use cave__invalidate instead.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path of the file to write (create or overwrite)",
      },
      content: {
        type: "string",
        description: "Text to write. Omit only when using `truncate`. An empty string writes an empty file.",
      },
      truncate: {
        type: "boolean",
        description: "Write an empty file (0 bytes) instead of `content`.",
        default: false,
      },
    },
    required: ["file_path"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path ?? "");
    if (filePath.length === 0) {
      return err("`file_path` is required");
    }

    const truncate = args.truncate === true;
    const hasContent = typeof args.content === "string";

    if (!truncate && !hasContent) {
      return err("provide `content` to write, or set `truncate: true`. For cache-only invalidation use cave__invalidate.");
    }

    const content = truncate ? "" : String(args.content);
    try {
      await writeFile(filePath, content, "utf-8");
    } catch {
      return err(`cannot write ${filePath}`);
    }
    invalidateFileCache(filePath);
    recordEdit(filePath);

    return ok(
      truncate
        ? `Truncated ${filePath} (0 bytes)`
        : `Wrote ${content.length} chars to ${filePath}`,
    );
  },
};

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
