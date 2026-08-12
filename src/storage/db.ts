import Database from "better-sqlite3";
import { existsSync, mkdirSync, readdirSync, readFileSync, statSync } from "fs";
import { homedir } from "os";
import { dirname, join } from "path";

export interface ToolSavings {
  calls: number;
  rawChars: number;
  compressedChars: number;
  savedChars: number;
}

export interface RtkStats {
  rewrites: number;
  alreadyWrapped: number;
  passthrough: number;
}

export interface BudgetConfig {
  maxLines: number;
  headLines: number;
  tailLines: number;
}

export interface BounceStats {
  totalReads: number;
  bounces: number;
  wastedChars: number;
}

export interface PersistedStats {
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

export interface ArchiveEntry {
  id: string;
  command: string;
  sizeChars: number;
  createdAt: number;
  contentPath: string;
}

interface SessionRow {
  pid: number;
  session_start: number;
  updated_at: number;
  ended_at: number | null;
  cache_hits: number;
  cache_misses: number;
  files_tracked: number;
  total_calls: number;
  raw_chars: number;
  compressed_chars: number;
  compression_saved_chars: number;
  dedup_saved_chars: number;
  saved_chars: number;
  rtk_rewrites: number;
  rtk_already_wrapped: number;
  rtk_passthrough: number;
  total_bounces: number;
  total_wasted_chars: number;
  budgets_json: string;
}

interface ToolRow {
  tool_name: string;
  calls: number;
  raw_chars: number;
  compressed_chars: number;
  saved_chars: number;
}

interface BounceRow {
  extension: string;
  total_reads: number;
  bounces: number;
  wasted_chars: number;
}

interface LifetimeRow {
  banked_sessions: number;
  calls: number;
  raw_chars: number;
  compressed_chars: number;
  compression_saved_chars: number;
  dedup_saved_chars: number;
  saved_chars: number;
  hits: number;
  misses: number;
  rtk_rewrites: number;
  rtk_already_wrapped: number;
  rtk_passthrough: number;
  updated_at: number;
  total_bounces: number;
  total_wasted_chars: number;
}

interface ArchiveRow {
  id: string;
  command: string;
  size_chars: number;
  created_at: number;
  content_path: string;
}

export function caveToolsDataDir(): string {
  const override = process.env.CAVE_TOOLS_DATA_DIR?.trim();
  return override || join(homedir(), ".cache", "cave-tools");
}

export function sessionsDir(): string {
  return join(caveToolsDataDir(), "sessions");
}

export function lifetimeFile(): string {
  return join(caveToolsDataDir(), "lifetime.json");
}

export function archiveDir(): string {
  return join(caveToolsDataDir(), "archives");
}

export function jobsDir(): string {
  return join(caveToolsDataDir(), "jobs");
}

export function readRegistryFile(): string {
  return join(
    process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude"),
    "cave-tools",
    "read-registry.txt",
  );
}

export function dbPath(): string {
  return join(caveToolsDataDir(), "cave-tools.db");
}

let db: Database.Database | null = null;

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

function openDb(): Database.Database {
  if (db) return db;
  const path = dbPath();
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  db = new Database(path);
  db.pragma("journal_mode = WAL");
  db.pragma("synchronous = NORMAL");
  db.pragma("busy_timeout = 5000");
  db.exec(`
    CREATE TABLE IF NOT EXISTS meta (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS lifetime_baseline (
      id INTEGER PRIMARY KEY CHECK (id = 1),
      banked_sessions INTEGER NOT NULL DEFAULT 0,
      calls INTEGER NOT NULL DEFAULT 0,
      raw_chars INTEGER NOT NULL DEFAULT 0,
      compressed_chars INTEGER NOT NULL DEFAULT 0,
      compression_saved_chars INTEGER NOT NULL DEFAULT 0,
      dedup_saved_chars INTEGER NOT NULL DEFAULT 0,
      saved_chars INTEGER NOT NULL DEFAULT 0,
      hits INTEGER NOT NULL DEFAULT 0,
      misses INTEGER NOT NULL DEFAULT 0,
      rtk_rewrites INTEGER NOT NULL DEFAULT 0,
      rtk_already_wrapped INTEGER NOT NULL DEFAULT 0,
      rtk_passthrough INTEGER NOT NULL DEFAULT 0,
      updated_at INTEGER NOT NULL DEFAULT 0,
      total_bounces INTEGER NOT NULL DEFAULT 0,
      total_wasted_chars INTEGER NOT NULL DEFAULT 0
    );

    CREATE TABLE IF NOT EXISTS sessions (
      pid INTEGER PRIMARY KEY,
      session_start INTEGER NOT NULL,
      updated_at INTEGER NOT NULL,
      ended_at INTEGER,
      cache_hits INTEGER NOT NULL DEFAULT 0,
      cache_misses INTEGER NOT NULL DEFAULT 0,
      files_tracked INTEGER NOT NULL DEFAULT 0,
      total_calls INTEGER NOT NULL DEFAULT 0,
      raw_chars INTEGER NOT NULL DEFAULT 0,
      compressed_chars INTEGER NOT NULL DEFAULT 0,
      compression_saved_chars INTEGER NOT NULL DEFAULT 0,
      dedup_saved_chars INTEGER NOT NULL DEFAULT 0,
      saved_chars INTEGER NOT NULL DEFAULT 0,
      rtk_rewrites INTEGER NOT NULL DEFAULT 0,
      rtk_already_wrapped INTEGER NOT NULL DEFAULT 0,
      rtk_passthrough INTEGER NOT NULL DEFAULT 0,
      total_bounces INTEGER NOT NULL DEFAULT 0,
      total_wasted_chars INTEGER NOT NULL DEFAULT 0,
      budgets_json TEXT NOT NULL DEFAULT '{}'
    );

    CREATE TABLE IF NOT EXISTS tool_stats (
      pid INTEGER NOT NULL,
      tool_name TEXT NOT NULL,
      calls INTEGER NOT NULL DEFAULT 0,
      raw_chars INTEGER NOT NULL DEFAULT 0,
      compressed_chars INTEGER NOT NULL DEFAULT 0,
      saved_chars INTEGER NOT NULL DEFAULT 0,
      PRIMARY KEY (pid, tool_name)
    );

    CREATE TABLE IF NOT EXISTS bounce_stats (
      pid INTEGER NOT NULL,
      extension TEXT NOT NULL,
      total_reads INTEGER NOT NULL DEFAULT 0,
      bounces INTEGER NOT NULL DEFAULT 0,
      wasted_chars INTEGER NOT NULL DEFAULT 0,
      PRIMARY KEY (pid, extension)
    );

    CREATE TABLE IF NOT EXISTS read_registry (
      path TEXT PRIMARY KEY,
      hash TEXT,
      last_read_at INTEGER NOT NULL
    );

    CREATE TABLE IF NOT EXISTS archive_meta (
      id TEXT PRIMARY KEY,
      command TEXT NOT NULL,
      size_chars INTEGER NOT NULL,
      created_at INTEGER NOT NULL,
      content_path TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_sessions_ended_at ON sessions(ended_at);
    CREATE INDEX IF NOT EXISTS idx_archive_created_at ON archive_meta(created_at);

    CREATE TABLE IF NOT EXISTS jobs (
      job_id TEXT PRIMARY KEY,
      pid INTEGER NOT NULL,
      session_id TEXT NOT NULL DEFAULT 'default',
      command TEXT NOT NULL,
      workdir TEXT,
      started_at INTEGER NOT NULL,
      ended_at INTEGER,
      exit_code INTEGER,
      signal TEXT,
      state TEXT NOT NULL DEFAULT 'running',
      log_path TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_jobs_started_at ON jobs(started_at);
  `);
  migrateLegacyData(db);
  return db;
}

function metaValue(conn: Database.Database, key: string): string | null {
  return (
    conn.prepare("SELECT value FROM meta WHERE key = ?").get(key) as
      | { value: string }
      | undefined
  )?.value ?? null;
}

function setMeta(conn: Database.Database, key: string, value: string): void {
  conn.prepare("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)").run(key, value);
}

function migrateLegacyData(conn: Database.Database): void {
  if (metaValue(conn, "legacy_migration_v1") === "done") return;

  const migrate = conn.transaction(() => {
    migrateLifetime(conn);
    migrateSessions(conn);
    migrateReadRegistry(conn);
    migrateArchives(conn);
    setMeta(conn, "legacy_migration_v1", "done");
  });
  migrate();
}

function migrateLifetime(conn: Database.Database): void {
  let lifetime = emptyLifetime();
  try {
    const parsed = JSON.parse(readFileSync(lifetimeFile(), "utf-8")) as Partial<LifetimeStats>;
    lifetime = { ...lifetime, ...parsed };
  } catch {
    // No legacy lifetime file yet.
  }

  conn.prepare(`
    INSERT OR IGNORE INTO lifetime_baseline (
      id, banked_sessions, calls, raw_chars, compressed_chars,
      compression_saved_chars, dedup_saved_chars, saved_chars,
      hits, misses, rtk_rewrites, rtk_already_wrapped, rtk_passthrough,
      updated_at, total_bounces, total_wasted_chars
    ) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    lifetime.bankedSessions,
    lifetime.calls,
    lifetime.rawChars,
    lifetime.compressedChars,
    lifetime.compressionSavedChars,
    lifetime.dedupSavedChars,
    lifetime.savedChars,
    lifetime.hits,
    lifetime.misses,
    lifetime.rtkRewrites,
    lifetime.rtkAlreadyWrapped,
    lifetime.rtkPassthrough,
    lifetime.updatedAt,
    lifetime.totalBounces,
    lifetime.totalWastedChars,
  );
}

function migrateSessions(conn: Database.Database): void {
  if (!existsSync(sessionsDir())) return;
  for (const name of readdirSync(sessionsDir())) {
    if (!name.endsWith(".json")) continue;
    try {
      const stats = JSON.parse(
        readFileSync(join(sessionsDir(), name), "utf-8"),
      ) as PersistedStats;
      upsertSessionStatsWithDb(conn, stats, null);
    } catch {
      // Skip corrupt legacy session files.
    }
  }
}

function migrateReadRegistry(conn: Database.Database): void {
  try {
    const raw = readFileSync(readRegistryFile(), "utf-8");
    const stmt = conn.prepare(
      "INSERT OR IGNORE INTO read_registry (path, hash, last_read_at) VALUES (?, NULL, ?)",
    );
    const now = Date.now();
    for (const line of raw.split("\n")) {
      const path = line.trim();
      if (path) stmt.run(path, now);
    }
  } catch {
    // No legacy registry.
  }
}

function migrateArchives(conn: Database.Database): void {
  if (!existsSync(archiveDir())) return;
  const stmt = conn.prepare(`
    INSERT OR IGNORE INTO archive_meta (id, command, size_chars, created_at, content_path)
    VALUES (?, ?, ?, ?, ?)
  `);
  for (const prefix of readdirSync(archiveDir())) {
    const prefixDir = join(archiveDir(), prefix);
    try {
      if (!statSync(prefixDir).isDirectory()) continue;
    } catch {
      continue;
    }
    for (const file of readdirSync(prefixDir)) {
      if (!file.endsWith(".meta.json")) continue;
      try {
        const meta = JSON.parse(readFileSync(join(prefixDir, file), "utf-8")) as {
          id: string;
          command: string;
          sizeChars: number;
          createdAt: number;
        };
        stmt.run(
          meta.id,
          meta.command,
          meta.sizeChars,
          meta.createdAt,
          join(prefixDir, `${meta.id}.txt`),
        );
      } catch {
        // Skip corrupt archive metadata.
      }
    }
  }
}

function parseBudgets(json: string): Record<string, BudgetConfig> {
  try {
    return JSON.parse(json) as Record<string, BudgetConfig>;
  } catch {
    return {};
  }
}

function rowToStats(
  conn: Database.Database,
  row: SessionRow,
): PersistedStats & { endedAt: number | null } {
  const tools = conn
    .prepare("SELECT * FROM tool_stats WHERE pid = ?")
    .all(row.pid) as ToolRow[];
  const bounces = conn
    .prepare("SELECT * FROM bounce_stats WHERE pid = ?")
    .all(row.pid) as BounceRow[];
  const byTool: Record<string, ToolSavings> = {};
  for (const tool of tools) {
    byTool[tool.tool_name] = {
      calls: tool.calls,
      rawChars: tool.raw_chars,
      compressedChars: tool.compressed_chars,
      savedChars: tool.saved_chars,
    };
  }
  const byExtension: Record<string, BounceStats> = {};
  for (const bounce of bounces) {
    byExtension[bounce.extension] = {
      totalReads: bounce.total_reads,
      bounces: bounce.bounces,
      wastedChars: bounce.wasted_chars,
    };
  }

  return {
    pid: row.pid,
    sessionStart: row.session_start,
    updatedAt: row.updated_at,
    endedAt: row.ended_at,
    cache: {
      hits: row.cache_hits,
      misses: row.cache_misses,
      total: row.cache_hits + row.cache_misses,
      hitRate:
        row.cache_hits + row.cache_misses > 0
          ? row.cache_hits / (row.cache_hits + row.cache_misses)
          : 0,
      filesTracked: row.files_tracked,
    },
    savings: {
      totalCalls: row.total_calls,
      rawChars: row.raw_chars,
      compressedChars: row.compressed_chars,
      compressionSavedChars: row.compression_saved_chars,
      dedupSavedChars: row.dedup_saved_chars,
      savedChars: row.saved_chars,
      estimatedTokensSaved: Math.round(row.saved_chars / 4),
      byTool,
    },
    rtk: {
      rewrites: row.rtk_rewrites,
      alreadyWrapped: row.rtk_already_wrapped,
      passthrough: row.rtk_passthrough,
    },
    budgets: parseBudgets(row.budgets_json),
    bounces: {
      totalBounces: row.total_bounces,
      totalWastedChars: row.total_wasted_chars,
      byExtension,
    },
  };
}

function upsertSessionStatsWithDb(
  conn: Database.Database,
  stats: PersistedStats,
  endedAt: number | null,
): void {
  conn.prepare(`
    INSERT INTO sessions (
      pid, session_start, updated_at, ended_at,
      cache_hits, cache_misses, files_tracked,
      total_calls, raw_chars, compressed_chars, compression_saved_chars,
      dedup_saved_chars, saved_chars,
      rtk_rewrites, rtk_already_wrapped, rtk_passthrough,
      total_bounces, total_wasted_chars, budgets_json
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(pid) DO UPDATE SET
      session_start = excluded.session_start,
      updated_at = excluded.updated_at,
      ended_at = COALESCE(sessions.ended_at, excluded.ended_at),
      cache_hits = excluded.cache_hits,
      cache_misses = excluded.cache_misses,
      files_tracked = excluded.files_tracked,
      total_calls = excluded.total_calls,
      raw_chars = excluded.raw_chars,
      compressed_chars = excluded.compressed_chars,
      compression_saved_chars = excluded.compression_saved_chars,
      dedup_saved_chars = excluded.dedup_saved_chars,
      saved_chars = excluded.saved_chars,
      rtk_rewrites = excluded.rtk_rewrites,
      rtk_already_wrapped = excluded.rtk_already_wrapped,
      rtk_passthrough = excluded.rtk_passthrough,
      total_bounces = excluded.total_bounces,
      total_wasted_chars = excluded.total_wasted_chars,
      budgets_json = excluded.budgets_json
  `).run(
    stats.pid,
    stats.sessionStart,
    stats.updatedAt,
    endedAt,
    stats.cache.hits,
    stats.cache.misses,
    stats.cache.filesTracked,
    stats.savings.totalCalls,
    stats.savings.rawChars,
    stats.savings.compressedChars,
    stats.savings.compressionSavedChars,
    stats.savings.dedupSavedChars,
    stats.savings.savedChars,
    stats.rtk.rewrites,
    stats.rtk.alreadyWrapped,
    stats.rtk.passthrough,
    stats.bounces.totalBounces,
    stats.bounces.totalWastedChars,
    JSON.stringify(stats.budgets),
  );

  conn.prepare("DELETE FROM tool_stats WHERE pid = ?").run(stats.pid);
  const toolStmt = conn.prepare(`
    INSERT INTO tool_stats (pid, tool_name, calls, raw_chars, compressed_chars, saved_chars)
    VALUES (?, ?, ?, ?, ?, ?)
  `);
  for (const [name, tool] of Object.entries(stats.savings.byTool)) {
    toolStmt.run(stats.pid, name, tool.calls, tool.rawChars, tool.compressedChars, tool.savedChars);
  }

  conn.prepare("DELETE FROM bounce_stats WHERE pid = ?").run(stats.pid);
  const bounceStmt = conn.prepare(`
    INSERT INTO bounce_stats (pid, extension, total_reads, bounces, wasted_chars)
    VALUES (?, ?, ?, ?, ?)
  `);
  for (const [ext, bounce] of Object.entries(stats.bounces.byExtension)) {
    bounceStmt.run(stats.pid, ext, bounce.totalReads, bounce.bounces, bounce.wastedChars);
  }
}

export function upsertSessionStats(stats: PersistedStats): void {
  const conn = openDb();
  const tx = conn.transaction(() => upsertSessionStatsWithDb(conn, stats, null));
  tx();
}

export function listSessionStats(includeEnded = true): Array<PersistedStats & { endedAt: number | null }> {
  const conn = openDb();
  const rows = conn
    .prepare(
      includeEnded
        ? "SELECT * FROM sessions ORDER BY updated_at DESC"
        : "SELECT * FROM sessions WHERE ended_at IS NULL ORDER BY updated_at DESC",
    )
    .all() as SessionRow[];
  return rows.map((row) => rowToStats(conn, row));
}

export function markSessionEnded(pid: number, endedAt = Date.now()): boolean {
  const result = openDb()
    .prepare("UPDATE sessions SET ended_at = ? WHERE pid = ? AND ended_at IS NULL")
    .run(endedAt, pid);
  return result.changes > 0;
}

function lifetimeFromRow(row: LifetimeRow | undefined): LifetimeStats {
  if (!row) return emptyLifetime();
  return {
    bankedSessions: row.banked_sessions,
    calls: row.calls,
    rawChars: row.raw_chars,
    compressedChars: row.compressed_chars,
    compressionSavedChars: row.compression_saved_chars,
    dedupSavedChars: row.dedup_saved_chars,
    savedChars: row.saved_chars,
    hits: row.hits,
    misses: row.misses,
    rtkRewrites: row.rtk_rewrites,
    rtkAlreadyWrapped: row.rtk_already_wrapped,
    rtkPassthrough: row.rtk_passthrough,
    updatedAt: row.updated_at,
    totalBounces: row.total_bounces,
    totalWastedChars: row.total_wasted_chars,
  };
}

export function getLifetimeStats(): LifetimeStats {
  const conn = openDb();
  const baseline = lifetimeFromRow(
    conn.prepare("SELECT * FROM lifetime_baseline WHERE id = 1").get() as
      | LifetimeRow
      | undefined,
  );
  const ended = conn
    .prepare("SELECT * FROM sessions WHERE ended_at IS NOT NULL")
    .all() as SessionRow[];
  for (const row of ended) {
    baseline.bankedSessions += 1;
    baseline.calls += row.total_calls;
    baseline.rawChars += row.raw_chars;
    baseline.compressedChars += row.compressed_chars;
    baseline.compressionSavedChars += row.compression_saved_chars;
    baseline.dedupSavedChars += row.dedup_saved_chars;
    baseline.savedChars += row.saved_chars;
    baseline.hits += row.cache_hits;
    baseline.misses += row.cache_misses;
    baseline.rtkRewrites += row.rtk_rewrites;
    baseline.rtkAlreadyWrapped += row.rtk_already_wrapped;
    baseline.rtkPassthrough += row.rtk_passthrough;
    baseline.totalBounces += row.total_bounces;
    baseline.totalWastedChars += row.total_wasted_chars;
    baseline.updatedAt = Math.max(baseline.updatedAt, row.updated_at);
  }
  return baseline;
}

export function registerReadPath(path: string, hash?: string): void {
  openDb()
    .prepare(
      "INSERT INTO read_registry (path, hash, last_read_at) VALUES (?, ?, ?) ON CONFLICT(path) DO UPDATE SET hash = COALESCE(excluded.hash, read_registry.hash), last_read_at = excluded.last_read_at",
    )
    .run(path, hash ?? null, Date.now());
}

export function recordArchive(entry: ArchiveEntry): void {
  openDb()
    .prepare(
      "INSERT OR REPLACE INTO archive_meta (id, command, size_chars, created_at, content_path) VALUES (?, ?, ?, ?, ?)",
    )
    .run(entry.id, entry.command, entry.sizeChars, entry.createdAt, entry.contentPath);
}

export function listArchives(): ArchiveEntry[] {
  const rows = openDb()
    .prepare("SELECT * FROM archive_meta ORDER BY created_at DESC")
    .all() as ArchiveRow[];
  return rows.map((row) => ({
    id: row.id,
    command: row.command,
    sizeChars: row.size_chars,
    createdAt: row.created_at,
    contentPath: row.content_path,
  }));
}

export function archiveStats(): { count: number; totalChars: number } {
  const row = openDb()
    .prepare("SELECT COUNT(*) AS count, COALESCE(SUM(size_chars), 0) AS totalChars FROM archive_meta")
    .get() as { count: number; totalChars: number };
  return row;
}

export function deleteArchive(id: string): void {
  openDb().prepare("DELETE FROM archive_meta WHERE id = ?").run(id);
}

// ── Background jobs ────────────────────────────────────────────────────────

export type JobState = "running" | "exited" | "killed" | "lost";

export interface JobRecord {
  jobId: string;
  pid: number;
  sessionId: string;
  command: string;
  workdir: string | null;
  startedAt: number;
  endedAt: number | null;
  exitCode: number | null;
  signal: string | null;
  state: JobState;
  logPath: string;
}

interface JobRow {
  job_id: string;
  pid: number;
  session_id: string;
  command: string;
  workdir: string | null;
  started_at: number;
  ended_at: number | null;
  exit_code: number | null;
  signal: string | null;
  state: string;
  log_path: string;
}

function jobFromRow(row: JobRow): JobRecord {
  return {
    jobId: row.job_id,
    pid: row.pid,
    sessionId: row.session_id,
    command: row.command,
    workdir: row.workdir,
    startedAt: row.started_at,
    endedAt: row.ended_at,
    exitCode: row.exit_code,
    signal: row.signal,
    state: row.state as JobState,
    logPath: row.log_path,
  };
}

export function insertJob(job: JobRecord): void {
  openDb()
    .prepare(
      `INSERT INTO jobs (job_id, pid, session_id, command, workdir, started_at, ended_at, exit_code, signal, state, log_path)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    )
    .run(
      job.jobId,
      job.pid,
      job.sessionId,
      job.command,
      job.workdir,
      job.startedAt,
      job.endedAt,
      job.exitCode,
      job.signal,
      job.state,
      job.logPath,
    );
}

export function updateJobState(
  jobId: string,
  update: { state: JobState; endedAt?: number | null; exitCode?: number | null; signal?: string | null },
): void {
  openDb()
    .prepare(
      `UPDATE jobs SET state = ?, ended_at = ?, exit_code = ?, signal = ? WHERE job_id = ?`,
    )
    .run(
      update.state,
      update.endedAt ?? null,
      update.exitCode ?? null,
      update.signal ?? null,
      jobId,
    );
}

export function getJob(jobId: string): JobRecord | null {
  const row = openDb().prepare("SELECT * FROM jobs WHERE job_id = ?").get(jobId) as
    | JobRow
    | undefined;
  return row ? jobFromRow(row) : null;
}

export function listJobs(sessionId?: string): JobRecord[] {
  const rows = (
    sessionId === undefined
      ? openDb().prepare("SELECT * FROM jobs ORDER BY started_at DESC").all()
      : openDb().prepare("SELECT * FROM jobs WHERE session_id = ? ORDER BY started_at DESC").all(sessionId)
  ) as JobRow[];
  return rows.map(jobFromRow);
}

export function listRunningJobs(): JobRecord[] {
  const rows = openDb()
    .prepare("SELECT * FROM jobs WHERE state = 'running' ORDER BY started_at DESC")
    .all() as JobRow[];
  return rows.map(jobFromRow);
}

export function deleteJob(jobId: string): void {
  openDb().prepare("DELETE FROM jobs WHERE job_id = ?").run(jobId);
}

export function listJobsEndedBefore(cutoffMs: number): JobRecord[] {
  const rows = openDb()
    .prepare(
      "SELECT * FROM jobs WHERE state != 'running' AND started_at < ? ORDER BY started_at DESC",
    )
    .all(cutoffMs) as JobRow[];
  return rows.map(jobFromRow);
}
