import { createHash } from "crypto";
import {
  readFile,
  writeFile,
  mkdir,
  rm,
  readdir,
  rename,
  access,
} from "fs/promises";
import { homedir } from "os";
import { join, dirname } from "path";

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

interface BounceEvent {
  seq: number;
  wasCompressed: boolean;
  charsSent: number;
}

interface BounceStats {
  totalReads: number;
  bounces: number;
  wastedChars: number;
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
  bounces: {
    totalBounces: number;
    totalWastedChars: number;
    byExtension: Record<string, BounceStats>;
  };
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
  totalBounces: number;
  totalWastedChars: number;
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
    totalBounces: 0,
    totalWastedChars: 0,
  };
}

const savingsByTool: Record<string, ToolSavings> = {};
const STATS_DIR = join(homedir(), ".cache", "cave-tools");
const SESSIONS_DIR = join(STATS_DIR, "sessions");
const SESSION_STATS_FILE = join(SESSIONS_DIR, `${sessionPid}.json`);
const LIFETIME_FILE = join(STATS_DIR, "lifetime.json");
const READ_REGISTRY_FILE = join(
  process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude"),
  "cave-tools",
  "read-registry.txt",
);

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
    await mkdir(SESSIONS_DIR, { recursive: true });
    await writeFile(SESSION_STATS_FILE, JSON.stringify(buildStats()), "utf-8");
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

export async function getLifetimeStats(): Promise<LifetimeStats> {
  try {
    const parsed = JSON.parse(await readFile(LIFETIME_FILE, "utf-8")) as Partial<LifetimeStats>;
    return { ...emptyLifetime(), ...parsed };
  } catch {
    return emptyLifetime();
  }
}

// Fold a (dead) session's totals into the persistent lifetime aggregate so its
// savings survive after the session file is pruned. Best-effort; never throws.
async function bankSession(filePath: string): Promise<void> {
  let session: PersistedStats;
  try {
    session = JSON.parse(await readFile(filePath, "utf-8")) as PersistedStats;
  } catch {
    return;
  }
  const lifetime = await getLifetimeStats();
  const s = session.savings;
  const c = session.cache;
  const r = session.rtk;
  const b = session.bounces;
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
  lifetime.totalBounces += b?.totalBounces ?? 0;
  lifetime.totalWastedChars += b?.totalWastedChars ?? 0;
  lifetime.updatedAt = Date.now();
  try {
    await mkdir(STATS_DIR, { recursive: true });
    await writeFile(LIFETIME_FILE, JSON.stringify(lifetime), "utf-8");
  } catch {
    // Non-fatal: lifetime persistence is best-effort.
  }
}

export async function pruneDeadSessions(): Promise<number> {
  let removed = 0;
  let names: string[];
  try {
    names = await readdir(SESSIONS_DIR);
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
      await bankSession(filePath);
      try {
        await rm(filePath, { force: true });
        removed++;
      } catch {
        // Non-fatal: best-effort cleanup.
      }
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
    void registerReadPath(filePath);
  }
}

// Append an absolute path to the flat-file read registry used by the
// strict-mode PreToolUse redirect hook. Deduplicates so the file never
// grows with duplicates. Writes atomically (temp + rename) with 0600
// permissions. Silent-fail on any filesystem error — the registry is
// best-effort.
async function registerReadPath(filePath: string): Promise<void> {
  try {
    await mkdir(dirname(READ_REGISTRY_FILE), { recursive: true });
    const paths = new Set<string>();
    try {
      const raw = await readFile(READ_REGISTRY_FILE, "utf-8");
      for (const line of raw.split("\n")) {
        const trimmed = line.trim();
        if (trimmed) paths.add(trimmed);
      }
    } catch {
      // File doesn't exist yet — start empty
    }
    if (paths.has(filePath)) return;
    paths.add(filePath);
    const tempPath = `${READ_REGISTRY_FILE}.tmp.${process.pid}`;
    await writeFile(tempPath, Array.from(paths).join("\n") + "\n", { mode: 0o600 });
    await rename(tempPath, READ_REGISTRY_FILE);
  } catch {
    // Silent fail — registry is advisory
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
  try {
    await rm(SESSION_STATS_FILE, { force: true });
  } catch {
    // Non-fatal: stats persistence is best-effort.
  }
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
