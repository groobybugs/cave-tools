import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget } from "../compression/utils.js";
import type { ToolResult } from "../types.js";
import { rgFiles } from "../runtime/ripgrep.js";
import { resolveExistingDirectory } from "../runtime/path.js";
import { getCwd } from "../runtime/context.js";

const DEFAULT_LIMIT = 1000;

export const findTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__find",
  annotations: { readOnlyHint: true },
  description:
    "Use instead of Glob/find (fd or Node fallback). Returns paths relative to path, trimmed to a budget.",
  inputSchema: {
    type: "object",
    properties: {
      pattern: {
        type: "string",
        description: "e.g. '**/*.ts'",
      },
      path: {
        type: "string",
      },
      limit: {
        type: "number",
        description: "Max results (default 1000)",
      },
      hidden: {
        type: "boolean",
        description: "Include dotfiles; false matches opencode glob",
        default: true,
      },
    },
    required: ["pattern"],
  },
  handler: async (args) => {
    const pattern = String(args.pattern);
    const limit = Math.max(1, Number(args.limit) || DEFAULT_LIMIT);
    const hidden = args.hidden !== false;

    let searchPath: string;
    try {
      searchPath = await resolveExistingDirectory(args.path ? String(args.path) : ".");
    } catch (error) {
      return {
        content: [{ type: "text", text: `Path not found or not a directory: ${path.resolve(getCwd(), args.path ? String(args.path) : ".")}` }],
        isError: true,
      };
    }

    let lines: string[];
    try {
      lines = await rgFiles(searchPath, pattern, limit, hidden);
    } catch (error) {
      return {
        content: [{ type: "text", text: `Error: ${error instanceof Error ? error.message : String(error)}` }],
        isError: true,
      };
    }

    if (lines.length === 0)
      return {
        content: [{ type: "text", text: "No files found matching pattern" }],
      };

    let output = lines.join("\n");
    if (lines.length >= limit)
      output += `\n\n[${limit} results limit reached. Use limit=${limit * 2} for more]`;

    return { content: [{ type: "text", text: applyBudget(output, "find") }] };
  },
};
