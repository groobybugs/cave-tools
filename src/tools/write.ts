import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";
import { writeTextPreservingBom } from "../runtime/file-mutation.js";

export const writeTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__write",
  description:
    "Create or overwrite one file and refresh the read cache. Empty content or truncate=true writes an empty file. For cache-only refresh use cave__invalidate.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path",
      },
      content: {
        type: "string",
        description: "Omit only with truncate",
      },
      truncate: {
        type: "boolean",
        description: "Write a 0-byte file",
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
      const result = await writeTextPreservingBom(filePath, content);
      invalidateFileCache(result.canonical);
      invalidateFileCache(filePath);
      recordEdit(result.canonical);
      return ok(
        truncate
          ? `Truncated ${result.canonical} (0 bytes)`
          : `Wrote ${content.length} chars to ${result.canonical} (${result.existed ? "overwrote existing file" : "created new file"})`,
      );
    } catch (error) {
      return err(`cannot write ${filePath}`);
    }
  },
};

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
