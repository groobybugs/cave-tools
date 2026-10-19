import { createHash } from "crypto";
import { resetSnapshots } from "./snapshots.js";
import { readFile, access, stat } from "fs/promises";
import {
  getLifetimeStats as getDbLifetimeStats,
  listSessionStats as listDbSessionStats,
  markSessionEnded,
  registerReadPath as registerDbReadPath,
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
import { redactSecrets } from "./redact.js";

export type { LifetimeStats, PersistedStats } from "../storage/db.js";

interface CacheEntry {
  hash: string;
  mtime: number;
  deliveries: Map<string, number>;
  readCount: number;
}

export interface FileCacheUpdate {
  deliveryKey?: string;
  content?: string | Buffer;
  hash?: string;
  mtimeMs?: number;
  emittedChars?: number;
}

const DEFAULT_SESSION = "default";
const DEFAULT_DELIVERY_KEY = "default";
const MAX_DELIVERIES_PER_FILE = 256;

// Per-session dedup cache: sessionId -> filePath -> entry. Keying by sessionId
// isolates opencode subagents (which share this MCP process) so a different
// agent/session always gets fresh content on first read, while same-session
// re-reads still dedup. When no _meta.sessionID is provided by the client,
// callers fall back to DEFAULT_SESSION (preserving prior single-session behavior).
//
// Content cache is in-memory per process. Parallel MCP processes do not share
// file bodies (by design — correctness > hit-rate). SQLite under
// CAVE_TOOLS_DATA_DIR (default ~/.cache/cave-tools) stores aggregate stats only.
const fileCache = new Map<string, Map<string, CacheEntry>>();

function trackedFileCount(): number {
  let n = 0;
  for (const sessionMap of fileCache.values()) n += sessionMap.size;
  return n;
}

// Env-based subagent detection (client-agnostic fallback). Ported from lean-ctx
// is_subagent_context(): when a known subagent env signal is present, force
// fresh reads so stubs are never returned to a subagent that never saw the
// content. Computed once per process (env is fixed at spawn).
// Known signals only — unknown CLAUDE_CODE_* / *SUBAGENT* keys are logged once
// and ignored (no guessing). TASK_ID-style keys (CURSOR_TASK_ID, CLINE_TASK_ID,
// TASK_ID, FOO_TASK_ID) count as subagent context.
const KNOWN_SUBAGENT_KEYS = new Set([
  "CLINE_TASK_ID",
  "CLAUDE_CODE_SUBAGENT",
  "ANTHROPIC_SUBAGENT",
  "CURSOR_TASK_ID",
]);
const TASK_ID_STYLE = /(?:^|_)TASK_ID$/;
const UNKNOWN_SUBAGENT_CANDIDATE = /(?:SUBAGENT|^CLAUDE_CODE_)/;

let subagentContextCached: boolean | null = null;
let unknownSubagentEnvLogged = false;

/** CLI tool calls: stderr lands in the agent's Bash output, so skip the diagnostic. */
export function suppressSubagentEnvLog(): void {
  unknownSubagentEnvLogged = true;
}

function envNonEmpty(value: string | undefined): value is string {
  return typeof value === "string" && value.length > 0;
}

function isKnownSubagentKey(key: string): boolean {
  return KNOWN_SUBAGENT_KEYS.has(key) || TASK_ID_STYLE.test(key);
}

function logUnknownSubagentEnvOnce(triggeredKeys: string[]): void {
  if (unknownSubagentEnvLogged) return;
  unknownSubagentEnvLogged = true;
  const triggered = new Set(triggeredKeys);
  const unknown: string[] = [];
  for (const key of Object.keys(process.env)) {
    if (triggered.has(key)) continue;
    if (!UNKNOWN_SUBAGENT_CANDIDATE.test(key)) continue;
    if (!envNonEmpty(process.env[key])) continue;
    unknown.push(key);
  }
  if (unknown.length > 0) {
    console.error(
      `cave-tools: unknown subagent-like env keys (ignored; not forcing fresh): ${unknown.sort().join(", ")}`,
    );
  }
}

/** Test hook: clear the process-level subagent detection cache. */
export function resetSubagentContextCache(): void {
  subagentContextCached = null;
  unknownSubagentEnvLogged = false;
}

export function isSubagentContext(): boolean {
  if (subagentContextCached !== null) return subagentContextCached;

  const force = process.env.CAVE_TOOLS_FORCE_FRESH;
  const triggered: string[] = [];
  let detected = false;

  if (force === "1" || force === "true") {
    detected = true;
    triggered.push("CAVE_TOOLS_FORCE_FRESH");
  }

  for (const key of Object.keys(process.env)) {
    if (!envNonEmpty(process.env[key])) continue;
    if (!isKnownSubagentKey(key)) continue;
    detected = true;
    triggered.push(key);
  }

  logUnknownSubagentEnvOnce(triggered);
  subagentContextCached = detected;
  return detected;
}

let cacheHits = 0;
let cacheMisses = 0;
let dedupSavedChars = 0;
/** Output chars avoided by line-range/move edits (no old_string echo). */
let rangeEchoSavedChars = 0;
let rangeEditCount = 0;
let rtkRewrites = 0;
let rtkAlreadyWrapped = 0;
let rtkPassthrough = 0;
const RTK_TOP_PASSTHROUGH_CAP = 50;
const rtkTopPassthrough = new Map<string, number>();
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

// Trimmed bash/grep/web output is recoverable through the archive handle, so
// these stay tight. "web" covers cave__webfetch and cave__websearch.
const budgets: Record<string, BudgetConfig> = {
  bash: { maxLines: 50, headLines: 15, tailLines: 35, maxChars: 6000 },
  read: { maxLines: 200, headLines: 100, tailLines: 100 },
  grep: { maxLines: 60, headLines: 30, tailLines: 30, maxChars: 6000 },
  web: { maxLines: 120, headLines: 60, tailLines: 60, maxChars: 8000 },
  find: { maxLines: 120, headLines: 60, tailLines: 60 },
  ls: { maxLines: 60, headLines: 30, tailLines: 30 },
};

function estimateTokens(charCount: number): number {
  return Math.round(charCount / 4);
}

export function buildStats(): PersistedStats {
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
      filesTracked: trackedFileCount(),
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

let statsPersistScheduled = false;

function persistStats(): void {
  // Coalesce bursts into one latest-snapshot transaction. setImmediate alone
  // deferred each call but still performed one synchronous SQLite write/event.
  if (statsPersistScheduled) return;
  statsPersistScheduled = true;
  setImmediate(() => {
    statsPersistScheduled = false;
    try {
      upsertSessionStats(buildStats());
    } catch {
      // Non-fatal: stats persistence is best-effort.
    }
  });
}

// Write the stats snapshot now. Short-lived callers (the CLI) use this before
// exiting, since persistStats() defers the write to setImmediate.
export function flushStats(): void {
  try {
    upsertSessionStats(buildStats());
  } catch {
    // Non-fatal: stats persistence is best-effort.
  }
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

export function hashContent(content: string | Buffer): string {
  return createHash("sha256").update(content).digest("hex");
}

export async function getFileHash(filePath: string): Promise<string | null> {
  try {
    return hashContent(await readFile(filePath));
  } catch {
    return null;
  }
}

export async function isFileUnchanged(
  filePath: string,
  sessionId: string = DEFAULT_SESSION,
  deliveryKey: string = DEFAULT_DELIVERY_KEY,
): Promise<boolean> {
  // Env-detected subagent context → never stub (force fresh). Different session
  // → no entry → fresh. Same session re-read + hash match + delivered → stub.
  if (isSubagentContext()) return false;
  const sessionMap = fileCache.get(sessionId);
  if (!sessionMap) return false;
  const entry = sessionMap.get(filePath);
  if (!entry) return false;

  let st;
  try {
    st = await stat(filePath);
  } catch {
    return false;
  }

  // mtime fast-path: if on-disk mtime matches the cached one, content is
  // guaranteed unchanged — skip the expensive full re-hash. Ported from lean-ctx
  // cache.rs stored_mtime + is_cache_entry_stale_verified.
  if (st.mtimeMs !== entry.mtime) {
    // mtime changed — re-hash to confirm (could be a touch with same content).
    const currentHash = await getFileHash(filePath);
    if (currentHash === null || currentHash !== entry.hash) {
      return false;
    }
    entry.mtime = st.mtimeMs;
  }

  // Stub only when this exact representation/range was delivered. A prior
  // signatures read must not suppress a later full read; likewise new offsets,
  // limits, and numbered views must be emitted once before they can dedup.
  const wouldEmit = entry.deliveries.get(deliveryKey);
  if (wouldEmit === undefined) return false;

  cacheHits++;
  dedupSavedChars += Math.max(0, wouldEmit - READ_STUB.length);
  void persistStats();
  return true;
}

export async function updateFileCache(
  filePath: string,
  sessionId: string = DEFAULT_SESSION,
  update: FileCacheUpdate = {},
): Promise<void> {
  let content = update.content;
  let hash = update.hash;
  let mtime = update.mtimeMs;

  if (!hash) {
    try {
      content = content ?? (await readFile(filePath));
      hash = hashContent(content);
    } catch {
      return;
    }
  }
  if (mtime === undefined) {
    try {
      mtime = (await stat(filePath)).mtimeMs;
    } catch {
      mtime = 0;
    }
  }

  let sessionMap = fileCache.get(sessionId);
  if (!sessionMap) {
    sessionMap = new Map();
    fileCache.set(sessionId, sessionMap);
  }
  const existing = sessionMap.get(filePath);
  const readCount = (existing?.readCount ?? 0) + 1;
  const deliveries = existing?.hash === hash ? existing.deliveries : new Map<string, number>();
  let emittedChars = update.emittedChars;
  if (emittedChars === undefined && content !== undefined) {
    const text = typeof content === "string" ? content : content.toString("utf-8");
    emittedChars = budgetedText(text, "read").length;
  }
  const deliveryKey = update.deliveryKey ?? DEFAULT_DELIVERY_KEY;
  if (!deliveries.has(deliveryKey) && deliveries.size >= MAX_DELIVERIES_PER_FILE) {
    const oldest = deliveries.keys().next().value;
    if (oldest !== undefined) deliveries.delete(oldest);
  }
  deliveries.set(deliveryKey, emittedChars ?? 0);
  sessionMap.set(filePath, {
    hash,
    mtime,
    deliveries,
    readCount,
  });
  cacheMisses++;
  void persistStats();
  const capturedPath = filePath;
  const capturedHash = hash;
  setImmediate(() => {
    try {
      registerDbReadPath(capturedPath, capturedHash);
    } catch {
      // Registry persistence is advisory — never block or fail a read.
    }
  });
}

export function markFullDelivered(
  filePath: string,
  sessionId: string = DEFAULT_SESSION,
): void {
  const entry = fileCache.get(sessionId)?.get(filePath);
  if (entry && !entry.deliveries.has(DEFAULT_DELIVERY_KEY)) {
    entry.deliveries.set(DEFAULT_DELIVERY_KEY, 0);
  }
}

// Dynamic stub message: references the last-delivering read #N for this session
// and tells the caller how to force a re-read. Ported from lean-ctx's
// "Use fresh=true to force re-read" hint + caveman-code's read #N index.
export function readStubFor(
  filePath: string,
  sessionId: string = DEFAULT_SESSION,
): string {
  const entry = fileCache.get(sessionId)?.get(filePath);
  const readCount = entry?.readCount ?? 0;
  const tag = sessionId === DEFAULT_SESSION ? "default" : sessionId;
  return `<file unchanged since read #${readCount} (session ${tag}). Use force=true to re-read.>`;
}

export function invalidateFileCache(filePath: string): void {
  // A disk change is global — clear the path across ALL sessions so every
  // caller's next read re-delivers fresh content.
  for (const sessionMap of fileCache.values()) {
    sessionMap.delete(filePath);
  }
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
    filesTracked: trackedFileCount(),
  };
}

export function getSavingsStats(): PersistedStats["savings"] {
  return buildStats().savings;
}

export function getRtkStats(): RtkStats {
  return buildStats().rtk;
}

export function getRtkTopPassthrough(): { prefix: string; count: number }[] {
  return [...rtkTopPassthrough.entries()]
    .map(([prefix, count]) => ({ prefix, count }))
    .sort((a, b) => b.count - a.count || a.prefix.localeCompare(b.prefix));
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
  resetSnapshots();
  void persistStats();
}

export async function resetStats(): Promise<void> {
  cacheHits = 0;
  cacheMisses = 0;
  dedupSavedChars = 0;
  rtkRewrites = 0;
  rtkAlreadyWrapped = 0;
  rtkPassthrough = 0;
  rtkTopPassthrough.clear();
  rtkAvailableCache = null;
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

/** Count chars of old span not re-emitted by the model (range/move edits). */
export function recordRangeEchoSaved(chars: number): void {
  if (chars > 0) rangeEchoSavedChars += chars;
  rangeEditCount++;
  void persistStats();
}

export function getRangeEditStats(): {
  edits: number;
  echoSavedChars: number;
  estimatedTokensSaved: number;
} {
  return {
    edits: rangeEditCount,
    echoSavedChars: rangeEchoSavedChars,
    estimatedTokensSaved: estimateTokens(rangeEchoSavedChars),
  };
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
  maxChars?: number,
): void {
  budgets[toolName] = maxChars ? { maxLines, headLines, tailLines, maxChars } : { maxLines, headLines, tailLines };
  void persistStats();
}

export function getAllBudgets(): Record<string, BudgetConfig> {
  return { ...budgets };
}

export function stripAnsi(text: string): string {
  // Comprehensive ANSI escape stripper: covers SGR (color/style), cursor
  // movement, screen clearing, and C1 (0x9b) control sequences. Ported from
  // caveman-code cave-tool-compression.ts — the previous /\u001b\[[0-9;]*m/g
  // only stripped SGR codes and left cursor/screen escapes in output.
  // eslint-disable-next-line no-control-regex
  return text.replace(
    /[\u001b\u009b](?:[@-Z\\-_]|\[[0-9;]*[ -/]*[@-~]|[@-_][0-9;]*[@-~]?|[@-_]|[0-9;]*m)/g,
    "",
  );
}

export function collapseBlankLines(text: string): string {
  // Handle CRLF (Windows) line endings too — ported from caveman-code.
  return text.replace(/(\r?\n){3,}/g, "\n\n");
}

// Runs of adjacent lines that differ only inside number runs (timestamps,
// counters, request ids) collapse to the first line plus a count marker.
// "GET /api 200 12ms" x 200 pays 2 lines, not 200. Ported from the caveman
// engine's digit-masked repetition compressor: first occurrence always kept
// verbatim, our own markers never re-collapse (idempotent), and the result is
// only emitted when it is actually smaller.
const RUN_LENGTH_MIN = 4;
const RUN_MARKER_PREFIX = "[... ";
const RUN_MARKER_RE = /^\[\.\.\. \d+ similar lines omitted/;

function maskDigitRuns(line: string): string {
  return line.replace(/\d+/g, "#");
}

export function collapseRepeatedLines(text: string): string {
  const lines = text.split("\n");
  const out: string[] = [];
  let collapsed = false;

  let i = 0;
  while (i < lines.length) {
    const key = maskDigitRuns(lines[i]!);
    let j = i + 1;
    while (j < lines.length && maskDigitRuns(lines[j]!) === key) j++;
    const runLen = j - i;
    out.push(lines[i]!);
    if (
      runLen >= RUN_LENGTH_MIN &&
      /[a-zA-Z]/.test(lines[i]!) && // pure-numeric runs (seq, counters, bare timestamps) ARE the content
      lines[i]!.trim().length > 0 &&
      !RUN_MARKER_RE.test(lines[i]!) &&
      !lines[i]!.startsWith(RUN_MARKER_PREFIX)
    ) {
      out.push(`[... ${runLen - 1} similar lines omitted (cave: run-length) ...]`);
      collapsed = true;
    } else {
      for (let k = i + 1; k < j; k++) out.push(lines[k]!);
    }
    i = j;
  }

  if (!collapsed) return text;
  const result = out.join("\n");
  return result.length < text.length ? result : text;
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

// Global truncation safety net — a final defense applied after per-tool
// budgets so no output can exceed this regardless of tool config. Ported from
// caveman-code's truncateLongOutput (MAX_LINES=500, head 200 / tail 100).
const GLOBAL_MAX_LINES = 500;
const GLOBAL_HEAD_LINES = 200;
const GLOBAL_TAIL_LINES = 100;

export function truncateLongOutput(text: string): string {
  return truncateLines(text, GLOBAL_MAX_LINES, GLOBAL_HEAD_LINES, GLOBAL_TAIL_LINES);
}

// Room kept for the "... (N chars truncated) ..." marker and its newlines.
const CHAR_MARKER_RESERVE = 48;

// Character cap applied after the line budget. Splits the kept budget between
// head and tail by the tool's head/tail line ratio and cuts at line
// boundaries; cuts inside a line only when that line alone exceeds maxChars.
export function truncateChars(
  text: string,
  maxChars: number,
  headLines: number,
  tailLines: number,
): string {
  if (text.length <= maxChars) return text;
  const keep = Math.max(0, maxChars - CHAR_MARKER_RESERVE);
  const ratio = headLines + tailLines > 0 ? headLines / (headLines + tailLines) : 0.5;
  const headBudget = Math.floor(keep * ratio);
  const tailBudget = keep - headBudget;
  const lines = text.split("\n");
  const n = lines.length;

  let h = 0;
  let used = 0;
  while (h < n && used + lines[h].length + 1 <= headBudget) used += lines[h++].length + 1;
  let t = n;
  used = 0;
  while (t - 1 >= h && used + lines[t - 1].length + 1 <= tailBudget) used += lines[--t].length + 1;

  let head = lines.slice(0, h).join("\n");
  let tail = lines.slice(t).join("\n");
  if (h === 0 && lines[0].length > maxChars) head = lines[0].slice(0, headBudget);
  if (t === n && lines[n - 1].length > maxChars) tail = lines[n - 1].slice(-tailBudget);

  const dropped = text.length - head.length - tail.length;
  const marker = `... (${dropped} chars truncated) ...`;
  return [head, marker, tail].filter((part) => part !== "").join("\n");
}

/** Same cap as cave__read MAX_BYTES — skip strip/collapse/truncate when already small. */
export const BUDGET_FAST_PATH_MAX_BYTES = 50 * 1024;

function countLines(text: string): number {
  let n = 1;
  for (let i = 0; i < text.length; i++) {
    if (text.charCodeAt(i) === 10) n++;
  }
  return n;
}

export function fitsBudget(
  text: string,
  toolName: string,
  maxBytes: number = BUDGET_FAST_PATH_MAX_BYTES,
): boolean {
  if (Buffer.byteLength(text, "utf8") > maxBytes) return false;
  const budget = getBudget(toolName);
  if (budget.maxChars && text.length > budget.maxChars) return false;
  return countLines(text) <= budget.maxLines;
}

export function budgetedText(text: string, toolName: string): string {
  if (fitsBudget(text, toolName)) return text;
  const budget = getBudget(toolName);
  const stripped = stripAnsi(text);
  const collapsed = collapseBlankLines(stripped);
  const truncated = truncateLines(
    collapsed,
    budget.maxLines,
    budget.headLines,
    budget.tailLines,
  );
  // Final global safety net (defense-in-depth, may be a no-op when the per-tool
  // budget is already tighter).
  const capped = truncateLongOutput(truncated);
  return budget.maxChars
    ? truncateChars(capped, budget.maxChars, budget.headLines, budget.tailLines)
    : capped;
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

const RTK_REWRITE_TIMEOUT_MS = 3000;
const RTK_TOKEN_MAX_LEN = 80;
const ENV_ASSIGN_PREFIX =
  /^(?:[A-Za-z_][A-Za-z0-9_]*\s*=\s*(?:'[^']*'|"[^"]*"|\S*)\s+)/;
const RTK_TOKEN = /^(?:["'`]rtk(?:\.exe)?["'`]|rtk(?:\.exe)?)(?=\s|$)/i;

let rtkAvailableCache: boolean | null = null;

export function resetRtkAvailabilityCache(): void {
  rtkAvailableCache = null;
}

/** Peel exactly one leading `rtk` argv token (optional quotes / ENV=value prefix). */
export function stripLeadingRtk(command: string): {
  command: string;
  stripped: boolean;
} {
  let s = command.trim();
  if (!s) return { command, stripped: false };

  const quoted = /^(["'`])([\s\S]*)\1$/.exec(s);
  if (quoted) s = quoted[2]!.trim();

  let prefix = "";
  let rest = s;
  while (true) {
    const env = ENV_ASSIGN_PREFIX.exec(rest);
    if (!env) break;
    prefix += env[0]!;
    rest = rest.slice(env[0]!.length);
  }

  const rtkTok = RTK_TOKEN.exec(rest);
  if (!rtkTok) return { command, stripped: false };

  const after = rest.slice(rtkTok[0].length).replace(/^\s+/, "");
  const rebuilt = `${prefix}${after}`.trim();
  if (!rebuilt) return { command, stripped: false };
  return { command: rebuilt, stripped: true };
}

function isPathToken(tok: string): boolean {
  if (tok.startsWith("~") || tok.startsWith("/") || tok.startsWith("\\")) return true;
  if (/^[A-Za-z]:[\\/]/.test(tok)) return true;
  return tok.includes("/") || tok.includes("\\");
}

function redactPassthroughToken(tok: string): string {
  const clipped = tok.length > RTK_TOKEN_MAX_LEN ? tok.slice(0, RTK_TOKEN_MAX_LEN) : tok;
  if (isPathToken(tok)) return "<path>";
  return redactSecrets(clipped);
}

function passthroughPrefix(command: string): string {
  const tokens: string[] = [];
  for (const part of command.trim().split(/\s+/)) {
    if (!part) continue;
    if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(part)) continue;
    tokens.push(redactPassthroughToken(part));
    if (tokens.length === 2) break;
  }
  return tokens.join(" ");
}

function notePassthrough(command: string): void {
  rtkPassthrough++;
  const key = passthroughPrefix(command);
  if (key) {
    const prev = rtkTopPassthrough.get(key);
    if (prev !== undefined) {
      rtkTopPassthrough.set(key, prev + 1);
    } else if (rtkTopPassthrough.size < RTK_TOP_PASSTHROUGH_CAP) {
      rtkTopPassthrough.set(key, 1);
    } else {
      let minKey: string | undefined;
      let min = Infinity;
      for (const [k, count] of rtkTopPassthrough) {
        if (count < min) {
          min = count;
          minKey = k;
        }
      }
      if (minKey !== undefined) {
        rtkTopPassthrough.delete(minKey);
        rtkTopPassthrough.set(key, 1);
      }
    }
    if (process.env.CAVE_TOOLS_DEBUG === "1") {
      console.error(
        `[cave-tools] rtk passthrough ${key} n=${rtkTopPassthrough.get(key) ?? 0}`,
      );
    }
  }
  void persistStats();
}

function runRtkRewrite(command: string): Promise<{ stdout: string; timedOut: boolean }> {
  return new Promise((resolve) => {
    void import("child_process")
      .then(({ execFile }) => {
        execFile(
          "rtk",
          ["rewrite", command],
          {
            encoding: "utf-8",
            timeout: RTK_REWRITE_TIMEOUT_MS,
            windowsHide: true,
          },
          (err, stdout) => {
            const out = typeof stdout === "string" ? stdout : "";
            if (!err) {
              resolve({ stdout: out, timedOut: false });
              return;
            }
            const code = (err as NodeJS.ErrnoException).code;
            const timedOut =
              code === "ETIMEDOUT" || Boolean((err as { killed?: boolean }).killed);
            resolve({ stdout: timedOut ? "" : out, timedOut });
          },
        );
      })
      .catch(() => resolve({ stdout: "", timedOut: false }));
  });
}

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

export async function rewriteCommandWithRtk(
  command: string,
  opts?: { incomingAlreadyWrapped?: boolean },
): Promise<string> {
  try {
    const peeled = stripLeadingRtk(command);
    const toRewrite = peeled.command;
    const incomingWrapped = Boolean(opts?.incomingAlreadyWrapped) || peeled.stripped;

    if (!(await isRtkAvailable())) {
      notePassthrough(toRewrite);
      return toRewrite;
    }

    const { stdout, timedOut } = await runRtkRewrite(toRewrite);
    const rewritten = stdout.trim();

    if (timedOut || !rewritten) {
      notePassthrough(toRewrite);
      return toRewrite;
    }

    if (
      incomingWrapped ||
      rewritten === toRewrite.trim() ||
      rewritten === command.trim()
    ) {
      rtkAlreadyWrapped++;
    } else {
      rtkRewrites++;
    }
    void persistStats();
    return rewritten;
  } catch {
    notePassthrough(command);
    return command;
  }
}
