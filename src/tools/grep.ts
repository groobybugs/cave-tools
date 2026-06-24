import { stat } from "fs/promises";
import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { applyBudget } from "../compression/utils.js";
import { readLineWindow, rgGrep } from "../runtime/ripgrep.js";

const DEFAULT_LIMIT = 100;
export const grepTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__grep",
  description:
    "Optimized drop-in replacement for the built-in Grep (ripgrep). Returns matching lines with file paths and line numbers, respects .gitignore, with output trimmed to a line budget.",
  inputSchema: {
    type: "object",
    properties: {
      pattern: {
        type: "string",
        description: "Search pattern (regex or literal string)",
      },
      path: {
        type: "string",
        description: "Directory or file to search (default: current directory)",
      },
      glob: {
        type: "string",
        description:
          "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'",
      },
      ignoreCase: {
        type: "boolean",
        description: "Case-insensitive search (default: false)",
      },
      literal: {
        type: "boolean",
        description:
          "Treat pattern as literal string instead of regex (default: false)",
      },
      context: {
        type: "number",
        description:
          "Number of lines to show before and after each match (default: 0)",
      },
      limit: {
        type: "number",
        description: "Maximum number of matches to return (default: 100)",
      },
    },
    required: ["pattern"],
  },
  handler: async (args) => {
    const pattern = String(args.pattern);
    const searchDir = args.path ? String(args.path) : ".";
    const glob = args.glob ? String(args.glob) : undefined;
    const ignoreCase = args.ignoreCase === true;
    const literal = args.literal === true;
    const contextValue =
      typeof args.context === "number" && args.context > 0 ? args.context : 0;
    const effectiveLimit = Math.max(1, (args.limit as number) ?? DEFAULT_LIMIT);

    try {
      const searchPath = path.resolve(searchDir);
      let isDirectory: boolean;
      try {
        const s = await stat(searchPath);
        isDirectory = s.isDirectory();
      } catch {
        return {
          content: [
            { type: "text" as const, text: `Path not found: ${searchPath}` },
          ],
          isError: true,
        };
      }

      const cwd = isDirectory ? searchPath : path.dirname(searchPath);
      const file = isDirectory ? undefined : path.basename(searchPath);
      const result = await rgGrep({
        cwd,
        pattern,
        file,
        include: glob,
        limit: effectiveLimit,
        ignoreCase,
        literal,
      });
      const output = await formatMatches(result.matches, cwd, isDirectory ? searchPath : cwd, contextValue, result.truncated, effectiveLimit);

      if (!output) {
        return {
          content: [{ type: "text" as const, text: "No matches found" }],
        };
      }

      const compressed = applyBudget(output, "grep");

      return {
        content: [{ type: "text" as const, text: compressed }],
      };
    } catch (error) {
      return {
        content: [
          {
            type: "text" as const,
            text: `Error: ${error instanceof Error ? error.message : String(error)}`,
          },
        ],
        isError: true,
      };
    }
  },
};

async function formatMatches(
  matches: Array<{ path: string; line: number; text: string }>,
  cwd: string,
  searchPath: string,
  contextValue: number,
  truncated: boolean,
  effectiveLimit: number,
): Promise<string> {
  if (matches.length === 0) return "";
  const outputLines: string[] = [];
  let linesTruncated = false;
  for (const match of matches) {
    const absolute = path.resolve(cwd, match.path);
    const relative = path.relative(searchPath, absolute).replace(/\\/g, "/") || path.basename(absolute);
    if (contextValue === 0) {
      if (match.text.includes("...")) linesTruncated = true;
      outputLines.push(`${relative}:${match.line}: ${match.text}`);
      continue;
    }
    try {
      for (const item of await readLineWindow(absolute, match.line, contextValue)) {
        if (item.text.includes("... (truncated)")) linesTruncated = true;
        outputLines.push(item.match ? `${relative}:${item.line}: ${item.text}` : `${relative}-${item.line}- ${item.text}`);
      }
    } catch {
      outputLines.push(`${relative}:${match.line}: ${match.text}`);
    }
  }

  let output = outputLines.join("\n");
  const notices: string[] = [];
  if (truncated) notices.push(`${effectiveLimit} matches limit. Use limit=${effectiveLimit * 2} for more, or refine pattern`);
  if (linesTruncated) notices.push("Some lines truncated. Use cave__read to see full lines");
  if (notices.length > 0) output += `\n\n[${notices.join(". ")}]`;
  return output;
}
