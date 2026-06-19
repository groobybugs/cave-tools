import { readdir, stat, access } from "fs/promises";
import path from "path";
import { spawn } from "child_process";
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

async function findWithNode(
  searchPath: string,
  pattern: string,
  limit: number,
): Promise<string[]> {
  const matcher = globToRegExp(
    pattern.includes("/") ? pattern : `**/${pattern}`,
  );
  const basenameMatcher = pattern.includes("/") ? null : globToRegExp(pattern);
  const results: string[] = [];
  const stack = [searchPath];

  while (stack.length > 0 && results.length < limit) {
    const dir = stack.pop()!;
    let entries: import("fs").Dirent[];
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      continue;
    }

    for (const entry of entries) {
      if (entry.name === ".git" || entry.name === "node_modules") continue;
      const fullPath = path.join(dir, entry.name);

      const relative = toPosixPath(path.relative(searchPath, fullPath));
      const isDir = entry.isDirectory();
      if (
        matcher.test(relative) ||
        basenameMatcher?.test(path.basename(relative))
      ) {
        results.push(relative + (isDir ? "/" : ""));
      }
      if (results.length >= limit) break;
      if (isDir) stack.push(fullPath);
    }
  }

  return results.sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

function spawnFd(fdArgs: string[]): Promise<{ error?: Error; stdout: string }> {
  return new Promise((resolve) => {
    const child = spawn("fd", fdArgs, { stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";

    child.stdout.on("data", (chunk: Buffer) => {
      stdout += chunk.toString();
    });
    child.stderr?.on("data", (chunk: Buffer) => {
      stderr += chunk.toString();
    });

    child.on("error", (error) => {
      resolve({ error, stdout: "" });
    });

    child.on("close", (code) => {
      if (code !== 0 && !stdout.trim()) {
        resolve({ error: new Error(stderr || `fd exited with code ${code}`), stdout: "" });
      } else {
        resolve({ stdout });
      }
    });
  });
}

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
    const searchPath = path.resolve(args.path ? String(args.path) : ".");
    const limit = Math.max(1, Number(args.limit) || DEFAULT_LIMIT);

    try {
      await access(searchPath);
    } catch {
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
    const result = await spawnFd(fdArgs);

    const lines = result.error
      ? await findWithNode(searchPath, pattern, limit)
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
