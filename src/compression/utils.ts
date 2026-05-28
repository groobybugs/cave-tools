import { createHash } from "crypto";
import { spawnSync } from "child_process";
import { readFileSync, writeFileSync, mkdirSync, rmSync, statSync } from "fs";
import { homedir } from "os";
import { join } from "path";

// Dedup cache
const fileCache = new Map<string, string>(); // path -> hash

// Hit/miss counters
let cacheHits = 0;
let cacheMisses = 0;
let dedupSavedChars = 0;
const sessionStart = Date.now();
const sessionPid = process.pid;

interface ToolSavings {
  calls: number;
  rawChars: number;
  compressedChars: number;
  savedChars: number;
}

interface PersistedStats {
  pid: number;
  sessionStart: number;
  updatedAt: number;
  cache: {
    hits: number;
    misses: number;
    total: number;
    hitRate: number;
    filesTracked: number;
  };
  savings: {
    totalCalls: number;
    rawChars: number;
    compressedChars: number;
    compressionSavedChars: number;
    dedupSavedChars: number;
    savedChars: number;
    estimatedTokensSaved: number;
    byTool: Record<string, ToolSavings>;
  };
  budgets: Record<string, BudgetConfig>;
}

const savingsByTool: Record<string, ToolSavings> = {};

// Stats persistence path
const STATS_DIR = join(homedir(), ".cache", "cave-tools");
const SESSIONS_DIR = join(STATS_DIR, "sessions");
const SESSION_STATS_FILE = join(SESSIONS_DIR, `${sessionPid}.json`);

function estimateTokens(charCount: number): number {
  return Math.round(charCount / 4);
}

function buildStats(): PersistedStats {
  const cacheTotal = cacheHits + cacheMisses;
  const toolEntries = Object.values(savingsByTool);
  const rawChars = toolEntries.reduce((sum, tool) => sum + tool.rawChars, 0);
  const compressedChars = toolEntries.reduce(
    (sum, tool) => sum + tool.compressedChars,
    0,
  );
  const compressionSavedChars = toolEntries.reduce(
    (sum, tool) => sum + tool.savedChars,
    0,
  );
  const savedChars = compressionSavedChars + dedupSavedChars;
  const totalCalls = toolEntries.reduce((sum, tool) => sum + tool.calls, 0);

  return {
    pid: sessionPid,
    sessionStart,
    updatedAt: Date.now(),
    cache: {
      hits: cacheHits,
      misses: cacheMisses,
      total: cacheTotal,
      hitRate: cacheTotal > 0 ? cacheHits / cacheTotal : 0,
      filesTracked: fileCache.size,
    },
    savings: {
      totalCalls,
      rawChars,
      compressedChars,
      compressionSavedChars,
      dedupSavedChars,
      savedChars,
      estimatedTokensSaved: estimateTokens(savedChars),
      byTool: savingsByTool,
    },
    budgets: getAllBudgets(),
  };
}

function persistStats(): void {
  try {
    mkdirSync(SESSIONS_DIR, { recursive: true });
    writeFileSync(SESSION_STATS_FILE, JSON.stringify(buildStats()), "utf-8");
  } catch {
    // Non-fatal: stats persistence is best-effort
  }
}

export function getSessionStatsFile(): string {
  return SESSION_STATS_FILE;
}

export function getStatsSessionsDir(): string {
  return SESSIONS_DIR;
}

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
  const unchanged = cachedHash !== undefined && cachedHash === currentHash;
  if (unchanged) {
    cacheHits++;
    try {
      dedupSavedChars += Math.max(
        0,
        statSync(filePath).size - "<file unchanged since last read>".length,
      );
    } catch {
      // Non-fatal: cache hit stats still useful without size estimate.
    }
    persistStats();
  }
  return unchanged;
}

export function updateFileCache(filePath: string): void {
  const hash = getFileHash(filePath);
  if (hash) {
    fileCache.set(filePath, hash);
    cacheMisses++;
    persistStats();
  }
}

export function invalidateFileCache(filePath: string): void {
  fileCache.delete(filePath);
  persistStats();
}

export function getCacheStats(): {
  hits: number;
  misses: number;
  total: number;
  hitRate: number;
  filesTracked: number;
} {
  const total = cacheHits + cacheMisses;
  return {
    hits: cacheHits,
    misses: cacheMisses,
    total,
    hitRate: total > 0 ? cacheHits / total : 0,
    filesTracked: fileCache.size,
  };
}

export function getSavingsStats(): PersistedStats["savings"] {
  return buildStats().savings;
}

/**
 * Overall reduction percentage. Baseline includes the dedup baseline
 * (dedupSavedChars), since dedup'd reads contribute to savedChars but have
 * no rawChars entry of their own.
 */
export function reductionPercent(
  rawChars: number,
  savedChars: number,
  dedupSavedChars = 0,
): number {
  const baseline = rawChars + dedupSavedChars;
  if (baseline <= 0) return 0;
  return (savedChars / baseline) * 100;
}

/** rtk-gain-style ASCII meter, default 24 cells. */
export function efficiencyMeter(percent: number, width = 24): string {
  const clamped = Math.max(0, Math.min(100, percent));
  const filled = Math.round((clamped / 100) * width);
  return "\u2588".repeat(filled) + "\u2591".repeat(width - filled);
}

export function resetCache(): void {
  fileCache.clear();
  persistStats();
}

export function resetStats(): void {
  cacheHits = 0;
  cacheMisses = 0;
  dedupSavedChars = 0;
  for (const key of Object.keys(savingsByTool)) delete savingsByTool[key];
  try {
    rmSync(SESSION_STATS_FILE, { force: true });
  } catch {
    // Non-fatal: stats persistence is best-effort
  }
  persistStats();
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
  persistStats();
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
  const rawLength = text.length;
  const budget = getBudget(toolName);
  let result = stripAnsi(text);
  result = collapseBlankLines(result);
  result = truncateLines(
    result,
    budget.maxLines,
    budget.headLines,
    budget.tailLines,
  );
  const tool = savingsByTool[toolName] || {
    calls: 0,
    rawChars: 0,
    compressedChars: 0,
    savedChars: 0,
  };
  tool.calls++;
  tool.rawChars += rawLength;
  tool.compressedChars += result.length;
  tool.savedChars += Math.max(0, rawLength - result.length);
  savingsByTool[toolName] = tool;
  persistStats();
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
