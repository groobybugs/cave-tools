import { createHash } from "crypto";
import { spawnSync } from "child_process";
import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync } from "fs";
import { homedir } from "os";
import { join } from "path";

const fileCache = new Map<string, string>();

let cacheHits = 0;
let cacheMisses = 0;
let dedupSavedChars = 0;
let rtkRewrites = 0;
let rtkAlreadyWrapped = 0;
let rtkPassthrough = 0;
const sessionStart = Date.now();
const sessionPid = process.pid;
const READ_STUB = "<file unchanged since last read>";

interface ToolSavings {
  calls: number;
  rawChars: number;
  compressedChars: number;
  savedChars: number;
}

interface RtkStats {
  rewrites: number;
  alreadyWrapped: number;
  passthrough: number;
}

interface BudgetConfig {
  maxLines: number;
  headLines: number;
  tailLines: number;
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
  rtk: RtkStats;
  budgets: Record<string, BudgetConfig>;
}

export interface LifetimeStats {
  bankedSessions: number;
  calls: number;
  rawChars: number;
  compressedChars: number;
  compressionSavedChars: number;
  dedupSavedChars: number;
  savedChars: number;
  hits: number;
  misses: number;
  rtkRewrites: number;
  rtkAlreadyWrapped: number;
  rtkPassthrough: number;
  updatedAt: number;
}

function emptyLifetime(): LifetimeStats {
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
  };
}

const savingsByTool: Record<string, ToolSavings> = {};
const STATS_DIR = join(homedir(), ".cache", "cave-tools");
const SESSIONS_DIR = join(STATS_DIR, "sessions");
const SESSION_STATS_FILE = join(SESSIONS_DIR, `${sessionPid}.json`);
const LIFETIME_FILE = join(STATS_DIR, "lifetime.json");

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
  };
}

function persistStats(): void {
  try {
    mkdirSync(SESSIONS_DIR, { recursive: true });
    writeFileSync(SESSION_STATS_FILE, JSON.stringify(buildStats()), "utf-8");
  } catch {
    // Non-fatal: stats persistence is best-effort.
  }
}

export function getSessionStatsFile(): string {
  return SESSION_STATS_FILE;
}

export function getStatsSessionsDir(): string {
  return SESSIONS_DIR;
}

export function getLifetimeStats(): LifetimeStats {
  try {
    const parsed = JSON.parse(readFileSync(LIFETIME_FILE, "utf-8")) as Partial<LifetimeStats>;
    return { ...emptyLifetime(), ...parsed };
  } catch {
    return emptyLifetime();
  }
}

// Fold a (dead) session's totals into the persistent lifetime aggregate so its
// savings survive after the session file is pruned. Best-effort; never throws.
function bankSession(filePath: string): void {
  let session: PersistedStats;
  try {
    session = JSON.parse(readFileSync(filePath, "utf-8")) as PersistedStats;
  } catch {
    return; // Unreadable/corrupt — nothing to bank.
  }
  const lifetime = getLifetimeStats();
  const s = session.savings;
  const c = session.cache;
  const r = session.rtk;
  const compressionSavedChars = s?.compressionSavedChars ?? s?.savedChars ?? 0;
  const dedupSavedChars = s?.dedupSavedChars ?? 0;
  lifetime.bankedSessions += 1;
  lifetime.calls += s?.totalCalls ?? 0;
  lifetime.rawChars += s?.rawChars ?? 0;
  lifetime.compressedChars += s?.compressedChars ?? 0;
  lifetime.compressionSavedChars += compressionSavedChars;
  lifetime.dedupSavedChars += dedupSavedChars;
  lifetime.savedChars += compressionSavedChars + dedupSavedChars;
  lifetime.hits += c?.hits ?? 0;
  lifetime.misses += c?.misses ?? 0;
  lifetime.rtkRewrites += r?.rewrites ?? 0;
  lifetime.rtkAlreadyWrapped += r?.alreadyWrapped ?? 0;
  lifetime.rtkPassthrough += r?.passthrough ?? 0;
  lifetime.updatedAt = Date.now();
  try {
    mkdirSync(STATS_DIR, { recursive: true });
    writeFileSync(LIFETIME_FILE, JSON.stringify(lifetime), "utf-8");
  } catch {
    // Non-fatal: lifetime persistence is best-effort.
  }
}

export function pruneDeadSessions(): number {
  let removed = 0;
  let names: string[];
  try {
    names = readdirSync(SESSIONS_DIR);
  } catch {
    return 0;
  }
  for (const name of names) {
    if (!name.endsWith(".json")) continue;
    const pid = parseInt(name.slice(0, -".json".length), 10);
    if (!Number.isFinite(pid) || pid === sessionPid) continue;
    let alive: boolean;
    try {
      process.kill(pid, 0);
      alive = true;
    } catch (e) {
      alive = (e as NodeJS.ErrnoException)?.code === "EPERM";
    }
    if (!alive) {
      const filePath = join(SESSIONS_DIR, name);
      bankSession(filePath); // Preserve savings before deleting the file.
      try {
        rmSync(filePath, { force: true });
        removed++;
      } catch {
        // Non-fatal: best-effort cleanup.
      }
    }
  }
  return removed;
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
  let content: string;
  try {
    content = readFileSync(filePath, "utf-8");
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
  persistStats();
}

export function resetStats(): void {
  cacheHits = 0;
  cacheMisses = 0;
  dedupSavedChars = 0;
  rtkRewrites = 0;
  rtkAlreadyWrapped = 0;
  rtkPassthrough = 0;
  for (const key of Object.keys(savingsByTool)) delete savingsByTool[key];
  try {
    rmSync(SESSION_STATS_FILE, { force: true });
  } catch {
    // Non-fatal: stats persistence is best-effort.
  }
  persistStats();
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
  persistStats();
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
  persistStats();
  return result;
}

export function extractStructuredData(
  text: string,
  commandHint?: string,
): string {
  try {
    const parsed = JSON.parse(text);
    return JSON.stringify(parsed, null, 1);
  } catch {
    // Not JSON.
  }

  if (text.trim().startsWith("<")) {
    return text.replace(/>\s+</g, "><").replace(/\s{2,}/g, " ");
  }

  return text;
}

export function isRtkAvailable(): boolean {
  const result = spawnSync("rtk", ["--version"], {
    encoding: "utf-8",
    timeout: 3000,
  });
  return result.status === 0;
}

export function rewriteCommandWithRtk(command: string): string {
  if (!isRtkAvailable()) {
    rtkPassthrough++;
    persistStats();
    return command;
  }

  const result = spawnSync("rtk", ["rewrite", command], {
    encoding: "utf-8",
    timeout: 3000,
  });
  const rewritten = result.stdout.trim();

  // Some RTK versions emit valid rewrites with non-zero status. Trust stdout.
  if (!rewritten) {
    rtkPassthrough++;
    persistStats();
    return command;
  }

  if (rewritten === command.trim()) rtkAlreadyWrapped++;
  else rtkRewrites++;
  persistStats();
  return rewritten;
}
