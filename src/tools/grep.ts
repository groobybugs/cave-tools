import { spawn } from "child_process";
import { createInterface } from "node:readline";
import { readFile, stat } from "fs/promises";
import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { applyBudget } from "../compression/utils.js";

const DEFAULT_LIMIT = 100;
const GREP_MAX_LINE_LENGTH = 500;

function truncateLine(text: string): string {
  if (text.length <= GREP_MAX_LINE_LENGTH) return text;
  return text.slice(0, GREP_MAX_LINE_LENGTH) + "... (truncated)";
}

export const grepTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__grep",
  description:
    "Search file contents using ripgrep. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is compressed with Flint Chipper budgets.",
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

      const rgArgs: string[] = [
        "--json",
        "--line-number",
        "--color=never",
        "--hidden",
      ];
      if (ignoreCase) rgArgs.push("--ignore-case");
      if (literal) rgArgs.push("--fixed-strings");
      if (glob) rgArgs.push("--glob", glob);
      rgArgs.push(pattern, searchPath);

      const output = await runRg(
        rgArgs,
        searchPath,
        isDirectory,
        contextValue,
        effectiveLimit,
      );

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

async function getFileLinesAsync(
  filePath: string,
  cache: Map<string, string[]>,
): Promise<string[]> {
  let lines = cache.get(filePath);
  if (!lines) {
    try {
      const content = await readFile(filePath, "utf-8");
      lines = content
        .replace(/\r\n/g, "\n")
        .replace(/\r/g, "\n")
        .split("\n");
    } catch {
      lines = [];
    }
    cache.set(filePath, lines);
  }
  return lines;
}

function runRg(
  rgArgs: string[],
  searchPath: string,
  isDirectory: boolean,
  contextValue: number,
  effectiveLimit: number,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn("rg", rgArgs, { stdio: ["ignore", "pipe", "pipe"] });
    const rl = createInterface({ input: child.stdout });
    let stderr = "";
    let matchCount = 0;
    let matchLimitReached = false;

    const matches: Array<{ filePath: string; lineNumber: number }> = [];

    child.stderr?.on("data", (chunk: Buffer) => {
      stderr += chunk.toString();
    });

    rl.on("line", (line: string) => {
      if (!line.trim() || matchCount >= effectiveLimit) return;
      let event: any;
      try {
        event = JSON.parse(line);
      } catch {
        return;
      }
      if (event.type === "match") {
        matchCount++;
        const filePath = event.data?.path?.text;
        const lineNumber = event.data?.line_number;
        if (filePath && typeof lineNumber === "number")
          matches.push({ filePath, lineNumber });
        if (matchCount >= effectiveLimit) {
          matchLimitReached = true;
          if (!child.killed) child.kill();
        }
      }
    });

    child.on("error", (error) => {
      reject(new Error(`Failed to run ripgrep: ${error.message}`));
    });

    child.on("close", async (code) => {
      if (code !== 0 && code !== 1 && matchCount === 0) {
        reject(new Error(stderr.trim() || `ripgrep exited with code ${code}`));
        return;
      }

      if (matchCount === 0) {
        resolve("");
        return;
      }

      const formatPath = (filePath: string): string => {
        if (isDirectory) {
          const relative = path.relative(searchPath, filePath);
          if (relative && !relative.startsWith(".."))
            return relative.replace(/\\/g, "/");
        }
        return path.basename(filePath);
      };

      const fileCache = new Map<string, string[]>();
      let linesTruncated = false;

      const outputLines: string[] = [];
      for (const match of matches) {
        const relativePath = formatPath(match.filePath);
        const lines = await getFileLinesAsync(match.filePath, fileCache);
        if (!lines.length) {
          outputLines.push(`${relativePath}:${match.lineNumber}: (unable to read file)`);
          continue;
        }
        const start =
          contextValue > 0 ? Math.max(1, match.lineNumber - contextValue) : match.lineNumber;
        const end =
          contextValue > 0
            ? Math.min(lines.length, match.lineNumber + contextValue)
            : match.lineNumber;
        for (let current = start; current <= end; current++) {
          const lineText = lines[current - 1] ?? "";
          const sanitized = lineText.replace(/\r/g, "");
          const isMatchLine = current === match.lineNumber;
          const truncated = truncateLine(sanitized);
          if (truncated.length < sanitized.length) linesTruncated = true;
          if (isMatchLine) outputLines.push(`${relativePath}:${current}: ${truncated}`);
          else outputLines.push(`${relativePath}-${current}- ${truncated}`);
        }
      }

      let output = outputLines.join("\n");

      const notices: string[] = [];
      if (matchLimitReached) {
        notices.push(
          `${effectiveLimit} matches limit. Use limit=${effectiveLimit * 2} for more, or refine pattern`,
        );
      }
      if (linesTruncated) {
        notices.push(
          `Some lines truncated to ${GREP_MAX_LINE_LENGTH} chars. Use cave__read to see full lines`,
        );
      }
      if (notices.length > 0) output += `\n\n[${notices.join(". ")}]`;

      resolve(output);
    });
  });
}
