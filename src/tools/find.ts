import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget } from "../compression/utils.js";
import type { ToolResult } from "../types.js";
import { rgFiles } from "../runtime/ripgrep.js";
import { resolveExistingDirectory } from "../runtime/path.js";

const DEFAULT_LIMIT = 1000;

export const findTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__find",
  description:
    "Optimized drop-in replacement for the built-in Glob/find (fd when available, Node fallback). Returns matching file paths relative to the search directory, with output trimmed to a line budget.",
  inputSchema: {
    type: "object",
    properties: {
      pattern: {
        type: "string",
        description:
          "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'",
      },
      path: {
        type: "string",
        description: "Directory to search in (default: current directory)",
      },
      limit: {
        type: "number",
        description: "Maximum number of results (default: 1000)",
      },
    },
    required: ["pattern"],
  },
  handler: async (args) => {
    const pattern = String(args.pattern);
    const limit = Math.max(1, Number(args.limit) || DEFAULT_LIMIT);

    let searchPath: string;
    try {
      searchPath = await resolveExistingDirectory(args.path ? String(args.path) : ".");
    } catch (error) {
      return {
        content: [{ type: "text", text: `Path not found or not a directory: ${path.resolve(args.path ? String(args.path) : ".")}` }],
        isError: true,
      };
    }

    let lines: string[];
    try {
      lines = await rgFiles(searchPath, pattern, limit);
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
