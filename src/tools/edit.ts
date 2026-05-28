import { readFileSync, writeFileSync } from "fs";
import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache } from "../compression/utils.js";

export const editTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__edit",
  description:
    "Exact string replacement in a file (like the built-in Edit). Replaces old_string with new_string; fails if old_string is missing or non-unique (unless replace_all). Invalidates the read dedup cache automatically. Use this instead of the built-in Edit so reads stay routed through cave__read.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to modify",
      },
      old_string: {
        type: "string",
        description: "Text to replace (must match exactly, including indentation)",
      },
      new_string: {
        type: "string",
        description: "Replacement text (must differ from old_string)",
      },
      replace_all: {
        type: "boolean",
        description: "Replace every occurrence instead of requiring uniqueness (default false)",
      },
    },
    required: ["file_path", "old_string", "new_string"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path);
    const oldString = String(args.old_string ?? "");
    const newString = String(args.new_string ?? "");
    const replaceAll = args.replace_all === true;

    if (oldString === newString) {
      return err("old_string and new_string are identical");
    }

    let content: string;
    try {
      content = readFileSync(filePath, "utf-8");
    } catch {
      return err(`Cannot read file: ${filePath}`);
    }

    const occurrences = content.split(oldString).length - 1;
    if (occurrences === 0) {
      return err("old_string not found in file");
    }
    if (occurrences > 1 && !replaceAll) {
      return err(
        `old_string is not unique (${occurrences} matches). Add more context or set replace_all.`,
      );
    }

    const updated = replaceAll
      ? content.split(oldString).join(newString)
      : content.replace(oldString, newString);

    try {
      writeFileSync(filePath, updated, "utf-8");
    } catch {
      return err(`Cannot write file: ${filePath}`);
    }
    invalidateFileCache(filePath);

    const n = replaceAll ? occurrences : 1;
    return ok(`Edited ${filePath} (${n} replacement${n === 1 ? "" : "s"})`);
  },
};

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
