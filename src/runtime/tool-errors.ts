// Privacy-safe persistent log of tool error outcomes (best-effort).
//
// What this module does:
// - Records tool calls that end in a returned `isError` result ("soft"
//   error) or a handler throw ("thrown" error) at the shared dispatch
//   layer. Successful calls are never recorded.
// - Never changes what the caller sees: results and throws pass through
//   untouched. Classification is transient (in memory only).
// - Best-effort and bounded: a busy or unwritable store only drops
//   records, never breaks a tool call. There is no guarantee every
//   failure is durably recorded.
//
// Privacy rules (hard requirements, not aspirations):
// - NEVER stored: raw error messages, stacks, prompts, tool result bodies,
//   file contents, commands, env values, full paths, arbitrary unknown
//   argument key strings, or arbitrary tool/mode strings supplied by the
//   caller (they could literally be secrets). Tool names pass through a
//   fixed known allowlist (anything else becomes "unknown_tool"); mode is
//   a finite set (anything else becomes "default").
// - Stored per event: timestamp, event id, allowlisted tool name, outcome
//   (`soft`/`thrown`), a stable finite reason code, elapsedMs,
//   mode/operation shape, batch index parsed from anchored `edits[N]`
//   prefixes only, sha256-truncated session/file correlation hashes,
//   allowlisted argument keys plus their JS types/counts, and (for throws)
//   the allowlisted error kind.
// - Agent attribution is never guessed: "unknown" unless the supported
//   coarse subagent signal (compression/utils isSubagentContext) fires.
//
// Reliability rules:
// - Only bounded in-memory work (arg summary, hashes, one queue slot) runs
//   on the tool-call path. SQLite I/O happens on later event-loop turns:
//   one bounded batch (<=50 rows) per setImmediate turn, no timers, no
//   retry spin — a locked store defers to the next tool event. Expect a
//   small best-effort delay (and possible loss under lock or shutdown),
//   never a stall: lock wait is zero and failures are counted with
//   process-level give-up.
// - Flush is scheduled with setImmediate (not a microtask) so the resolved
//   tool result is delivered before the sink does its first write. For
//   short-lived CLI processes, `flushToolErrorQueue()` is called at the
//   controlled exit funnel (cli-tools finish); tests drain explicitly.
// - Disable policy (OR): a call is dropped when the caller env disables
//   logging (per-client override, so a daemon client's
//   CAVE_TOOLS_ERROR_LOG=0 always wins for that client) OR when the
//   process env disables it at flush time. A record gated-in at event
//   time may still be dropped if the process disables before flush; a
//   disabled call is never recorded. Raw env values are never stored
//   (mode is a finite allowlist).
// - Storage: additive `tool_errors` table in the existing SQLite db.
//   The db file is pre-created 0600 and existing dirs/files/sidecars are
//   tightened to 0700/0600 without following symlinks; symlink or
//   non-regular db paths refuse logging for that directory.
// - Retention: 7 days, 5000 rows max, enforced on successful flush.

import Database from "better-sqlite3";
import { createHash, randomUUID } from "crypto";
import {
  chmodSync,
  closeSync,
  constants as fsConstants,
  existsSync,
  fstatSync,
  lstatSync,
  mkdirSync,
  openSync,
  statSync,
} from "fs";
import { join } from "path";
import { caveToolsDataDir } from "../storage/data-dir.js";
import { isSubagentContext } from "../compression/utils.js";

export const TOOL_ERROR_RETENTION_DAYS = 7;
export const TOOL_ERROR_MAX_ROWS = 5000;
export const TOOL_ERROR_QUEUE_CAP = 256;

const TOOL_ERROR_FLUSH_BATCH = 50;
const TOOL_ERROR_MAX_FLUSH_FAILURES = 5;
const SESSION_HASH_LEN = 16;
const FILE_HASH_LEN = 16;

export type ToolErrorOutcome = "soft" | "thrown";

export type ToolErrorReasonCode =
  | "invalid_args"
  | "not_found"
  | "unreadable"
  | "stale_hash"
  | "stale_range"
  | "stale_write"
  | "not_unique"
  | "overlap"
  | "conflict"
  | "exec_failed"
  | "upstream"
  | "internal"
  | "unknown";

const REASON_CODES: ReadonlySet<string> = new Set([
  "invalid_args",
  "not_found",
  "unreadable",
  "stale_hash",
  "stale_range",
  "stale_write",
  "not_unique",
  "overlap",
  "conflict",
  "exec_failed",
  "upstream",
  "internal",
  "unknown",
]);

export interface ToolErrorRecord {
  id: string;
  createdAt: number;
  tool: string;
  outcome: ToolErrorOutcome;
  reasonCode: ToolErrorReasonCode;
  elapsedMs: number;
  mode: string;
  operation: string | null;
  batchIndex: number | null;
  agent: string;
  sessionHash: string;
  fileHash: string | null;
  argKeys: string[];
  argTypes: Record<string, string>;
  argCount: number;
  unknownArgCount: number;
  errorKind: string | null;
}

// --- Caller-controlled string sanitization ---

// Fixed allowlist, registered once by dispatch (which owns TOOL_NAMES).
// Anything else — including attacker-controlled tool names — is stored as
// "unknown_tool", never the raw string.
let knownToolNames: ReadonlySet<string> = new Set();

/** Called once by dispatch with its TOOL_NAMES. Not for general use. */
export function setKnownToolNames(names: readonly string[]): void {
  knownToolNames = new Set(names);
}

export function sanitizeToolName(name: unknown): string {
  return typeof name === "string" && knownToolNames.has(name) ? name : "unknown_tool";
}

// Finite mode set. Absent or unrecognized values become "default" — the
// current mode is never guessed.
const KNOWN_MODES: ReadonlySet<string> = new Set(["off", "hint", "enforce", "strict"]);

export function sanitizeMode(raw: unknown): string {
  return typeof raw === "string" && KNOWN_MODES.has(raw) ? raw : "default";
}

const DISABLED_VALUES: ReadonlySet<string> = new Set(["0", "false", "off", "no"]);

/** Caller env wins (daemon per-client env); falls back to process env. */
export function isToolErrorLoggingEnabled(env?: NodeJS.ProcessEnv): boolean {
  try {
    const fromCaller = env?.CAVE_TOOLS_ERROR_LOG;
    const raw = (fromCaller ?? process.env.CAVE_TOOLS_ERROR_LOG ?? "").trim().toLowerCase();
    return !DISABLED_VALUES.has(raw);
  } catch {
    return true;
  }
}

function firstText(result: unknown, maxChars = 1000): string {
  try {
    const content = (result as { content?: unknown }).content;
    if (!Array.isArray(content)) return "";
    for (const block of content) {
      if (
        block !== null &&
        typeof block === "object" &&
        (block as { type?: unknown }).type === "text" &&
        typeof (block as { text?: unknown }).text === "string"
      ) {
        return ((block as { text: string }).text ?? "").slice(0, maxChars);
      }
    }
  } catch {
    // Shape is caller-controlled; never let inspection throw.
  }
  return "";
}

function stripErrorPrefix(text: string): string {
  return text.replace(/^\s*error\s*:\s*/i, "");
}

// Parse only the anchored generated batch prefix `edits[N]:` at the start
// of the (prefix-stripped) text. Anything else yields null.
export function parseBatchIndex(text: string): number | null {
  try {
    const m = /^edits\[(\d+)\]:/.exec(stripErrorPrefix(text).trimStart());
    if (!m) return null;
    const n = Number(m[1]);
    return Number.isSafeInteger(n) ? n : null;
  } catch {
    return null;
  }
}

// Classify a returned soft-error body. Transient only: the input text is
// never persisted, only the returned finite code.
//
// Anchored fixed generated prefixes are checked BEFORE general substring
// text, so content windows / source hints embedded later in the message
// cannot change the classification.
export function classifySoftReasonCode(tool: string, text: string): ToolErrorReasonCode {
  const t = stripErrorPrefix(text).trimStart();
  if (!t) return "unknown";
  const anchored = t.toLowerCase();

  // 1. Anchored batch remainder: `edits[N]: <inner>` classifies on <inner>.
  const batch = /^edits\[\d+\]:\s*/.exec(anchored);
  const body = batch ? anchored.slice(batch[0].length) : anchored;
  const isBash = tool === "cave__bash" || tool === "cave__bash_status";
  const isNet = tool === "cave__webfetch" || tool === "cave__websearch";

  // 2. Anchored fixed generated prefixes (edit stale/not-unique/not-found).
  if (body.startsWith("expected_hash mismatch")) return "stale_hash";
  if (body.startsWith("expected_range_checksum mismatch")) return "stale_range";
  if (body.startsWith("hash failed")) return "stale_hash";
  if (body.startsWith("checksum failed")) return "stale_range";
  if (body.startsWith("old_string is not unique")) return "not_unique";
  if (body.startsWith("old_string not found")) return "not_found";
  if (body.startsWith("edits overlap") || body.startsWith("cannot mix move and range")) return "overlap";
  if (body.startsWith("file changed after it was read")) return "stale_write";
  if (
    body.startsWith("expected_hash must be") ||
    body.startsWith("expected_range_checksum must be") ||
    body.startsWith("expected_hash must be hex") ||
    body.startsWith("strict:")
  ) {
    return "invalid_args";
  }
  if (
    body.startsWith("file not found") ||
    body.startsWith("path not found") ||
    body.startsWith("unknown job") ||
    body.startsWith("unknown archive") ||
    body.startsWith("no such file") ||
    body.startsWith("no such directory") ||
    body.startsWith("does not exist") ||
    body.startsWith("not a directory")
  ) {
    return "not_found";
  }

  // 3. Bash exit markers are wrapper-generated, never user output.
  if (isBash && (body.includes("[exit:") || body.includes("[timed out") || body.includes("[killed by signal"))) {
    return "exec_failed";
  }

  // 4. General substring checks (unanchored text).
  const has = (...needles: string[]): boolean => needles.some((n) => body.includes(n));
  if (has("expected_hash mismatch", "hash failed")) return "stale_hash";
  if (has("expected_range_checksum mismatch", "checksum failed")) return "stale_range";
  if (has("changed after it was read")) return "stale_write";
  if (has("is not unique")) return "not_unique";
  if (has("overlap", "cannot mix move and range")) return "overlap";
  if (
    has(
      "not found",
      "unknown job",
      "unknown archive",
      "no such file",
      "no such directory",
      "does not exist",
      "not a directory",
      "enoent",
    )
  ) {
    return "not_found";
  }
  if (
    has(
      "is required",
      "must be a",
      "must provide",
      "must supply",
      "invalid",
      "malformed",
      "missing",
      "out of range",
      "not a valid",
      "cannot be empty",
      "must not be empty",
      "expected",
      "file_paths",
      "usage:",
      "must be hex",
      "must be integers",
      "array is empty",
      "strict proof",
    )
  ) {
    return "invalid_args";
  }
  if (
    has(
      "not valid utf-8",
      "is a directory",
      "permission",
      "denied",
      "eacces",
      "eperm",
      "too large",
      "binary",
      "unsupported fetched",
    )
  ) {
    return "unreadable";
  }
  if (isNet && has("request failed", "http ", "status ", "cloudflare", "timed out", "network", "fetch")) {
    return "upstream";
  }
  if (has("exit:", "timed out", "killed by signal", "non-zero", "command failed", "exited(")) {
    return "exec_failed";
  }
  if (has("already exists", "ambiguous", "multiple matches")) return "conflict";
  if (has("request failed", "cloudflare", "response too large", "network error")) return "upstream";
  return "unknown";
}

const THROWN_KINDS = new Set([
  "Error",
  "TypeError",
  "RangeError",
  "SyntaxError",
  "ReferenceError",
  "CloudflareChallengeError",
]);

export function normalizeThrownKind(name: unknown): string {
  return typeof name === "string" && THROWN_KINDS.has(name) ? name : "Error";
}

// Classify a handler throw. Transient only: name/message are never stored,
// only the returned finite code (plus the allowlisted kind separately).
export function classifyThrownReasonCode(tool: string, name: string, message: string): ToolErrorReasonCode {
  void tool;
  const t = `${name} ${message}`.toLowerCase();
  if (t.includes("cloudflare")) return "upstream";
  if (t.includes("unknown tool")) return "internal";
  if (t.includes("changed after it was read")) return "stale_write";
  if (
    t.includes("cannot read propert") ||
    t.includes("cannot read property") ||
    t.includes("undefined") ||
    t.includes("not a function") ||
    t.includes("missing") ||
    t.includes("required") ||
    t.includes("invalid") ||
    t.includes("malformed") ||
    t.includes("out of range") ||
    t.includes("expected")
  ) {
    // Covers the missing-args crash shape (TypeError on undefined).
    return "invalid_args";
  }
  return "internal";
}

function jsType(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  return typeof value;
}

// Summarize call arguments against the tool's own inputSchema property
// names (the allowlist). Unknown key strings are counted, never stored.
export function summarizeArgs(
  args: Record<string, unknown> | undefined,
  schemaProps: string[],
): { keys: string[]; types: Record<string, string>; count: number; unknownCount: number } {
  const out = { keys: [] as string[], types: {} as Record<string, string>, count: 0, unknownCount: 0 };
  if (!args || typeof args !== "object") return out;
  const allow = new Set(schemaProps);
  const names = Object.keys(args);
  out.count = names.filter((k) => !k.startsWith("__")).length;
  for (const key of names) {
    if (key.startsWith("__")) continue; // dispatch-injected internals
    if (!allow.has(key)) {
      out.unknownCount += 1;
      continue;
    }
    out.keys.push(key);
    try {
      out.types[key] = jsType(args[key]);
    } catch {
      out.types[key] = "unknown";
    }
  }
  out.keys.sort();
  return out;
}

// Coarse operation shape for tools with meaningful sub-modes. Values only,
// never argument contents.
export function deriveOperation(tool: string, args: Record<string, unknown> | undefined): string | null {
  try {
    if (!args || typeof args !== "object") return null;
    if (tool === "cave__edit") {
      if (Array.isArray(args.edits)) return "batch";
      if (args.delete === true) return "delete";
      if (args.insert_before !== undefined && args.insert_before !== null) return "move";
      if (args.start_line !== undefined || args.end_line !== undefined) return "range";
      if (typeof args.old_string === "string") return "string";
      return "unknown";
    }
    if (tool === "cave__read") {
      return args.offset !== undefined || args.limit !== undefined ? "paged" : "full";
    }
    return null;
  } catch {
    return null;
  }
}

function sha16(value: string): string {
  return createHash("sha256").update(value, "utf8").digest("hex").slice(0, SESSION_HASH_LEN);
}

export function sessionHashFor(sessionId: string): string {
  try {
    return sha16(String(sessionId ?? ""));
  } catch {
    return "unknown";
  }
}

// Correlation hash over path-valued arguments only (file_path, path,
// file_paths[], edits[].file_path). Values are hashed, never stored.
export function fileHashFor(args: Record<string, unknown> | undefined): string | null {
  try {
    if (!args || typeof args !== "object") return null;
    const paths: string[] = [];
    for (const key of ["file_path", "path"]) {
      const v = args[key];
      if (typeof v === "string" && v !== "") paths.push(v);
    }
    const multi = args.file_paths;
    if (Array.isArray(multi)) {
      for (const p of multi) if (typeof p === "string" && p !== "") paths.push(p);
    }
    const edits = args.edits;
    if (Array.isArray(edits)) {
      for (const item of edits) {
        if (item !== null && typeof item === "object") {
          const fp = (item as Record<string, unknown>).file_path;
          if (typeof fp === "string" && fp !== "") paths.push(fp);
        }
      }
    }
    if (paths.length === 0) return null;
    paths.sort();
    return createHash("sha256").update(paths.join("\n"), "utf8").digest("hex").slice(0, FILE_HASH_LEN);
  } catch {
    return null;
  }
}

// Coarse agent hint only: "subagent" when the supported env signal fires,
// otherwise "unknown". Never a model/agent identity (unavailable).
function agentHint(): string {
  try {
    return isSubagentContext() ? "subagent" : "unknown";
  } catch {
    return "unknown";
  }
}

// --- Bounded best-effort SQLite sink ---

interface QueuedError extends ToolErrorRecord {
  storeDir: string;
}

// Thrown (internally) when the store is only locked, not broken: the flush
// must defer without counting a failure.
class StoreBusy extends Error {}

function isBusyError(error: unknown): boolean {
  const code = (error as { code?: unknown })?.code;
  return code === "SQLITE_BUSY" || code === "SQLITE_LOCKED";
}

let queue: QueuedError[] = [];
let flushScheduled = false;
let consecutiveFailures = 0;
let storeDead = false;

// One cached connection per data dir (stamped at enqueue, so queued events
// never cross-contaminate when the env changes). Small map with FIFO
// eviction; production uses a single entry.
const connections = new Map<string, Database.Database>();

function closeConnections(): void {
  for (const db of connections.values()) {
    try {
      db.close();
    } catch {
      // Best-effort close.
    }
  }
  connections.clear();
}

// Tighten without following symlinks. Returns false when the path is
// missing, a symlink, or the wrong kind — never chmods a symlink target.
function tightenIfOwned(target: string, expectDir: boolean, mode: number): boolean {
  if (process.platform === "win32") return true;
  try {
    const st = lstatSync(target);
    if (st.isSymbolicLink()) return false;
    if (expectDir && !st.isDirectory()) return false;
    if (!expectDir && !st.isFile()) return false;
    if ((st.mode & 0o777 & ~mode) !== 0) chmodSync(target, mode);
    return true;
  } catch {
    return false;
  }
}

function ensureLoggingConn(dir: string): Database.Database | null {
  const cached = connections.get(dir);
  if (cached) return cached;
  const path = join(dir, "cave-tools.db");
  try {
    mkdirSync(dir, { recursive: true, mode: 0o700 });
  } catch {
    return null;
  }
  if (!tightenIfOwned(dir, true, 0o700)) return null;
  if (process.platform !== "win32" && !existsSync(path)) {
    // O_NOFOLLOW: a symlink at the db path (dangling or live) refuses with
    // ELOOP instead of creating/following the target. O_CREAT|O_WRONLY
    // with explicit 0600 for owner-only creation under any umask. The fd
    // is verified regular before use (TOCTOU best-effort).
    const nofollow = fsConstants.O_NOFOLLOW ?? 0;
    let fd: number | null = null;
    try {
      fd = openSync(path, fsConstants.O_CREAT | fsConstants.O_WRONLY | nofollow, 0o600);
      if (!fstatSync(fd).isFile()) {
        closeSync(fd);
        return null;
      }
      closeSync(fd);
    } catch {
      if (fd !== null) {
        try {
          closeSync(fd);
        } catch {
          // ignore
        }
      }
      return null;
    }
  }
  if (!tightenIfOwned(path, false, 0o600)) return null;
  for (const sidecar of [`${path}-wal`, `${path}-shm`]) {
    if (existsSync(sidecar) && !tightenIfOwned(sidecar, false, 0o600)) return null;
  }
  let db: Database.Database;
  try {
    // timeout: 0 — zero lock wait from the start, before any lock-taking
    // pragma (journal_mode itself can block under the default timeout).
    db = new Database(path, { timeout: 0 });
  } catch {
    return null;
  }
  try {
    db.pragma("journal_mode = WAL");
    db.pragma("synchronous = NORMAL");
    db.pragma("busy_timeout = 0");
    db.exec(`
      CREATE TABLE IF NOT EXISTS tool_errors (
        id TEXT PRIMARY KEY,
        created_at INTEGER NOT NULL,
        tool TEXT NOT NULL,
        outcome TEXT NOT NULL,
        reason_code TEXT NOT NULL,
        elapsed_ms INTEGER NOT NULL,
        mode TEXT NOT NULL DEFAULT 'unknown',
        operation TEXT,
        batch_index INTEGER,
        agent TEXT NOT NULL DEFAULT 'unknown',
        session_hash TEXT NOT NULL,
        file_hash TEXT,
        arg_keys TEXT NOT NULL DEFAULT '[]',
        arg_types TEXT NOT NULL DEFAULT '{}',
        arg_count INTEGER NOT NULL DEFAULT 0,
        unknown_arg_count INTEGER NOT NULL DEFAULT 0,
        error_kind TEXT
      );
      CREATE INDEX IF NOT EXISTS idx_tool_errors_created_at ON tool_errors(created_at);
      CREATE INDEX IF NOT EXISTS idx_tool_errors_tool ON tool_errors(tool);
    `);
  } catch (error) {
    try {
      db.close();
    } catch {
      // ignore
    }
    if (isBusyError(error)) throw new StoreBusy();
    return null;
  }
  if (connections.size >= 4) {
    const oldest = connections.keys().next();
    if (!oldest.done) {
      try {
        connections.get(oldest.value)?.close();
      } catch {
        // ignore
      }
      connections.delete(oldest.value);
    }
  }
  connections.set(dir, db);
  for (const sidecar of [`${path}-wal`, `${path}-shm`]) {
    if (existsSync(sidecar)) tightenIfOwned(sidecar, false, 0o600);
  }
  return db;
}

// Enforce retention: 7-day window + 5000-row cap. Best-effort; throws stay
// inside the flush guard.
export function pruneToolErrors(conn: Database.Database, now = Date.now()): void {
  conn
    .prepare("DELETE FROM tool_errors WHERE created_at < ?")
    .run(now - TOOL_ERROR_RETENTION_DAYS * 24 * 60 * 60 * 1000);
  conn.prepare(
    `DELETE FROM tool_errors WHERE id NOT IN (
        SELECT id FROM tool_errors ORDER BY created_at DESC, rowid DESC LIMIT ${TOOL_ERROR_MAX_ROWS}
      )`,
  ).run();
}

// One bounded batch (<=50 rows) from the leading same-dir run.
// Returns true when draining may continue, false when blocked by a lock
// (queue left intact for the next tool event). Never throws.
function flushOneBatch(now = Date.now()): boolean {
  if (queue.length === 0) return true;
  if (!isToolErrorLoggingEnabled() || storeDead) {
    queue = [];
    return true;
  }
  try {
    const dir = queue[0].storeDir;
    const batch: QueuedError[] = [];
    while (batch.length < TOOL_ERROR_FLUSH_BATCH && queue.length > 0 && queue[0].storeDir === dir) {
      batch.push(queue.shift() as QueuedError);
    }
    let conn: Database.Database | null = null;
    try {
      conn = ensureLoggingConn(dir);
    } catch (error) {
      if (!(error instanceof StoreBusy)) throw error;
      // Locked: put the batch back, keep the queue for the next event.
      queue.unshift(...batch);
      return false;
    }
    if (!conn) {
      queue.unshift(...batch);
      consecutiveFailures += 1;
      if (consecutiveFailures >= TOOL_ERROR_MAX_FLUSH_FAILURES) {
        storeDead = true;
        queue = [];
        closeConnections();
      }
      return true;
    }
    const insert = conn.prepare(`
      INSERT OR IGNORE INTO tool_errors (
        id, created_at, tool, outcome, reason_code, elapsed_ms,
        mode, operation, batch_index, agent, session_hash, file_hash,
        arg_keys, arg_types, arg_count, unknown_arg_count, error_kind
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `);
    try {
      const tx = conn.transaction((rows: QueuedError[]) => {
        for (const r of rows) {
          insert.run(
            r.id,
            r.createdAt,
            r.tool,
            r.outcome,
            r.reasonCode,
            r.elapsedMs,
            r.mode,
            r.operation,
            r.batchIndex,
            r.agent,
            r.sessionHash,
            r.fileHash,
            JSON.stringify(r.argKeys),
            JSON.stringify(r.argTypes),
            r.argCount,
            r.unknownArgCount,
            r.errorKind,
          );
        }
      });
      tx(batch);
    } catch (error) {
      if (isBusyError(error)) {
        queue.unshift(...batch);
        return false;
      }
      throw error;
    }
    consecutiveFailures = 0;
    pruneToolErrors(conn, now);
    return true;
  } catch {
    consecutiveFailures += 1;
    if (consecutiveFailures >= TOOL_ERROR_MAX_FLUSH_FAILURES) {
      storeDead = true;
      queue = [];
      closeConnections();
    }
    return true;
  }
}

function scheduleFlush(): void {
  if (flushScheduled) return;
  flushScheduled = true;
  // setImmediate, not a microtask: the sink runs on a later event-loop
  // turn, after the already-resolved tool result has been delivered. One
  // bounded batch per turn; a locked store stops here (no retry spin) and
  // the next tool event schedules again.
  setImmediate(() => {
    flushScheduled = false;
    // No retry spin: continue only when the batch made progress.
    if (!flushOneBatch()) return;
    if (queue.length > 0 && !storeDead && isToolErrorLoggingEnabled()) scheduleFlush();
  });
}

/**
 * Synchronously drain the queue (bounded: stops after maxBatches batches or
 * when a lock blocks progress). Explicit flush point for the CLI
 * controlled-exit funnel and for tests. Never throws.
 */
export function flushToolErrorQueue(maxBatches = 8): void {
  try {
    for (let i = 0; i < maxBatches && queue.length > 0; i++) {
      if (storeDead || !isToolErrorLoggingEnabled()) {
        queue = [];
        return;
      }
      if (!flushOneBatch()) return;
    }
  } catch {
    // Best-effort only.
  }
}

export interface ObserveToolCallOptions {
  tool: string;
  args: Record<string, unknown> | undefined;
  schemaProps: string[];
  sessionId: string;
  startedAt: number;
  env?: NodeJS.ProcessEnv;
  batchIndex?: number;
  result?: unknown;
  thrown?: unknown;
}

// Shared dispatch hook. Records soft (returned isError) and thrown
// outcomes; successes are ignored. A throw is detected by the presence of
// the `thrown` property (not by value), so thrown null/undefined values
// are recorded without fabricating messages. Never throws, never mutates
// result/error.
export function observeToolCall(opts: ObserveToolCallOptions): void {
  try {
    if (!isToolErrorLoggingEnabled(opts.env)) return;
    // Presence, not value: `throw null` / `throw undefined` are real
    // outcomes and must be recorded; the original value is never touched.
    const hasThrown = Object.hasOwn(opts, "thrown");
    const thrown = hasThrown;
    const isSoft =
      !hasThrown &&
      opts.result !== null &&
      typeof opts.result === "object" &&
      (opts.result as { isError?: unknown }).isError === true;
    if (!thrown && !isSoft) return;

    const now = Date.now();
    const summary = summarizeArgs(opts.args, opts.schemaProps);
    let reasonCode: ToolErrorReasonCode;
    let errorKind: string | null = null;
    let batchIndex: number | null = null;
    if (thrown) {
      const err = opts.thrown as { name?: unknown; message?: unknown };
      const name = typeof err?.name === "string" ? err.name : "Error";
      const message = typeof err?.message === "string" ? err.message : String(opts.thrown);
      errorKind = normalizeThrownKind(name);
      reasonCode = classifyThrownReasonCode(opts.tool, name, message);
    } else {
      const text = firstText(opts.result);
      reasonCode = classifySoftReasonCode(opts.tool, text);
      batchIndex = parseBatchIndex(text);
    }
    if (typeof opts.batchIndex === "number" && Number.isFinite(opts.batchIndex)) {
      batchIndex = Math.trunc(opts.batchIndex);
    }

    let storeDir: string;
    try {
      storeDir = caveToolsDataDir();
    } catch {
      return;
    }
    const record: QueuedError = {
      id: randomUUID(),
      createdAt: now,
      tool: sanitizeToolName(opts.tool),
      outcome: thrown ? "thrown" : "soft",
      reasonCode,
      elapsedMs: Math.max(0, now - opts.startedAt),
      mode: sanitizeMode(opts.env?.CAVE_TOOLS_MODE ?? process.env.CAVE_TOOLS_MODE),
      operation: deriveOperation(opts.tool, opts.args),
      batchIndex,
      agent: agentHint(),
      sessionHash: sessionHashFor(opts.sessionId),
      fileHash: fileHashFor(opts.args),
      argKeys: summary.keys,
      argTypes: summary.types,
      argCount: summary.count,
      unknownArgCount: summary.unknownCount,
      errorKind,
      storeDir,
    };
    if (queue.length >= TOOL_ERROR_QUEUE_CAP) queue.splice(0, queue.length - TOOL_ERROR_QUEUE_CAP + 1);
    queue.push(record);
    scheduleFlush();
  } catch {
    // Logging must never break a tool call.
  }
}

// --- Local read access (CLI) ---

function openReadDb(): Database.Database | null {
  try {
    const path = join(caveToolsDataDir(), "cave-tools.db");
    if (!existsSync(path)) return null;
    try {
      const st = lstatSync(path);
      if (st.isSymbolicLink() || !st.isFile()) return null;
    } catch {
      return null;
    }
    const db = new Database(path, { readonly: true, timeout: 0 });
    let table: unknown;
    try {
      table = db.prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'tool_errors'").get();
    } catch {
      try {
        db.close();
      } catch {
        // ignore
      }
      return null;
    }
    if (!table) {
      db.close();
      return null;
    }
    return db;
  } catch {
    return null;
  }
}

interface ToolErrorRow {
  id: string;
  created_at: number;
  tool: string;
  outcome: string;
  reason_code: string;
  elapsed_ms: number;
  mode: string;
  operation: string | null;
  batch_index: number | null;
  agent: string;
  session_hash: string;
  file_hash: string | null;
  arg_keys: string;
  arg_types: string;
  arg_count: number;
  unknown_arg_count: number;
  error_kind: string | null;
}

function rowToRecord(row: ToolErrorRow): ToolErrorRecord {
  let argKeys: string[] = [];
  let argTypes: Record<string, string> = {};
  try {
    const parsed: unknown = JSON.parse(row.arg_keys);
    if (Array.isArray(parsed)) argKeys = parsed.filter((k): k is string => typeof k === "string");
  } catch {
    // keep default
  }
  try {
    const parsed: unknown = JSON.parse(row.arg_types);
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
      argTypes = parsed as Record<string, string>;
    }
  } catch {
    // keep default
  }
  return {
    id: row.id,
    createdAt: row.created_at,
    tool: row.tool,
    outcome: row.outcome === "thrown" ? "thrown" : "soft",
    reasonCode: REASON_CODES.has(row.reason_code)
      ? (row.reason_code as ToolErrorReasonCode)
      : "unknown",
    elapsedMs: row.elapsed_ms,
    mode: row.mode,
    operation: row.operation,
    batchIndex: row.batch_index,
    agent: row.agent,
    sessionHash: row.session_hash,
    fileHash: row.file_hash,
    argKeys,
    argTypes,
    argCount: row.arg_count,
    unknownArgCount: row.unknown_arg_count,
    errorKind: row.error_kind,
  };
}

export function listToolErrors(limit = 20): ToolErrorRecord[] {
  const db = openReadDb();
  if (!db) return [];
  try {
    const n = Math.min(500, Math.max(1, Math.floor(limit) || 20));
    const rows = db
      .prepare("SELECT * FROM tool_errors ORDER BY created_at DESC, rowid DESC LIMIT ?")
      .all(n) as ToolErrorRow[];
    return rows.map(rowToRecord);
  } catch {
    return [];
  } finally {
    try {
      db.close();
    } catch {
      // ignore
    }
  }
}

export interface ToolErrorCounts {
  total: number;
  byCode: Record<string, number>;
  byTool: Record<string, number>;
}

export function countToolErrors(): ToolErrorCounts {
  const out: ToolErrorCounts = { total: 0, byCode: {}, byTool: {} };
  const db = openReadDb();
  if (!db) return out;
  try {
    const total = db.prepare("SELECT COUNT(*) AS n FROM tool_errors").get() as { n: number };
    out.total = total.n;
    for (const row of db
      .prepare("SELECT reason_code AS k, COUNT(*) AS n FROM tool_errors GROUP BY reason_code")
      .all() as Array<{ k: string; n: number }>) {
      out.byCode[row.k] = row.n;
    }
    for (const row of db
      .prepare("SELECT tool AS k, COUNT(*) AS n FROM tool_errors GROUP BY tool")
      .all() as Array<{ k: string; n: number }>) {
      out.byTool[row.k] = row.n;
    }
  } catch {
    // empty result on unreadable store
  } finally {
    try {
      db.close();
    } catch {
      // ignore
    }
  }
  return out;
}

export function toolErrorsDbPath(): string {
  return join(caveToolsDataDir(), "cave-tools.db");
}

/** Test-only hook: drop cached connections/queue/failure state. */
export function resetToolErrorStateForTests(): void {
  closeConnections();
  queue = [];
  flushScheduled = false;
  consecutiveFailures = 0;
  storeDead = false;
}

export function toolErrorQueueDepthForTests(): number {
  return queue.length;
}
