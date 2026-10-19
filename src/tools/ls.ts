import { readdir, stat, realpath } from "fs/promises";
import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget } from "../compression/utils.js";
import type { ToolResult } from "../types.js";
import { containsPath, resolveExistingDirectory } from "../runtime/path.js";
import { getCwd } from "../runtime/context.js";

const DEFAULT_LIMIT = 500;

export const lsTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__ls",
  annotations: { readOnlyHint: true },
  description:
    "Use instead of shell ls. Sorted entries, '/' suffix on dirs, dotfiles included, trimmed to a budget.",
  inputSchema: {
    type: "object",
    properties: {
      path: {
        type: "string",
      },
      limit: {
        type: "number",
        description: "Max entries (default 500)",
      },
      offset: {
        type: "number",
        description: "1-based entry offset",
        default: 1,
      },
    },
  },
  handler: async (args) => {
    const requested = path.resolve(getCwd(), args.path ? String(args.path) : ".");
    const limit = Math.max(1, Number(args.limit) || DEFAULT_LIMIT);
    const offset = Math.max(1, Number(args.offset) || 1);

    let dirPath: string;
    try {
      dirPath = await resolveExistingDirectory(requested);
    } catch {
      return {
        content: [{ type: "text", text: `Path not found or not a directory: ${requested}` }],
        isError: true,
      };
    }

    try {
      const entries = await readdir(dirPath);
      const results: Array<{ name: string; type: "directory" | "file" }> = [];
      for (const entry of entries) {
        const fullPath = path.join(dirPath, entry);
        try {
          const target = await realpath(fullPath);
          if (!containsPath(dirPath, target)) continue;
          const entryStat = await stat(fullPath);
          if (entryStat.isDirectory()) results.push({ name: `${entry}/`, type: "directory" });
          else if (entryStat.isFile()) results.push({ name: entry, type: "file" });
        } catch {
          // Skip entries that disappeared or cannot be statted.
        }
      }
      results.sort((a, b) =>
        a.type === b.type ? a.name.localeCompare(b.name) : a.type === "directory" ? -1 : 1,
      );

      const selected = results.slice(offset - 1, offset - 1 + limit).map((entry) => entry.name);
      const limitReached = offset - 1 + selected.length < results.length;

      if (selected.length === 0)
        return { content: [{ type: "text", text: "(empty directory)" }] };

      let output = selected.join("\n");
      if (limitReached)
        output += `\n\n[Directory listing truncated. Use offset=${offset + selected.length} to continue]`;

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
