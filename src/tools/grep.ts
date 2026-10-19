import { stat } from "fs/promises";
import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { applyBudget } from "../compression/utils.js";
import { collapseRepetition } from "../compression/repetition.js";
import { archiveHandle } from "../compression/archive.js";
import { readLineWindow, rgGrep } from "../runtime/ripgrep.js";
import { getCwd } from "../runtime/context.js";

const DEFAULT_LIMIT = 100;
const PER_FILE_LINE_CAP = 10;
export const grepTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__grep",
  annotations: { readOnlyHint: true },
  description:
    "Use instead of Grep (ripgrep). Respects .gitignore, caps hits per file, trims output to a budget.",
  inputSchema: {
    type: "object",
    properties: {
      pattern: {
        type: "string",
        description: "Regex (or literal with literal=true)",
      },
      path: {
        type: "string",
      },
      glob: {
        type: "string",
        description: "File filter, e.g. '*.ts'",
      },
      ignoreCase: {
        type: "boolean",
      },
      literal: {
        type: "boolean",
      },
      context: {
        type: "number",
        description: "Lines around each match",
      },
      limit: {
        type: "number",
        description: "Max matches (default 100)",
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
      const searchPath = path.resolve(getCwd(), searchDir);
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
      const { output, full } = await formatMatches(result.matches, cwd, isDirectory ? searchPath : cwd, contextValue, result.truncated, effectiveLimit);

      if (!output) {
        return {
          content: [{ type: "text" as const, text: "No matches found" }],
        };
      }

      const compressed = applyBudget(output, "grep");
      const handle = await archiveHandle(full, compressed, `grep:${pattern}`);

      return {
        content: [{ type: "text" as const, text: `${compressed}${handle}` }],
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
): Promise<{ output: string; full: string }> {
  if (matches.length === 0) return { output: "", full: "" };
  const outputLines: string[] = [];
  // Every line, without the per-file cap or repetition collapse, for the archive.
  const allLines: string[] = [];
  let linesTruncated = false;
  let currentFile = "";
  let fileLineCount = 0;
  let fileOverflow = 0;

  const flushOverflow = () => {
    if (fileOverflow > 0 && currentFile) {
      outputLines.push(`+${fileOverflow} more in ${currentFile}`);
    }
  };

  const pushLine = (relative: string, line: string): void => {
    allLines.push(line);
    if (relative !== currentFile) {
      flushOverflow();
      currentFile = relative;
      fileLineCount = 0;
      fileOverflow = 0;
    }
    if (fileLineCount >= PER_FILE_LINE_CAP) {
      fileOverflow++;
      return;
    }
    fileLineCount++;
    outputLines.push(line);
  };

  for (const match of matches) {
    const absolute = path.resolve(cwd, match.path);
    const relative = path.relative(searchPath, absolute).replace(/\\/g, "/") || path.basename(absolute);
    if (contextValue === 0) {
      if (match.text.includes("...")) linesTruncated = true;
      pushLine(relative, `${relative}:${match.line}: ${match.text}`);
      continue;
    }
    try {
      for (const item of await readLineWindow(absolute, match.line, contextValue)) {
        if (item.text.includes("... (truncated)")) linesTruncated = true;
        pushLine(
          relative,
          item.match
            ? `${relative}:${item.line}: ${item.text}`
            : `${relative}-${item.line}- ${item.text}`,
        );
      }
    } catch {
      pushLine(relative, `${relative}:${match.line}: ${match.text}`);
    }
  }
  flushOverflow();

  let output = collapseRepetition(outputLines.join("\n"));
  let full = allLines.join("\n");
  const notices: string[] = [];
  if (truncated) notices.push(`${effectiveLimit} matches limit. Use limit=${effectiveLimit * 2} for more, or refine pattern`);
  if (linesTruncated) notices.push("Some lines truncated. Use cave__read to see full lines");
  if (notices.length > 0) {
    output += `\n\n[${notices.join(". ")}]`;
    full += `\n\n[${notices.join(". ")}]`;
  }
  return { output, full };
}
