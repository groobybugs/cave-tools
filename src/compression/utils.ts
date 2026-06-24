import { createHash } from "crypto";
import { readFile, access } from "fs/promises";
import {
  getLifetimeStats as getDbLifetimeStats,
  listSessionStats as listDbSessionStats,
  markSessionEnded,
  registerReadPath as registerDbReadPath,
  sessionsDir,
  upsertSessionStats,
} from "../storage/db.js";
import type {
  BounceStats,
  BudgetConfig,
  LifetimeStats,
  PersistedStats,
  RtkStats,
  ToolSavings,
} from "../storage/db.js";

export type { LifetimeStats, PersistedStats } from "../storage/db.js";

const fileCache = new Map<string, string>();

let cacheHits = 0;
let cacheMisses = 0;
let dedupSavedChars = 0;
let rtkRewrites = 0;
let rtkAlreadyWrapped = 0;
let rtkPassthrough = 0;
let seqCounter = 0;
let totalBounces = 0;
let totalWastedChars = 0;
const sessionStart = Date.now();
const sessionPid = process.pid;
const READ_STUB = "<file unchanged since last read>";

export { READ_STUB };

const recentReads = new Map<string, BounceEvent[]>();
const perExtension = new Map<string, BounceStats>();
const recentlyEdited = new Map<string, number>();

const BOUNCE_WINDOW = 5;
const EDIT_FORCE_WINDOW = 10;
const BOUNCE_RATE_THRESHOLD = 0.3;

interface BounceEvent {
  seq: number;
  wasCompressed: boolean;
  charsSent: number;
}

const savingsByTool: Record<string, ToolSavings> = {};

const budgets: Record<string, BudgetConfig> = {
  bash: { maxLines: 80, headLines: 40, tailLines: 40 },
  read: { maxLines: 300, headLines: 150, tailLines: 150 },
  grep: { maxLines: 120, headLines: 60, tailLines: 60 },
  find: { maxLines: 120, headLines: 60, tailLines: 60 },
  ls: { maxLines: 120, headLines: 60, tailLines: 60 },
};

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
    rtk: {
      rewrites: rtkRewrites,
      alreadyWrapped: rtkAlreadyWrapped,
      passthrough: rtkPassthrough,
    },
    budgets: getAllBudgets(),
    bounces: getBounceStats(),
  };
}

async function persistStats(): Promise<void> {
  try {
    upsertSessionStats(buildStats());
  } catch {
    // Non-fatal: stats persistence is best-effort.
  }
}

export function getSessionStatsFile(): string {
  return "sqlite:cave-tools.db:sessions";
}

export function getStatsSessionsDir(): string {
  return sessionsDir();
}

export async function getLifetimeStats(): Promise<LifetimeStats> {
  try {
    return getDbLifetimeStats();
  } catch {
    return {
      bankedSessions: 0,
      calls: 0,
      rawChars: 0,
      compressedChars: 0,
      compressionSavedChars: 0,
      dedupSavedChars: 0,
      savedChars: 0,
      hits: 0,
      misses: 0,
      rtkRewrites: 0,
      rtkAlreadyWrapped: 0,
      rtkPassthrough: 0,
      updatedAt: 0,
      totalBounces: 0,
      totalWastedChars: 0,
    };
  }
}

export function listSessionStats(includeEnded = false): Array<PersistedStats & { endedAt: number | null }> {
  try {
    return listDbSessionStats(includeEnded);
  } catch {
    return [];
  }
}

export async function pruneDeadSessions(): Promise<number> {
  let removed = 0;
  for (const session of listSessionStats(false)) {
    const pid = session.pid;
    if (!Number.isFinite(pid) || pid === sessionPid) continue;
    let alive: boolean;
    try {
      process.kill(pid, 0);
      alive = true;
    } catch (e) {
      alive = (e as NodeJS.ErrnoException)?.code === "EPERM";
    }
    if (!alive) {
      if (markSessionEnded(pid)) removed++;
    }
  }
  return removed;
}

export async function getFileHash(filePath: string): Promise<string | null> {
  try {
    const content = await readFile(filePath, "utf-8");
    return createHash("sha256").update(content).digest("hex");
  } catch {
    return null;
  }
}

export async function isFileUnchanged(filePath: string): Promise<boolean> {
  let content: string;
  try {
    content = await readFile(filePath, "utf-8");
  } catch {
    return false;
  }
  const currentHash = createHash("sha256").update(content).digest("hex");
  const cachedHash = fileCache.get(filePath);
  const unchanged = cachedHash !== undefined && cachedHash === currentHash;
  if (unchanged) {
    cacheHits++;
    const wouldEmit = budgetedText(content, "read").length;
    dedupSavedChars += Math.max(0, wouldEmit - READ_STUB.length);
    void persistStats();
  }
  return unchanged;
}

export async function updateFileCache(filePath: string): Promise<void> {
  const hash = await getFileHash(filePath);
  if (hash) {
    fileCache.set(filePath, hash);
    cacheMisses++;
    void persistStats();
    try {
      registerDbReadPath(filePath, hash);
    } catch {
      // Registry persistence is advisory.
    }
  }
}

export function invalidateFileCache(filePath: string): void {
  fileCache.delete(filePath);
  void persistStats();
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

export function getRtkStats(): RtkStats {
  return buildStats().rtk;
}

export function reductionPercent(
  rawChars: number,
  savedChars: number,
  dedupSavedChars = 0,
): number {
  const baseline = rawChars + dedupSavedChars;
  if (baseline <= 0) return 0;
  return (savedChars / baseline) * 100;
}

export function efficiencyMeter(percent: number, width = 24): string {
  const clamped = Math.max(0, Math.min(100, percent));
  const filled = Math.round((clamped / 100) * width);
  return "█".repeat(filled) + "░".repeat(width - filled);
}

export function resetCache(): void {
  fileCache.clear();
  void persistStats();
}

export async function resetStats(): Promise<void> {
  cacheHits = 0;
  cacheMisses = 0;
  dedupSavedChars = 0;
  rtkRewrites = 0;
  rtkAlreadyWrapped = 0;
  rtkPassthrough = 0;
  seqCounter = 0;
  totalBounces = 0;
  totalWastedChars = 0;
  recentReads.clear();
  perExtension.clear();
  recentlyEdited.clear();
  for (const key of Object.keys(savingsByTool)) delete savingsByTool[key];
  void persistStats();
}

function extensionOf(filePath: string): string {
  const dot = filePath.lastIndexOf(".");
  return dot > 0 ? filePath.slice(dot).toLowerCase() : "";
}

export function recordRead(
  filePath: string,
  wasCompressed: boolean,
  charsSent: number,
): void {
  seqCounter++;
  const events = recentReads.get(filePath) ?? [];

  if (!wasCompressed && events.length > 0) {
    const last = events[events.length - 1];
    if (last.wasCompressed && seqCounter - last.seq <= BOUNCE_WINDOW) {
      totalBounces++;
      totalWastedChars += last.charsSent;

      const ext = extensionOf(filePath);
      if (ext) {
        const stats = perExtension.get(ext) ?? {
          totalReads: 0,
          bounces: 0,
          wastedChars: 0,
        };
        stats.bounces++;
        stats.wastedChars += last.charsSent;
        perExtension.set(ext, stats);
      }
    }
  }

  events.push({ seq: seqCounter, wasCompressed, charsSent });
  if (events.length > 10) events.shift();
  recentReads.set(filePath, events);

  const ext = extensionOf(filePath);
  if (ext) {
    const stats = perExtension.get(ext) ?? {
      totalReads: 0,
      bounces: 0,
      wastedChars: 0,
    };
    stats.totalReads++;
    perExtension.set(ext, stats);
  }

  void persistStats();
}

export function recordEdit(filePath: string): void {
  seqCounter++;
  recentlyEdited.set(filePath, seqCounter);
  void persistStats();
}

export function shouldForceFull(filePath: string): boolean {
  const editSeq = recentlyEdited.get(filePath);
  if (editSeq !== undefined && seqCounter - editSeq <= EDIT_FORCE_WINDOW) {
    return true;
  }

  const ext = extensionOf(filePath);
  if (!ext) return false;

  const stats = perExtension.get(ext);
  if (!stats || stats.totalReads < 3) return false;

  return stats.bounces / stats.totalReads >= BOUNCE_RATE_THRESHOLD;
}

export function getBounceStats(): {
  totalBounces: number;
  totalWastedChars: number;
  byExtension: Record<string, BounceStats>;
} {
  const byExtension: Record<string, BounceStats> = {};
  for (const [ext, stats] of perExtension) {
    byExtension[ext] = { ...stats };
  }
  return {
    totalBounces,
    totalWastedChars,
    byExtension,
  };
}

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
  void persistStats();
}

export function getAllBudgets(): Record<string, BudgetConfig> {
  return { ...budgets };
}

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

export function budgetedText(text: string, toolName: string): string {
  const budget = getBudget(toolName);
  const stripped = stripAnsi(text);
  const collapsed = collapseBlankLines(stripped);
  return truncateLines(
    collapsed,
    budget.maxLines,
    budget.headLines,
    budget.tailLines,
  );
}

export function applyBudget(text: string, toolName: string): string {
  const rawLength = text.length;
  const result = budgetedText(text, toolName);
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
  void persistStats();
  return result;
}

const MAX_STRUCTURED_INPUT_BYTES = 4 * 1024 * 1024;

function stripJsonInsignificantWhitespace(input: string): string {
  let out = "";
  let inString = false;
  let escaped = false;

  for (let i = 0; i < input.length; i++) {
    const c = input[i]!;

    if (inString) {
      out += c;
      if (escaped) {
        escaped = false;
      } else if (c === "\\") {
        escaped = true;
      } else if (c === '"') {
        inString = false;
      }
      continue;
    }

    if (c === '"') {
      inString = true;
      out += c;
    } else if (c === " " || c === "\t" || c === "\n" || c === "\r") {
      // drop insignificant whitespace outside strings
    } else {
      out += c;
    }
  }

  return out;
}

export function compactJson(text: string): string | null {
  if (text.length > MAX_STRUCTURED_INPUT_BYTES) return null;

  const trimmed = text.trimStart();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return null;

  // Validate before mutating — never reshape malformed JSON.
  try {
    JSON.parse(text);
  } catch {
    return null;
  }

  const compact = stripJsonInsignificantWhitespace(text);
  return compact.length < text.length ? compact : null;
}

export function compactJsonl(text: string): string | null {
  if (text.length > MAX_STRUCTURED_INPUT_BYTES) return null;

  const lines = text.split("\n");
  const out: string[] = [];

  for (const raw of lines) {
    const line = raw.trim();
    if (!line) continue;

    try {
      JSON.parse(line);
    } catch {
      return null;
    }

    out.push(stripJsonInsignificantWhitespace(line));
  }

  if (out.length === 0) return null;

  const compact = out.join("\n");
  return compact.length < text.length ? compact : null;
}

export function extractStructuredData(
  text: string,
  commandHint?: string,
): string {
  const compacted = compactJson(text) ?? compactJsonl(text);
  if (compacted !== null) {
    return compacted;
  }

  if (text.trim().startsWith("<")) {
    return text.replace(/>\s+</g, "><").replace(/\s{2,}/g, " ");
  }

  return text;
}

let rtkAvailableCache: boolean | null = null;

export async function isRtkAvailable(): Promise<boolean> {
  if (rtkAvailableCache !== null) return rtkAvailableCache;
  try {
    const { execFile } = await import("child_process");
    const { promisify } = await import("util");
    const execFileAsync = promisify(execFile);
    await execFileAsync("rtk", ["--version"], { timeout: 3000 });
    rtkAvailableCache = true;
  } catch {
    rtkAvailableCache = false;
  }
  return rtkAvailableCache;
}

export async function rewriteCommandWithRtk(command: string): Promise<string> {
  if (!(await isRtkAvailable())) {
    rtkPassthrough++;
    void persistStats();
    return command;
  }

  const { execFile } = await import("child_process");
  const { promisify } = await import("util");
  const execFileAsync = promisify(execFile);

  let rewritten: string;
  try {
    const result = await execFileAsync("rtk", ["rewrite", command], {
      encoding: "utf-8",
      timeout: 3000,
    });
    rewritten = result.stdout.trim();
  } catch {
    rtkPassthrough++;
    void persistStats();
    return command;
  }

  if (!rewritten) {
    rtkPassthrough++;
    void persistStats();
    return command;
  }

  if (rewritten === command.trim()) rtkAlreadyWrapped++;
  else rtkRewrites++;
  void persistStats();
  return rewritten;
}
