import { createHash } from "crypto";
import { spawnSync } from "child_process";
import { readFileSync } from "fs";

// Dedup cache
const fileCache = new Map<string, string>(); // path -> hash

export function getFileHash(filePath: string): string | null {
  try {
    const content = readFileSync(filePath, "utf-8");
    return createHash("sha256").update(content).digest("hex");
  } catch {
    return null;
  }
}

export function isFileUnchanged(filePath: string): boolean {
  const currentHash = getFileHash(filePath);
  if (!currentHash) return false;
  const cachedHash = fileCache.get(filePath);
  return cachedHash !== undefined && cachedHash === currentHash;
}

export function updateFileCache(filePath: string): void {
  const hash = getFileHash(filePath);
  if (hash) {
    fileCache.set(filePath, hash);
  }
}

export function invalidateFileCache(filePath: string): void {
  fileCache.delete(filePath);
}

export function getCacheStats(): {
  hits: number;
  total: number;
  hitRate: number;
} {
  // Simplified stats - in production, track actual hits
  return {
    hits: fileCache.size,
    total: fileCache.size,
    hitRate: 1.0,
  };
}

// Budget configuration
interface BudgetConfig {
  maxLines: number;
  headLines: number;
  tailLines: number;
}

const budgets: Record<string, BudgetConfig> = {
  bash: { maxLines: 80, headLines: 40, tailLines: 40 },
  read: { maxLines: 300, headLines: 150, tailLines: 150 },
  grep: { maxLines: 120, headLines: 60, tailLines: 60 },
  find: { maxLines: 120, headLines: 60, tailLines: 60 },
  ls: { maxLines: 120, headLines: 60, tailLines: 60 },
};

export function getBudget(toolName: string): BudgetConfig {
  return budgets[toolName] || { maxLines: 100, headLines: 50, tailLines: 50 };
}

export function setBudget(
  toolName: string,
  maxLines: number,
  headLines: number,
  tailLines: number,
): void {
  budgets[toolName] = { maxLines, headLines, tailLines };
}

export function getAllBudgets(): Record<string, BudgetConfig> {
  return { ...budgets };
}

// Compression utilities
export function stripAnsi(text: string): string {
  // eslint-disable-next-line no-control-regex
  return text.replace(/\u001b\[[0-9;]*m/g, "");
}

export function collapseBlankLines(text: string): string {
  return text.replace(/\n{3,}/g, "\n\n");
}

export function truncateLines(
  text: string,
  maxLines: number,
  headLines: number,
  tailLines: number,
): string {
  const lines = text.split("\n");
  if (lines.length <= maxLines) return text;

  const head = lines.slice(0, headLines);
  const tail = lines.slice(-tailLines);
  const omitted = lines.length - headLines - tailLines;

  return [...head, `\n... (${omitted} lines truncated) ...\n`, ...tail].join(
    "\n",
  );
}

export function applyBudget(text: string, toolName: string): string {
  const budget = getBudget(toolName);
  let result = stripAnsi(text);
  result = collapseBlankLines(result);
  result = truncateLines(
    result,
    budget.maxLines,
    budget.headLines,
    budget.tailLines,
  );
  return result;
}

// Stone Tablet - JSON/XML extraction
export function extractStructuredData(
  text: string,
  commandHint?: string,
): string {
  // Try JSON
  try {
    const parsed = JSON.parse(text);
    return JSON.stringify(parsed, null, 1); // Compact but readable
  } catch {
    // Not JSON
  }

  // Try XML (simplified)
  if (text.trim().startsWith("<")) {
    // Basic XML minification
    return text
      .replace(/>\s+</g, "><") // Remove whitespace between tags
      .replace(/\s{2,}/g, " "); // Collapse multiple spaces
  }

  return text;
}

// RTK detection
export function isRtkAvailable(): boolean {
  const result = spawnSync("rtk", ["--version"], {
    encoding: "utf-8",
    timeout: 3000,
  });
  return result.status === 0;
}

export function rewriteCommandWithRtk(command: string): string {
  if (command === "rtk" || command.startsWith("rtk ")) return command;
  if (!isRtkAvailable()) return command;

  const result = spawnSync("rtk", ["rewrite", command], {
    encoding: "utf-8",
    timeout: 200,
  });
  if (result.status !== 0) return command;

  const rewritten = result.stdout.trim();
  return rewritten || command;
}
