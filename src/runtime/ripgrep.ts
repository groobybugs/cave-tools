import { spawn } from "child_process";
import path from "path";
import { readFile, readdir } from "fs/promises";
import { toPosixPath } from "./path.js";

const MAX_RECORD_BYTES = 64 * 1024;
const MAX_LINE_LENGTH = 2000;

export interface GrepMatch {
  path: string;
  line: number;
  text: string;
}

function isInvalidPattern(stderr: string): boolean {
  return stderr.includes("regex parse error") || stderr.includes("error parsing regex");
}

function globToRegExp(pattern: string): RegExp {
  let source = "";
  for (let i = 0; i < pattern.length; i++) {
    const char = pattern[i]!;
    const next = pattern[i + 1];
    if (char === "*" && next === "*") {
      source += ".*";
      i++;
    } else if (char === "*") source += "[^/]*";
    else if (char === "?") source += "[^/]";
    else if (".+^${}()|[]\\".includes(char)) source += `\\${char}`;
    else source += char;
  }
  return new RegExp(`^${source}$`);
}

function spawnRg(args: string[], cwd: string): Promise<{ stdout: string; stderr: string; code: number | null; error?: Error }> {
  return new Promise((resolve) => {
    const child = spawn("rg", args, { cwd, stdio: ["ignore", "pipe", "pipe"], windowsHide: true });
    let stdout = "";
    let stderr = "";
    child.stdout?.on("data", (chunk: Buffer) => (stdout += chunk.toString("utf-8")));
    child.stderr?.on("data", (chunk: Buffer) => (stderr += chunk.toString("utf-8")));
    child.on("error", (error) => resolve({ stdout: "", stderr: "", code: null, error }));
    child.on("close", (code) => resolve({ stdout, stderr, code }));
  });
}

async function findWithNode(cwd: string, pattern: string, limit: number): Promise<string[]> {
  const matcher = globToRegExp(pattern.includes("/") ? pattern : `**/${pattern}`);
  const basenameMatcher = pattern.includes("/") ? null : globToRegExp(pattern);
  const results: string[] = [];
  const stack = [cwd];
  while (stack.length && results.length < limit) {
    const dir = stack.pop()!;
    let entries: import("fs").Dirent[];
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      if (entry.name === ".git") continue;
      const full = path.join(dir, entry.name);
      const relative = toPosixPath(path.relative(cwd, full));
      if (entry.isDirectory()) stack.push(full);
      if (!entry.isFile()) continue;
      if (matcher.test(relative) || basenameMatcher?.test(path.basename(relative))) results.push(relative);
      if (results.length >= limit) break;
    }
  }
  return results.sort((a, b) => a.localeCompare(b));
}

export async function rgFiles(cwd: string, pattern: string, limit: number, hidden = true): Promise<string[]> {
  const args = [
    "--no-config",
    "--files",
    ...(hidden ? ["--hidden"] : []),
    `--glob=${pattern}`,
    "--glob=!**/.git/**",
    ".",
  ];
  const result = await spawnRg(args, cwd);
  if (result.error) return findWithNode(cwd, pattern, limit);
  if (result.code !== 0 && result.code !== 1) throw new Error(result.stderr.trim() || `ripgrep exited with code ${result.code}`);
  return result.stdout
    .split("\n")
    .map((line) => line.trim().replace(/^(?:\.[\\/])+/u, "").replace(/^[\\/]+/u, "").replaceAll("\\", "/"))
    .filter(Boolean)
    .slice(0, limit);
}

export async function rgGrep(input: {
  cwd: string;
  pattern: string;
  file?: string;
  include?: string;
  limit: number;
  ignoreCase?: boolean;
  literal?: boolean;
}): Promise<{ matches: GrepMatch[]; truncated: boolean }> {
  const args = [
    "--no-config",
    "--json",
    "--hidden",
    "--no-messages",
    ...(input.ignoreCase ? ["--ignore-case"] : []),
    ...(input.literal ? ["--fixed-strings"] : []),
    ...(input.include ? [`--glob=${input.include}`] : []),
    "--glob=!**/.git/**",
    "--",
    input.pattern,
    input.file ?? ".",
  ];
  const result = await spawnRg(args, input.cwd);
  if (result.error) throw new Error(`Failed to run ripgrep: ${result.error.message}`);
  if (result.code === 2 && isInvalidPattern(result.stderr)) throw new Error(result.stderr.trim());
  if (result.code !== 0 && result.code !== 1 && result.code !== 2) {
    throw new Error(result.stderr.trim() || `ripgrep exited with code ${result.code}`);
  }

  const matches: GrepMatch[] = [];
  for (const line of result.stdout.split("\n")) {
    if (!line.trim()) continue;
    if (Buffer.byteLength(line, "utf8") > MAX_RECORD_BYTES) throw new Error(`Ripgrep JSON record exceeded ${MAX_RECORD_BYTES} bytes`);
    let event: any;
    try {
      event = JSON.parse(line);
    } catch {
      throw new Error("Invalid ripgrep JSON output");
    }
    if (event.type !== "match") continue;
    const relative = String(event.data?.path?.text ?? "")
      .replace(/^(?:\.[\\/])+/u, "")
      .replace(/^[\\/]+/u, "")
      .replaceAll("\\", "/");
    const text = String(event.data?.lines?.text ?? "").replace(/\r?\n$/, "");
    matches.push({
      path: relative,
      line: Number(event.data?.line_number),
      text: text.length > MAX_LINE_LENGTH ? `${text.slice(0, MAX_LINE_LENGTH)}...` : text,
    });
    if (matches.length > input.limit) break;
  }
  return { matches: matches.slice(0, input.limit), truncated: matches.length > input.limit || result.code === 2 };
}

export async function readLineWindow(filePath: string, line: number, context: number): Promise<Array<{ line: number; text: string; match: boolean }>> {
  const content = await readFile(filePath, "utf-8");
  const lines = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
  const start = Math.max(1, line - context);
  const end = Math.min(lines.length, line + context);
  const result: Array<{ line: number; text: string; match: boolean }> = [];
  for (let current = start; current <= end; current++) {
    const raw = lines[current - 1] ?? "";
    result.push({ line: current, text: raw.length > 500 ? `${raw.slice(0, 500)}... (truncated)` : raw, match: current === line });
  }
  return result;
}
