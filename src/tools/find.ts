import { existsSync, readdirSync, statSync } from "fs";
import path from "path";
import { spawnSync } from "child_process";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget } from "../compression/utils.js";
import type { ToolResult } from "../types.js";

const DEFAULT_LIMIT = 1000;

function toPosixPath(value: string): string {
  return value.split(path.sep).join("/");
}

function globToRegExp(pattern: string): RegExp {
  let source = "";
  for (let i = 0; i < pattern.length; i++) {
    const char = pattern[i]!;
    const next = pattern[i + 1];
    if (char === "*" && next === "*") {
      source += ".*";
      i++;
    } else if (char === "*") {
      source += "[^/]*";
    } else if (char === "?") {
      source += "[^/]";
    } else if (".+^${}()|[]\\".includes(char)) {
      source += `\\${char}`;
    } else {
      source += char;
    }
  }
  return new RegExp(`^${source}$`);
}

function findWithNode(
  searchPath: string,
  pattern: string,
  limit: number,
): string[] {
  const matcher = globToRegExp(
    pattern.includes("/") ? pattern : `**/${pattern}`,
  );
  const basenameMatcher = pattern.includes("/") ? null : globToRegExp(pattern);
  const results: string[] = [];
  const stack = [searchPath];

  while (stack.length > 0 && results.length < limit) {
    const dir = stack.pop()!;
    let entries: string[];
    try {
      entries = readdirSync(dir);
    } catch {
      continue;
    }

    for (const entry of entries) {
      if (entry === ".git" || entry === "node_modules") continue;
      const fullPath = path.join(dir, entry);
      let stat;
      try {
        stat = statSync(fullPath);
      } catch {
        continue;
      }

      const relative = toPosixPath(path.relative(searchPath, fullPath));
      if (
        matcher.test(relative) ||
        basenameMatcher?.test(path.basename(relative))
      ) {
        results.push(relative + (stat.isDirectory() ? "/" : ""));
      }
      if (results.length >= limit) break;
      if (stat.isDirectory()) stack.push(fullPath);
    }
  }

  return results.sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

export const findTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__find",
  description:
    "Search for files by glob pattern using fd when available, with a Node fallback. Returns matching file paths relative to the search directory. Output is compressed with Flint Chipper budgets.",
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
    const searchPath = path.resolve(args.path ? String(args.path) : ".");
    const limit = Math.max(1, Number(args.limit) || DEFAULT_LIMIT);

    if (!existsSync(searchPath)) {
      return {
        content: [{ type: "text", text: `Path not found: ${searchPath}` }],
        isError: true,
      };
    }

    const fdArgs = [
      "--glob",
      "--color=never",
      "--hidden",
      "--max-results",
      String(limit),
      pattern,
      searchPath,
    ];
    const result = spawnSync("fd", fdArgs, {
      encoding: "utf-8",
      maxBuffer: 10 * 1024 * 1024,
    });

    const lines = result.error
      ? findWithNode(searchPath, pattern, limit)
      : (result.stdout?.trim() || "")
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean)
          .map((line) => {
            const hadTrailingSlash = line.endsWith("/") || line.endsWith("\\");
            const relative = line.startsWith(searchPath)
              ? line.slice(searchPath.length + 1)
              : path.relative(searchPath, line);
            return `${toPosixPath(relative)}${hadTrailingSlash && !relative.endsWith("/") ? "/" : ""}`;
          });

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
