import {
  getAllBudgets,
  reductionPercent,
  isRtkAvailable,
} from "./compression/utils.js";
import {
  getLifetimeStats as getDbLifetimeStats,
  listSessionStats as listDbSessionStats,
  markSessionEnded,
} from "./storage/db.js";
import {
  mkdirSync,
  writeFileSync,
  lstatSync,
  realpathSync,
  statSync,
  renameSync,
  unlinkSync,
} from "fs";
import { randomBytes } from "crypto";
import { homedir } from "os";
import { join, basename } from "path";
import type { ToolBreakdown } from "./status-ui.js";

export interface StatusSummary {
  rtkAvailable: boolean;
  reductionPct: number;
  hitRatePct: number;
  cacheHits: number;
  cacheMisses: number;
  filesTracked: number;
  rtkRewrites: number;
  rtkAlreadyWrapped: number;
  rtkPassthrough: number;
  totalCalls: number;
  rawChars: number;
  compressedChars: number;
  compressionSavedChars: number;
  dedupSavedChars: number;
  savedChars: number;
  tokensSaved: number;
  liveSessions: number;
  endedSessions: number;
  visibleSessions: number;
  lastUpdate: number;
  hasData: boolean;
  budgets: ReturnType<typeof getAllBudgets>;
  byTool: ToolBreakdown[];
}

/** Everything the statusline badge needs; also the base of the full summary. */
type StatusCore = Omit<StatusSummary, "rtkAvailable" | "byTool">;

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

// Sync wrappers matching compression/utils.ts: stats are best-effort, so db
// failures degrade to empty/lifetime-only data instead of throwing.
function lifetimeStats(): ReturnType<typeof getDbLifetimeStats> {
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

function sessionStats(includeEnded = false): ReturnType<typeof listDbSessionStats> {
  try {
    return listDbSessionStats(includeEnded);
  } catch {
    return [];
  }
}

// Same rules as compression/utils.ts:pruneDeadSessions, but synchronous so the
// emit path can run from a process exit handler. A pid check that fails with
// EPERM means the process exists but belongs to someone else.
function pruneDeadSessions(): void {
  for (const session of sessionStats(false)) {
    const pid = session.pid;
    if (!Number.isFinite(pid) || pid === process.pid) continue;
    let alive: boolean;
    try {
      process.kill(pid, 0);
      alive = true;
    } catch (e) {
      alive = (e as NodeJS.ErrnoException)?.code === "EPERM";
    }
    if (!alive) markSessionEnded(pid);
  }
}

/** Aggregate lifetime + visible-session stats without the rtk probe/by-tool table. */
export function buildStatusCore(): StatusCore {
  pruneDeadSessions();
  const lifetime = lifetimeStats();
  const sessions = sessionStats(false);
  const liveSessions = sessions.filter((s) => isProcessAlive(s.pid));
  const visibleSessions = liveSessions.length > 0 ? liveSessions : sessions;
  const latestSession = visibleSessions
    .slice()
    .sort((a, b) => b.updatedAt - a.updatedAt)[0];
  const budgets = latestSession?.budgets || getAllBudgets();

  const totals = visibleSessions.reduce(
    (acc, s) => {
      const compressionSavedChars =
        s.savings.compressionSavedChars ?? s.savings.savedChars ?? 0;
      const dedupSavedChars = s.savings.dedupSavedChars ?? 0;
      acc.filesTracked += s.cache.filesTracked ?? 0;
      acc.hits += s.cache.hits ?? 0;
      acc.misses += s.cache.misses ?? 0;
      acc.rawChars += s.savings.rawChars ?? 0;
      acc.compressedChars += s.savings.compressedChars ?? 0;
      acc.compressionSavedChars += compressionSavedChars;
      acc.dedupSavedChars += dedupSavedChars;
      acc.savedChars += compressionSavedChars + dedupSavedChars;
      acc.calls += s.savings.totalCalls ?? 0;
      acc.rtkRewrites += s.rtk?.rewrites ?? 0;
      acc.rtkAlreadyWrapped += s.rtk?.alreadyWrapped ?? 0;
      acc.rtkPassthrough += s.rtk?.passthrough ?? 0;
      return acc;
    },
    {
      filesTracked: 0,
      hits: lifetime.hits,
      misses: lifetime.misses,
      rawChars: lifetime.rawChars,
      compressedChars: lifetime.compressedChars,
      compressionSavedChars: lifetime.compressionSavedChars,
      dedupSavedChars: lifetime.dedupSavedChars,
      savedChars: lifetime.savedChars,
      calls: lifetime.calls,
      rtkRewrites: lifetime.rtkRewrites,
      rtkAlreadyWrapped: lifetime.rtkAlreadyWrapped,
      rtkPassthrough: lifetime.rtkPassthrough,
    },
  );

  const cacheTotal = totals.hits + totals.misses;
  const hitRate = cacheTotal > 0 ? totals.hits / cacheTotal : 0;
  const compressionPct = reductionPercent(
    totals.rawChars,
    totals.compressionSavedChars,
  );

  const updateTimes = visibleSessions.map((s) => s.updatedAt);
  if (lifetime.updatedAt > 0) updateTimes.push(lifetime.updatedAt);
  const lastUpdate = updateTimes.length > 0 ? Math.max(...updateTimes) : 0;

  return {
    reductionPct: compressionPct,
    hitRatePct: hitRate * 100,
    cacheHits: totals.hits,
    cacheMisses: totals.misses,
    filesTracked: totals.filesTracked,
    rtkRewrites: totals.rtkRewrites,
    rtkAlreadyWrapped: totals.rtkAlreadyWrapped,
    rtkPassthrough: totals.rtkPassthrough,
    totalCalls: totals.calls,
    rawChars: totals.rawChars,
    compressedChars: totals.compressedChars,
    compressionSavedChars: totals.compressionSavedChars,
    dedupSavedChars: totals.dedupSavedChars,
    savedChars: totals.savedChars,
    tokensSaved: Math.round(totals.savedChars / 4),
    liveSessions: liveSessions.length,
    endedSessions: lifetime.bankedSessions,
    visibleSessions: visibleSessions.length,
    lastUpdate,
    hasData: visibleSessions.length > 0 || lifetime.bankedSessions > 0,
    budgets,
  };
}

export async function buildStatusSummary(): Promise<StatusSummary> {
  const core = buildStatusCore();
  const rtkAvailable = await isRtkAvailable();

  // Aggregate by-tool from all sessions (live + ended) for the breakdown table.
  const byToolMap = new Map<string, ToolBreakdown>();
  for (const s of sessionStats(true)) {
    const tools = s.savings?.byTool ?? {};
    for (const [name, t] of Object.entries(tools)) {
      const cur = byToolMap.get(name) ?? {
        name,
        calls: 0,
        rawChars: 0,
        savedChars: 0,
      };
      cur.calls += t.calls ?? 0;
      cur.rawChars += t.rawChars ?? 0;
      cur.savedChars += t.savedChars ?? 0;
      byToolMap.set(name, cur);
    }
  }

  return { ...core, rtkAvailable, byTool: [...byToolMap.values()] };
}

// Compact one-liner for the statusline suffix.
// Format: "↓38% • 1.1M tok" (or "↓38% • 18cache" when no tokens yet).
// Pure ASCII fallback for terminals without UTF-8 — controlled by env var.
export function formatStatuslineSuffix(
  s: Pick<StatusSummary, "hasData" | "reductionPct" | "tokensSaved" | "cacheHits">,
): string {
  if (!s.hasData) return "";
  const pct = s.reductionPct.toFixed(0);
  const tokens = s.tokensSaved;
  let amount: string;
  if (tokens >= 1_000_000) amount = `${(tokens / 1_000_000).toFixed(1)}M tok`;
  else if (tokens >= 1000) amount = `${(tokens / 1000).toFixed(0)}k tok`;
  else if (tokens > 0) amount = `${tokens} tok`;
  else amount = `${s.cacheHits}cache`;
  const arrow = process.env.CAVE_TOOLS_ASCII === "1" ? "v" : "↓";
  const bullet = process.env.CAVE_TOOLS_ASCII === "1" ? "|" : "•";
  return `${arrow}${pct}% ${bullet} ${amount}`;
}

/** Badge text the CLI would write for the current DB state. */
export function buildStatuslineSuffix(): string {
  return formatStatuslineSuffix(buildStatusCore());
}

// Symlink-safe, atomic write of the statusline suffix file: the new content
// lands in a temp file beside the target and is renamed into place, so a
// concurrent statusline read never sees a partial badge. Refuses to write
// through a symlink (would let a local attacker clobber an unrelated file).
// Verifies directory ownership when the parent itself is a legitimately
// symlinked ~/.claude (common in shared-config setups).
export function writeStatuslineSuffix(suffix: string): void {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude");
  const suffixPath = join(claudeDir, ".cave-tools-statusline-suffix");
  try {
    mkdirSync(claudeDir, { recursive: true });
    let realDir = claudeDir;
    try {
      const ls = lstatSync(claudeDir);
      if (ls.isSymbolicLink()) {
        realDir = realpathSync(claudeDir);
        const rs = statSync(realDir);
        if (!rs.isDirectory()) return;
        if (typeof process.getuid === "function" && rs.uid !== process.getuid()) return;
      }
    } catch {
      return;
    }
    const realPath = join(realDir, basename(suffixPath));
    try {
      if (lstatSync(realPath).isSymbolicLink()) return;
    } catch (e) {
      const code = (e as NodeJS.ErrnoException).code;
      if (code !== "ENOENT") return;
    }
    const tempPath = join(
      realDir,
      `.cave-tools-statusline-suffix.${process.pid}.${randomBytes(6).toString("hex")}.tmp`,
    );
    try {
      writeFileSync(tempPath, suffix, { mode: 0o600 });
      renameSync(tempPath, realPath);
    } catch {
      try {
        unlinkSync(tempPath);
      } catch {
        // Temp file may not exist; nothing to clean up.
      }
    }
  } catch {
    // Silent fail — suffix is best-effort. Statusline script handles absence.
  }
}

export interface StatuslineEmitterOptions {
  debounceMs?: number;
  build?: () => string;
  write?: (suffix: string) => void;
  disabled?: () => boolean;
}

export interface StatuslineEmitter {
  /** Mark the badge stale and (re)arm the trailing-edge timer. */
  schedule(): void;
  /** Write now if a write is pending, cancelling the timer. */
  flush(): void;
}

// The suffix is derived from SQLite, so every call makes it stale. A trailing
// edge debounce collapses bursts (and their SQLite reads) into one write.
export const STATUSLINE_EMIT_DEBOUNCE_MS = 300;

function statuslineEmitDisabled(): boolean {
  return process.env.CAVE_TOOLS_STATUSLINE_SAVINGS === "0";
}

export function createStatuslineEmitter(
  options: StatuslineEmitterOptions = {},
): StatuslineEmitter {
  const debounceMs = options.debounceMs ?? STATUSLINE_EMIT_DEBOUNCE_MS;
  const build = options.build ?? buildStatuslineSuffix;
  const write = options.write ?? writeStatuslineSuffix;
  const disabled = options.disabled ?? statuslineEmitDisabled;
  let timer: NodeJS.Timeout | null = null;
  let dirty = false;

  const emitNow = () => {
    if (!dirty) return;
    dirty = false;
    try {
      write(build());
    } catch {
      // Best-effort side effect: never fail the tool response.
    }
  };

  return {
    schedule() {
      if (disabled()) return;
      dirty = true;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = null;
        emitNow();
      }, debounceMs);
      // Never keep an otherwise idle MCP server alive just for the badge.
      timer.unref?.();
    },
    flush() {
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
      emitNow();
    },
  };
}

const defaultEmitter = createStatuslineEmitter();

/** Debounced write of the statusline suffix after a tool call. */
export function scheduleStatuslineEmit(): void {
  defaultEmitter.schedule();
}

/** Flush a pending badge write (process shutdown). */
export function flushStatuslineEmit(): void {
  defaultEmitter.flush();
}
