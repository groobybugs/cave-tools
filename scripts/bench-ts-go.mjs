#!/usr/bin/env node
/**
 * scripts/bench-ts-go.mjs — standalone TS vs Go benchmark harness for cave-tools.
 *
 * Measures the same workloads against an absolute TypeScript CLI entry (dist/
 * of a given checkout) and the Go CLI binary, with identical fixtures, env,
 * cwd and session policies. Zero dependencies; node built-ins only.
 *
 * Routes:
 *   cli_help_cold      `help`                      — cold process startup + tool help load
 *   cli_filter_medium  `filter --json` (32 KiB)    — cold CLI compression pipeline
 *   cli_filter_large   `filter --json` (~450 KiB)  — same, large payload
 *   cli_read_cold      `read <fixture>`            — cold process, first/full read (in-process cache empty)
 *   mcp_read_first     tools/call cave__read       — warm persistent MCP session, first read (full)
 *   mcp_read_dedup     tools/call cave__read       — same session + file again (dedup stub)
 *   mcp_compress       tools/call cave__compress   — warm persistent MCP session
 *   daemon_cli_read    `read <fix> --session s`    — OPTIONAL (--daemon): CLI served by its own
 *                                                    daemon (auto-started, untimed warmups). TS
 *                                                    keeps Node startup in every sample. Deferred
 *                                                    by default; see bench-ts-go.md for the smoke
 *                                                    divergence this route surfaced.
 *
 * Validation runs every route once per side BEFORE timing and compares stdout,
 * stderr and exit codes byte-for-byte (no normalization). Mismatched routes are
 * flagged, their timing is still collected but kept out of the matched summary.
 *
 * See scripts/bench-ts-go.md for the full methodology and limitations.
 *
 * Usage:
 *   node scripts/bench-ts-go.mjs --ts-cli /abs/path/dist/cli.js [options]
 *
 * Options:
 *   --ts-cli <path>     absolute path to the TS dist/cli.js (required)
 *   --go-src <dir>      go module dir to build the Go binary from (default: <repo>/go)
 *   --go-bin <path>     absolute path to an already-built cave-tools-go binary
 *                       (overrides --go-src; otherwise built into <work>/bin/cave-tools-go, offline)
 *   --repeats <n>       timed samples per side per route (default 25)
 *   --seed <n>          PRNG seed for TS/Go order randomization (default 42)
 *   --work-dir <path>   scratch root (default /tmp/opencode/w5-bench)
 *   --out <path>        raw JSON results (default <work>/<run>/results.json)
 *   --routes <csv>      subset of route ids
 *   --daemon            ALSO run the optional daemon_cli_read route (off by default)
 *   --memory            ALSO run the optional memory phase (off by default)
 *
 * Provenance (recorded + checked; mismatches are reported, never accepted silently):
 *   --expect-ts-commit <sha>          expected TS repo commit
 *   --expect-ts-dist-sha256 <hex>     expected TS dist-tree manifest hash (frozen dist pin)
 *   --expect-go-commit <sha>          expected Go source commit (go-src repo HEAD, or the
 *                                     prebuilt binary's own embedded vcs.revision)
 *   --expect-go-binary-sha256 <hex>   expected sha256 of the measured Go binary
 *   --go-bin-ref <label>              asserted provenance label for a prebuilt --go-bin
 *                                     (e.g. "5f28d88 build job <id>"); never inferred from HEAD
 */

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// ---------------------------------------------------------------- arguments

function parseArgs(argv) {
  const cfg = {
    tsCli: null,
    goSrc: path.join(REPO_ROOT, "go"),
    goBin: null,
    repeats: 25,
    seed: 42,
    workDir: "/tmp/opencode/w5-bench",
    out: null,
    routes: null,
    daemon: false,
    memory: false,
    provenance: { expectTsCommit: null, expectGoCommit: null, expectGoBinarySha256: null, expectTsDistSha256: null, goBinRef: null },
  };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const next = () => argv[++i];
    switch (flag) {
      case "--ts-cli": cfg.tsCli = next(); break;
      case "--go-src": cfg.goSrc = next(); break;
      case "--go-bin": cfg.goBin = next(); break;
      case "--repeats": cfg.repeats = Number(next()); break;
      case "--seed": cfg.seed = Number(next()); break;
      case "--work-dir": cfg.workDir = next(); break;
      case "--out": cfg.out = next(); break;
      case "--routes": cfg.routes = new Set(next().split(",")); break;
      case "--daemon": cfg.daemon = true; break;
      case "--memory": cfg.memory = true; break;
      case "--expect-ts-commit": cfg.provenance.expectTsCommit = next(); break;
      case "--expect-go-commit": cfg.provenance.expectGoCommit = next(); break;
      case "--expect-go-binary-sha256": cfg.provenance.expectGoBinarySha256 = next(); break;
      case "--expect-ts-dist-sha256": cfg.provenance.expectTsDistSha256 = next(); break;
      case "--go-bin-ref": cfg.provenance.goBinRef = next(); break;
      default:
        console.error(`bench-ts-go: unknown argument "${flag}"`);
        process.exit(2);
    }
  }
  if (!cfg.tsCli) {
    console.error("bench-ts-go: --ts-cli <abs path to dist/cli.js> is required");
    process.exit(2);
  }
  if (!path.isAbsolute(cfg.tsCli)) {
    console.error("bench-ts-go: --ts-cli must be an absolute path");
    process.exit(2);
  }
  if (!Number.isFinite(cfg.repeats) || cfg.repeats < 1) {
    console.error("bench-ts-go: --repeats must be a positive number");
    process.exit(2);
  }
  return cfg;
}

// --------------------------------------------------------------- utilities

// Best-effort synchronous cleanup so a failed run never leaks MCP children or
// sandbox daemons. Tasks guard themselves against already-cleaned resources.
const cleanupTasks = [];
function runCleanup() {
  for (const fn of cleanupTasks.reverse()) {
    try { fn(); } catch { /* best effort */ }
  }
  cleanupTasks.length = 0;
}

const sha256 = (buf) => createHash("sha256").update(buf).digest("hex");

function firstDiff(a, b) {
  const n = Math.min(a.length, b.length);
  let offset = 0;
  while (offset < n && a[offset] === b[offset]) offset++;
  const ctx = (buf) => JSON.stringify(buf.subarray(Math.max(0, offset - 20), offset + 40).toString("utf8"));
  return {
    offset,
    ts_bytes: a.length,
    go_bytes: b.length,
    ts_context: ctx(a),
    go_context: ctx(b),
  };
}

function gitCommit(dir) {
  const res = spawnSync("git", ["-C", dir, "rev-parse", "HEAD"], { encoding: "utf8" });
  return res.status === 0 ? res.stdout.trim() : null;
}

function readPackageVersion(root) {
  try {
    return JSON.parse(readFileSync(path.join(root, "package.json"), "utf8")).version ?? null;
  } catch {
    return null;
  }
}

// mulberry32: tiny deterministic PRNG for order randomization.
function mulberry32(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// Nearest-rank percentile over an ascending-sorted array.
function percentile(sortedAsc, p) {
  if (sortedAsc.length === 0) return null;
  return sortedAsc[Math.max(0, Math.ceil(p * sortedAsc.length) - 1)];
}

function stats(samples) {
  const valid = samples.filter((s) => s.valid).map((s) => s.ms);
  const sorted = [...valid].sort((x, y) => x - y);
  const mean = valid.length ? valid.reduce((a, b) => a + b, 0) / valid.length : null;
  return {
    n_valid: valid.length,
    n_invalid: samples.length - valid.length,
    min_ms: sorted[0] ?? null,
    p50_ms: percentile(sorted, 0.5),
    p95_ms: percentile(sorted, 0.95),
    max_ms: sorted[sorted.length - 1] ?? null,
    mean_ms: mean,
  };
}

// ----------------------------------------------------------------- fixtures
// Deterministic, index-driven content. Same bytes for both sides and for every
// run of the harness; hashes are recorded in the results.

function makeRng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1103515245) + 12345) >>> 0;
    return s / 4294967296;
  };
}

const WORDS = ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel",
  "india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa"];

function genLine(rng, i) {
  const word = () => WORDS[Math.floor(rng() * WORDS.length)];
  const kind = i % 7;
  if (kind === 3) {
    // ANSI-colored status line (exercises stripAnsi).
    return `\x1b[32mOK\x1b[0m worker=${word()}-${i % 97} shard=${i % 13} state=${word()} elapsed=${(i * 7) % 900}ms`;
  }
  if (kind === 5) {
    // Structured-ish JSON line (exercises extractStructuredData).
    return `{"id":${i},"name":"evt-${word()}","status":"${i % 3 === 0 ? "ok" : "retry"}","size":${(i * 37) % 9000}}`;
  }
  return `2026-02-${String((i % 28) + 1).padStart(2, "0")}T${String(i % 24).padStart(2, "0")}:${String(i % 60).padStart(2, "0")}:${String((i * 7) % 60).padStart(2, "0")}Z GET /api/v1/${word()}/${word()}-${i} status=${[200, 201, 304, 404, 500][i % 5]} bytes=${(i * 131) % 50000} dur=${(i * 13) % 800}ms`;
}

function genLines(count, seed) {
  const rng = makeRng(seed);
  const out = [];
  for (let i = 0; i < count; i++) {
    if (i > 0 && i % 37 === 0) out.push("", ""); // blank runs
    if (i > 0 && i % 53 === 0) { // identical-line run (exercises run-length collapse)
      const repeat = `2026-02-01T00:00:00Z healthcheck ok node=${i % 11}`;
      for (let k = 0; k < 6; k++) out.push(repeat);
    }
    out.push(genLine(rng, i));
  }
  return out.join("\n") + "\n";
}

function buildFixtures(dir) {
  mkdirSync(dir, { recursive: true });
  const fixtures = {
    "read-fixture.txt": genLines(1500, 1),
    "warmup-fixture.txt": genLines(10, 2),
    "compress-fixture.txt": genLines(400, 3),
    "filter-medium.json": JSON.stringify({
      command: "make build",
      stdout: genLines(600, 4),
      stderr: 'warn: cache miss for key "alpha"\nerror: token=ghp_abcdefghijklmnopqrstuvwxyz0123456789 rejected\nwarn: retry scheduled\n',
      exitCode: 0,
    }),
    "filter-large.json": JSON.stringify({
      command: "make build",
      stdout: genLines(8000, 5),
      stderr: 'warn: cache miss for key "alpha"\nerror: token=ghp_abcdefghijklmnopqrstuvwxyz0123456789 rejected\nwarn: retry scheduled\n',
      exitCode: 0,
    }),
  };
  const meta = {};
  for (const [name, content] of Object.entries(fixtures)) {
    const buf = Buffer.from(content, "utf8");
    writeFileSync(path.join(dir, name), buf);
    meta[name] = { bytes: buf.length, sha256: sha256(buf) };
  }
  return meta;
}

// ------------------------------------------------------------- route table

const SCOPE_CLI =
  "fresh process per sample; includes runtime startup (TS: Node.js startup remains in every sample). " +
  "Process-cold only — OS page cache is NOT reset; this is not a cold-filesystem measurement.";
const SCOPE_MCP =
  "warm persistent MCP server per side (initialize + one untimed warmup read of a separate file); " +
  "timed value is the tools/call roundtrip only (write -> complete JSON-RPC response line).";
const SCOPE_DAEMON =
  "daemon auto-started during untimed warmups and kept warm; per-sample fresh CLI process served " +
  "through the daemon — TS keeps Node process startup in every sample; only tool work is daemon-served.";

function buildRoutes(cfg, fx) {
  const readFix = path.join(fx, "read-fixture.txt");
  const warmupFix = path.join(fx, "warmup-fixture.txt");
  const routes = [
    {
      id: "cli_help_cold", kind: "cli", scope: SCOPE_CLI,
      argv: () => ["help"], stdin: null,
      note: "startup route: process spawn + lazy module/tool load + help render",
    },
    {
      id: "cli_filter_medium", kind: "cli", scope: SCOPE_CLI,
      argv: () => ["filter", "--json"], stdin: "filter-medium.json",
      note: "cave__bash post-run compression pipeline on deterministic ~32 KiB output",
    },
    {
      id: "cli_filter_large", kind: "cli", scope: SCOPE_CLI,
      argv: () => ["filter", "--json"], stdin: "filter-large.json",
      note: "same pipeline on deterministic ~450 KiB output (archiving path included)",
    },
    {
      id: "cli_read_cold", kind: "cli", scope: SCOPE_CLI,
      argv: () => ["read", readFix], stdin: null,
      note: "first/full read: in-memory dedup cache is empty in every fresh process",
    },
    {
      id: "mcp_read_first", kind: "mcp", scope: SCOPE_MCP,
      call: "read_first",
      note: "first cave__read of the fixture in a fresh per-repeat session id (dedup miss, full output)",
    },
    {
      id: "mcp_read_dedup", kind: "mcp", scope: SCOPE_MCP,
      call: "read_dedup",
      note: "immediate second cave__read, same session id + unchanged file (dedup hit, stub output)",
    },
    {
      id: "mcp_compress", kind: "mcp", scope: SCOPE_MCP,
      call: "compress",
      note: "cave__compress on deterministic ~25 KiB text (structured extraction + budget)",
    },
    {
      id: "daemon_cli_read", kind: "daemon", scope: SCOPE_DAEMON,
      argv: (r) => ["read", readFix, "--session", `bench-d-${r}`], stdin: null,
      note: "CAVE_TOOLS_DAEMON=1, fresh --session per sample so every sample is a dedup miss",
      daemonEnv: true,
    },
  ];
  return routes.filter((route) => {
    if (cfg.routes && !cfg.routes.has(route.id)) return false;
    if (route.kind === "daemon" && !cfg.daemon) return false;
    return true;
  });
}

// ------------------------------------------------------------------- impls

function buildGoBinary(cfg, runDir) {
  const out = path.join(runDir, "bin", "cave-tools-go");
  mkdirSync(path.dirname(out), { recursive: true });
  const goSrc = path.resolve(cfg.goSrc);
  const version = readPackageVersion(path.resolve(goSrc, "..")) ?? "0.6.1";
  const res = spawnSync("go",
    ["build", "-ldflags", `-X main.version=${version}`, "-o", out, "./cmd/cave-tools-go"],
    {
      cwd: goSrc,
      encoding: "utf8",
      timeout: 600_000,
      env: { ...process.env, GOPROXY: "off", GOSUMDB: "off" },
    });
  if (res.status !== 0) {
    console.error(`bench-ts-go: go build failed:\n${res.stderr || res.stdout}`);
    process.exit(1);
  }
  return out;
}

// ----------------------------------------------------------------- runners

function runCliRaw(impl, argv, stdinBuf, envOverride) {
  const env = { ...impl.env, ...(envOverride ?? {}) };
  const t0 = performance.now();
  const res = spawnSync(impl.cmd, [...impl.prefix, ...argv], {
    input: stdinBuf ?? Buffer.alloc(0),
    env,
    cwd: impl.cwd,
    encoding: "buffer",
    timeout: 300_000,
    maxBuffer: 64 * 1024 * 1024,
  });
  const ms = performance.now() - t0;
  return {
    ms,
    code: res.status ?? -1,
    signal: res.signal ?? null,
    stdout: res.stdout ?? Buffer.alloc(0),
    stderr: res.stderr ?? Buffer.alloc(0),
  };
}

class McpClient {
  constructor(impl, envOverride) {
    this.child = spawn(impl.cmd, [...impl.prefix, "mcp"], {
      env: { ...impl.env, ...(envOverride ?? {}) },
      cwd: impl.cwd,
      stdio: ["pipe", "pipe", "pipe"],
    });
    this.buf = Buffer.alloc(0);
    this.pending = new Map();
    this.stderrChunks = [];
    this.nextId = 10;
    this.closed = false;
    cleanupTasks.push(() => {
      if (!this.closed) {
        try { this.child.kill("SIGKILL"); } catch { /* already gone */ }
      }
    });
    this.child.stdout.on("data", (chunk) => this._onData(chunk));
    this.child.stderr.on("data", (chunk) => this.stderrChunks.push(chunk));
  }

  _onData(chunk) {
    this.buf = Buffer.concat([this.buf, chunk]);
    let idx = this.buf.indexOf(0x0a);
    while (idx >= 0) {
      const line = this.buf.subarray(0, idx);
      this.buf = this.buf.subarray(idx + 1);
      if (line.length > 0) {
        let msg = null;
        try { msg = JSON.parse(line.toString("utf8")); } catch { /* skip */ }
        if (msg && msg.id !== undefined && this.pending.has(msg.id)) {
          this.pending.get(msg.id)({ raw: Buffer.from(line), msg });
          this.pending.delete(msg.id);
        }
      }
      idx = this.buf.indexOf(0x0a);
    }
  }

  request(frame, timeoutMs = 300_000) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(frame.id);
        reject(new Error(`mcp request ${frame.id} timed out`));
      }, timeoutMs);
      this.pending.set(frame.id, (res) => { clearTimeout(timer); resolve(res); });
      this.child.stdin.write(JSON.stringify(frame) + "\n");
    });
  }

  notify(frame) {
    this.child.stdin.write(JSON.stringify(frame) + "\n");
  }

  async initialize() {
    await this.request({
      jsonrpc: "2.0", id: 1, method: "initialize",
      params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "w5-bench", version: "0" } },
    });
    this.notify({ jsonrpc: "2.0", method: "notifications/initialized" });
  }

  id() { return this.nextId++; }

  async close() {
    this.closed = true;
    try { this.child.stdin.end(); } catch { /* already closed */ }
    await new Promise((resolve) => {
      const timer = setTimeout(() => { this.child.kill("SIGKILL"); resolve(); }, 5000);
      this.child.once("exit", () => { clearTimeout(timer); resolve(); });
    });
  }
}

function readFrame(client, session) {
  return client.request({
    jsonrpc: "2.0", id: client.id(), method: "tools/call",
    params: { name: "cave__read", arguments: { file_path: client.readFixture }, _meta: { sessionID: session } },
  });
}

function compressFrame(client, session) {
  return client.request({
    jsonrpc: "2.0", id: client.id(), method: "tools/call",
    params: { name: "cave__compress", arguments: { text: client.compressText }, _meta: { sessionID: session } },
  });
}

// Untimed session warmups: one read of a separate file plus one compress, so
// the timed calls hit warm tool code paths on both sides.
async function warmupClient(client) {
  await readFrame(client, "warmup");
  await compressFrame(client, "warmup");
}

// Per-route MCP session prefixes, so no route's cache state can leak into
// another route's samples.
const MCP_SESSION_PREFIX = {
  read_first: "bench-first",
  read_dedup: "bench-dedup",
  compress: "bench-compress",
};

// One MCP route invocation in the exact shape timing uses. read_dedup performs
// an untimed paired first read in the same session so the measured call is the
// dedup-stub path.
async function mcpInvoke(client, call, session) {
  if (call === "read_first") return readFrame(client, session);
  if (call === "read_dedup") {
    await readFrame(client, session);
    return readFrame(client, session);
  }
  return compressFrame(client, session);
}

function frameText(msg) {
  const content = msg?.result?.content;
  if (Array.isArray(content) && content[0] && typeof content[0].text === "string") return content[0].text;
  return JSON.stringify(msg ?? {});
}

// Canonical JSON (deep-sorted keys) for a scoped payload comparison of the
// JSON-RPC `result` value. Diagnostic only: the gating comparison stays
// byte-strict on the raw response line.
function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (value && typeof value === "object") {
    return `{${Object.keys(value).sort().map((k) => `${JSON.stringify(k)}:${canonicalJSON(value[k])}`).join(",")}}`;
  }
  return JSON.stringify(value) ?? "null";
}

// The dedup stub embeds its session tag and read count by design
// (src/compression/utils.ts readStubFor), so its payload identity is this
// pattern — not a literal hash, which would false-positive on every sample
// the moment its session name differs. Format check, not output normalization.
const READ_STUB_RE = /^<file unchanged since read #\d+ \(session [^)]*\)\. Use force=true to re-read\.>$/;

// Per-sample payload identity for MCP responses: hash + text length of the
// ToolResult payload only (never the raw frame, whose request-id digits vary
// across samples).
function resultIdentity(msg) {
  const text = frameText(msg);
  return {
    result_sha256: sha256(Buffer.from(canonicalJSON(msg?.result ?? null), "utf8")),
    text_sha256: sha256(Buffer.from(text, "utf8")),
    text_length: text.length,
    is_stub: text.includes("file unchanged"),
    is_error: Boolean(msg?.result?.isError),
  };
}

// Env sanitization for both sides, mirroring the runtime's own
// isSubagentContext() detection (src/compression/utils.ts): CAVE_TOOLS_FORCE_FRESH
// ("1"/"true"), the known subagent keys and any *_TASK_ID/TASK_ID key force
// fresh reads and would destroy the dedup-stub route. Also drop implementation
// switch inputs (CAVE_TOOLS_IMPL, CAVE_TOOLS_GO_BIN). Nothing else is removed
// (PATH and ordinary env pass through untouched).
const KNOWN_SUBAGENT_KEYS = ["CLINE_TASK_ID", "CLAUDE_CODE_SUBAGENT", "ANTHROPIC_SUBAGENT", "CURSOR_TASK_ID"];
const TASK_ID_STYLE = /(?:^|_)TASK_ID$/;
const ENV_SWITCH_KEYS = ["CAVE_TOOLS_IMPL", "CAVE_TOOLS_GO_BIN"];

function sanitizeInheritedEnv(base) {
  const env = { ...base };
  const stripped = [];
  for (const key of Object.keys(env)) {
    if (key === "CAVE_TOOLS_FORCE_FRESH" || KNOWN_SUBAGENT_KEYS.includes(key) ||
        TASK_ID_STYLE.test(key) || ENV_SWITCH_KEYS.includes(key)) {
      stripped.push(key);
      delete env[key];
    }
  }
  return { env, stripped: stripped.sort() };
}

// Content fingerprint of a dist tree (sorted relpath + size + content sha256),
// so a frozen TS dist can be pinned with --expect-ts-dist-sha256.
function distManifestSha256(distDir) {
  const lines = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => (a.name < b.name ? -1 : 1))) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.name.endsWith(".js")) {
        const buf = readFileSync(full);
        lines.push(`${path.relative(distDir, full)}\0${buf.length}\0${sha256(buf)}`);
      }
    }
  };
  walk(distDir);
  return sha256(Buffer.from(lines.join("\n"), "utf8"));
}

// ------------------------------------------------------------- validation

function compareOutputs(ts, go) {
  const stdoutEqual = ts.stdout.equals(go.stdout);
  const stderrEqual = ts.stderr.equals(go.stderr);
  const exitEqual = ts.code === go.code;
  return {
    match: stdoutEqual && stderrEqual && exitEqual,
    exit_equal: exitEqual,
    stdout_equal: stdoutEqual,
    stderr_equal: stderrEqual,
    ts: { exit: ts.code, stdout_bytes: ts.stdout.length, stderr_bytes: ts.stderr.length, stdout_sha256: sha256(ts.stdout), stderr_sha256: sha256(ts.stderr) },
    go: { exit: go.code, stdout_bytes: go.stdout.length, stderr_bytes: go.stderr.length, stdout_sha256: sha256(go.stdout), stderr_sha256: sha256(go.stderr) },
    first_diff: stdoutEqual ? null : firstDiff(ts.stdout, go.stdout),
  };
}

// ------------------------------------------------------------------- main

async function main() {
  const cfg = parseArgs(process.argv.slice(2));
  const tsCli = path.resolve(cfg.tsCli);
  if (!existsSync(tsCli)) {
    console.error(`bench-ts-go: TS entry not found: ${tsCli}`);
    process.exit(1);
  }
  const tsRepo = path.resolve(path.dirname(tsCli), "..");
  const goSrc = path.resolve(cfg.goSrc);
  const goSrcRepo = path.resolve(goSrc, "..");
  if (!cfg.goBin && !existsSync(path.join(goSrc, "go.mod"))) {
    console.error(`bench-ts-go: --go-src ${goSrc} has no go.mod`);
    process.exit(1);
  }

  const runId = new Date().toISOString().replace(/[:.]/g, "-");
  const runDir = path.join(cfg.workDir, runId);
  const fxD = path.join(runDir, "fixtures");
  const sandbox = path.join(runDir, "sandbox");
  for (const dir of ["home", "data", "claude", "run"]) {
    mkdirSync(path.join(sandbox, dir), { recursive: true });
  }
  mkdirSync(runDir, { recursive: true });

  const fixtures = buildFixtures(fxD);
  const goBin = cfg.goBin ? path.resolve(cfg.goBin) : buildGoBinary(cfg, runDir);

  // Identical child env for both sides (parity harness "shared" policy): one
  // bench-owned sandbox, so outputs that embed data/archive paths stay
  // byte-identical. CAVE_TOOLS_GO_DIST_DIR is read only by the Go runtime
  // (daemon build fingerprint, go/internal/daemon/paths.go) and ignored by TS;
  // pointing it at this worktree's dist keeps the Go daemon key distinct from
  // the TS daemon key (which TS derives from its own dist), so --daemon runs
  // never share a socket with the TS daemon or the live user daemon.
  const { env: sanitizedBase, stripped: strippedEnvKeys } = sanitizeInheritedEnv(process.env);
  const childEnv = {
    ...sanitizedBase,
    HOME: path.join(sandbox, "home"),
    CAVE_TOOLS_DATA_DIR: path.join(sandbox, "data"),
    CLAUDE_CONFIG_DIR: path.join(sandbox, "claude"),
    XDG_RUNTIME_DIR: path.join(sandbox, "run"),
    CAVE_TOOLS_GO_DIST_DIR: path.join(REPO_ROOT, "dist"),
    CAVE_TOOLS_DAEMON: "0",
    CAVE_TOOLS_PROFILE: "full",
    CAVE_TOOLS_STATUS_RTK: "0",
    LC_ALL: "C.UTF-8",
    LANG: "C.UTF-8",
  };
  const daemonEnv = { CAVE_TOOLS_DAEMON: "1" };

  const impls = {
    ts: { label: "ts", cmd: process.execPath, prefix: [tsCli], env: childEnv, cwd: fxD },
    go: { label: "go", cmd: goBin, prefix: [], env: childEnv, cwd: fxD },
  };

  const routes = buildRoutes(cfg, fxD);
  const compressText = readFileSync(path.join(fxD, "compress-fixture.txt"), "utf8");
  const stdinBufs = {
    "filter-medium.json": readFileSync(path.join(fxD, "filter-medium.json")),
    "filter-large.json": readFileSync(path.join(fxD, "filter-large.json")),
  };

  const goVersion = spawnSync("go", ["version"], { encoding: "utf8" }).stdout?.trim() ?? null;
  const report = {
    schema: "cave-tools ts-go bench v1",
    generated_at: new Date().toISOString(),
    metadata: {
      machine: {
        platform: os.platform(),
        release: os.release(),
        arch: os.arch(),
        cpu_model: os.cpus()[0]?.model ?? null,
        cpu_count: os.cpus().length,
        total_mem_bytes: os.totalmem(),
      },
      runtime: {
        node_version: process.version,
        node_path: process.execPath,
        go_version: goVersion,
      },
      refs: {
        ts_cli: tsCli,
        ts_repo: tsRepo,
        ts_commit: gitCommit(tsRepo),
        go_src: cfg.goBin ? null : goSrc,
        go_repo: cfg.goBin ? null : goSrcRepo,
        go_commit: cfg.goBin ? null : gitCommit(goSrcRepo),
        go_binary: goBin,
        go_binary_sha256: sha256(readFileSync(goBin)),
      },
      versions: {
        ts_package_version: readPackageVersion(tsRepo),
        go_package_version: cfg.goBin ? null : readPackageVersion(goSrcRepo),
      },
    },
    config: {
      repeats: cfg.repeats,
      seed: cfg.seed,
      percentile_method: "nearest-rank (sorted[ceil(p*n)-1])",
      clock: "performance.now() wall time around the whole invocation / MCP roundtrip",
      order_randomization: "per-repeat seeded coin flip, recorded per route as repeat_orders",
      env_policy: "identical child env for both sides; shared bench-owned sandbox (HOME/CAVE_TOOLS_DATA_DIR/CLAUDE_CONFIG_DIR/XDG_RUNTIME_DIR); CAVE_TOOLS_DAEMON=0 except daemon_cli_read; live user state never touched",
      sanitized_env: {
        policy: [
          "CAVE_TOOLS_FORCE_FRESH",
          "known subagent keys: CLINE_TASK_ID, CLAUDE_CODE_SUBAGENT, ANTHROPIC_SUBAGENT, CURSOR_TASK_ID",
          "TASK_ID-style keys (/(?:^|_)TASK_ID$/)",
          "implementation switch keys: CAVE_TOOLS_IMPL, CAVE_TOOLS_GO_BIN",
        ],
        stripped_from_inherited: strippedEnvKeys,
      },
      mcp_session_prefixes: { read_first: "bench-first", read_dedup: "bench-dedup", compress: "bench-compress" },
      run_dir: runDir,
      sandbox,
    },
    fixtures,
    validation: {},
    timing: {},
    memory: {},
    notes: [],
  };

  // ----------------------------------------------------------- provenance
  // Frozen-path / prebuilt-binary provenance: content fingerprint of the TS
  // dist tree, `go version -m` build info for the Go binary (module + vcs
  // metadata embedded by the Go toolchain), and optional expected refs. A
  // prebuilt --go-bin is never attributed to a git HEAD: its source ref comes
  // from vcs.revision in its own build info or from the asserted --go-bin-ref
  // label. Mismatches are recorded and noted, never silently accepted.
  const tsDistDir = path.join(tsRepo, "dist");
  const goVersionM = (() => {
    const res = spawnSync("go", ["version", "-m", goBin], { encoding: "utf8", timeout: 60_000 });
    return res.status === 0 ? res.stdout.trim().split("\n").slice(0, 60).join("\n") : null;
  })();
  const vcsRevision = /^\s*vcs\.revision=(.+)$/m.exec(goVersionM ?? "")?.[1]?.trim() ?? null;
  const vcsTime = /^\s*vcs\.time=(.+)$/m.exec(goVersionM ?? "")?.[1]?.trim() ?? null;
  const vcsModified = /^\s*vcs\.modified=(.+)$/m.exec(goVersionM ?? "")?.[1]?.trim() ?? null;
  const goLdflags = `-X main.version=${readPackageVersion(path.resolve(goSrc, "..")) ?? "0.6.1"}`;
  report.provenance = {
    ts_dist_manifest_sha256: existsSync(tsDistDir) ? distManifestSha256(tsDistDir) : null,
    go_binary: {
      path: goBin,
      sha256: sha256(readFileSync(goBin)),
      built_from_source: !cfg.goBin,
      ref_label: cfg.provenance.goBinRef,
      build_command: cfg.goBin
        ? null
        : `go build -ldflags "${goLdflags}" -o <run>/bin/cave-tools-go ./cmd/cave-tools-go  (cwd ${goSrc}, GOPROXY=off GOSUMDB=off)`,
      vcs_revision: vcsRevision,
      vcs_time: vcsTime,
      vcs_modified: vcsModified,
      go_version_m: goVersionM,
    },
    expected: {
      ts_commit: cfg.provenance.expectTsCommit,
      ts_dist_manifest_sha256: cfg.provenance.expectTsDistSha256,
      go_commit: cfg.provenance.expectGoCommit,
      go_binary_sha256: cfg.provenance.expectGoBinarySha256,
    },
    mismatches: [],
  };
  const provenanceCheck = (name, expected, actual) => {
    if (expected == null) return;
    if (String(expected).toLowerCase() !== String(actual).toLowerCase()) {
      report.provenance.mismatches.push({ name, expected, actual });
      report.notes.push(`provenance mismatch: ${name} expected ${expected}, got ${actual ?? "null"}`);
    }
  };
  provenanceCheck("ts_commit", cfg.provenance.expectTsCommit, report.metadata.refs.ts_commit);
  provenanceCheck("ts_dist_manifest_sha256", cfg.provenance.expectTsDistSha256, report.provenance.ts_dist_manifest_sha256);
  provenanceCheck("go_binary_sha256", cfg.provenance.expectGoBinarySha256, report.provenance.go_binary.sha256);
  // go_commit: source ref of the measured Go binary — built-from-source uses
  // the go-src repo commit, prebuilt uses its own embedded vcs.revision.
  provenanceCheck("go_commit", cfg.provenance.expectGoCommit, report.metadata.refs.go_commit ?? vcsRevision);
  if (cfg.goBin && vcsRevision == null && cfg.provenance.goBinRef == null) {
    report.notes.push(
      "prebuilt go binary carries no vcs.revision build stamp and no --go-bin-ref label: its " +
      "source ref is unproven (binary sha256 recorded; supply the build-job ref explicitly)",
    );
  }

  // ---------------------------------------------------------- validation
  console.error(`bench-ts-go: run ${runId} — validating ${routes.length} route(s) before timing`);

  const daemonRunning = { ts: false, go: false };
  function daemonStatusPid(impl) {
    const res = runCliRaw(impl, ["daemon", "status"], null, daemonEnv);
    const m = /running pid (\d+)/.exec(res.stdout.toString("utf8"));
    return res.code === 0 && m ? Number(m[1]) : null;
  }
  async function daemonWarmup(impl) {
    for (let i = 0; i < 2; i++) {
      runCliRaw(impl, ["read", path.join(fxD, "warmup-fixture.txt"), "--session", `warmup-d-${i}`], null, daemonEnv);
    }
    const pid = daemonStatusPid(impl);
    daemonRunning[impl.label] = pid !== null;
    if (pid !== null) {
      cleanupTasks.push(() => {
        if (daemonRunning[impl.label]) daemonStop(impl);
      });
    }
    return pid;
  }
  function daemonStop(impl) {
    runCliRaw(impl, ["daemon", "stop"], null, daemonEnv);
    const stillUp = daemonStatusPid(impl);
    if (stillUp !== null) {
      try { process.kill(stillUp, "SIGKILL"); } catch { /* already gone */ }
      report.notes.push(`daemon ${impl.label}: 'daemon stop' left pid ${stillUp} alive; harness SIGKILLed it`);
    }
    daemonRunning[impl.label] = false;
    return stillUp === null;
  }

  // Warm the daemons up front when the daemon route is in play.
  const daemonFeasible = {};
  for (const route of routes) {
    if (route.kind !== "daemon") continue;
    for (const impl of Object.values(impls)) {
      const pid = await daemonWarmup(impl);
      daemonFeasible[impl.label] = pid !== null;
      if (pid === null) {
        report.notes.push(`daemon_cli_read: ${impl.label} daemon did not auto-start in the sandbox; route skipped for that side`);
      }
    }
    if (!daemonFeasible.ts || !daemonFeasible.go) {
      report.notes.push("daemon_cli_read excluded from timing: daemon not safely feasible for both sides");
    }
  }

  for (const route of routes) {
    if (route.kind === "daemon" && !(daemonFeasible.ts && daemonFeasible.go)) {
      report.validation[route.id] = { skipped: "daemon not feasible for both sides" };
      continue;
    }
    if (route.kind === "mcp") {
      // Validate this route's own call shape once per side, in a session whose
      // prefix matches timing (bench-val-<call>), and record per-side payload
      // refs (canonical result hash + text hash/length + stub flag) that the
      // per-sample checks compare against.
      const call = route.call;
      const responses = {};
      const identities = {};
      for (const impl of Object.values(impls)) {
        const client = new McpClient(impl);
        client.readFixture = path.join(fxD, "read-fixture.txt");
        client.compressText = compressText;
        await client.initialize();
        await warmupClient(client);
        const res = await mcpInvoke(client, call, `bench-val-${call}`);
        responses[impl.label] = res;
        identities[impl.label] = resultIdentity(res.msg);
        await client.close();
      }
      const cmp = compareOutputs(
        { stdout: responses.ts.raw, code: 0, stderr: Buffer.alloc(0) },
        { stdout: responses.go.raw, code: 0, stderr: Buffer.alloc(0) },
      );
      cmp.exit_equal = true;
      cmp.stderr_equal = true;
      cmp.match = cmp.stdout_equal;
      cmp.result_match = canonicalJSON(responses.ts.msg?.result ?? null) === canonicalJSON(responses.go.msg?.result ?? null);
      cmp.envelope_only_drift = !cmp.match && cmp.result_match;
      const entry = {
        ...cmp,
        ts_result_sha256: identities.ts.result_sha256,
        go_result_sha256: identities.go.result_sha256,
        ts_text_sha256: identities.ts.text_sha256,
        go_text_sha256: identities.go.text_sha256,
        ts_text_length: identities.ts.text_length,
        go_text_length: identities.go.text_length,
        ts_is_stub: identities.ts.is_stub,
        go_is_stub: identities.go.is_stub,
        ts_is_error: identities.ts.is_error,
        go_is_error: identities.go.is_error,
        invariant: call === "read_first" ? "first read in session must be full (not a stub)"
          : call === "read_dedup" ? "paired re-read in session must be the dedup stub"
          : "none",
      };
      const invariantOk = call === "read_first" ? (!identities.ts.is_stub && !identities.go.is_stub)
        : call === "read_dedup" ? (identities.ts.is_stub && identities.go.is_stub)
        : true;
      entry.invariant_ok = invariantOk;
      if (!invariantOk) {
        report.notes.push(
          `mcp ${route.id}: validation-run invariant violated (ts_is_stub=${identities.ts.is_stub}, go_is_stub=${identities.go.is_stub}) — route flagged`,
        );
      }
      report.validation[`${route.id}:${call}`] = entry;
      continue;
    }
    // CLI and daemon routes: one plain run per side.
    const out = { ts: null, go: null };
    for (const impl of Object.values(impls)) {
      const stdinBuf = route.stdin ? stdinBufs[route.stdin] : null;
      out[impl.label] = runCliRaw(impl, route.argv("val"), stdinBuf, route.daemonEnv ? daemonEnv : null);
    }
    report.validation[route.id] = compareOutputs(out.ts, out.go);
  }
  if (Object.values(report.validation).some((v) => v.envelope_only_drift)) {
    report.notes.push(
      "MCP routes: JSON-RPC envelope key order differs (TS SDK serializes result-first; Go emits " +
      "canonical jsonrpc/id/result order) while the result payloads match (result_match=true). " +
      "Byte-strict policy flags these routes and keeps them out of summary.matched; their timings " +
      "are still collected and reported under summary.flagged.",
    );
  }

  // -------------------------------------------------------------- timing
  console.error("bench-ts-go: timing");

  // Persistent MCP session pair for the MCP routes (warm, one per side).
  const mcpRoutes = routes.filter((r) => r.kind === "mcp");
  const mcpClients = {};
  if (mcpRoutes.length > 0) {
    for (const impl of Object.values(impls)) {
      const client = new McpClient(impl);
      client.readFixture = path.join(fxD, "read-fixture.txt");
      client.compressText = compressText;
      await client.initialize();
      await warmupClient(client); // untimed read + compress warmups
      mcpClients[impl.label] = client;
    }
  }

  const daemonRoutes = routes.filter((r) => r.kind === "daemon" && daemonFeasible.ts && daemonFeasible.go);

  for (const route of routes) {
    const validationKey = route.kind === "mcp" ? `${route.id}:${route.call}` : route.id;
    const v = report.validation[validationKey];
    if (v && v.skipped) continue;
    const rng = mulberry32((cfg.seed >>> 0) + routes.indexOf(route) * 7919);
    const repeatOrders = [];
    const samples = { ts: [], go: [] };

    for (let r = 0; r < cfg.repeats; r++) {
      const order = rng() < 0.5 ? ["ts", "go"] : ["go", "ts"];
      repeatOrders.push(order);
      for (const label of order) {
        const impl = impls[label];
        let sample;
        if (route.kind === "mcp") {
          const client = mcpClients[label];
          const session = `${MCP_SESSION_PREFIX[route.call]}-${r}`;
          // read_dedup: untimed paired first read keeps this call a dedup hit
          // within its own per-route session.
          if (route.call === "read_dedup") await readFrame(client, session);
          const t0 = performance.now();
          const res = route.call === "compress"
            ? await compressFrame(client, session)
            : await readFrame(client, session);
          const ms = performance.now() - t0;
          // Payload identity is computed AFTER the captured timer and checked
          // against this side's own validation refs — canonical result hash +
          // text length, never raw frame length (request-id digits vary per
          // sample). Invariants: read_first must be full, read_dedup the stub.
          const id = resultIdentity(res.msg);
          const vEntry = report.validation[`${route.id}:${route.call}`];
          // read_dedup: stub-pattern identity (session tag + read count are
          // contractually variable). read_first / compress: literal canonical
          // result hash + text length against this side's refs.
          const payloadMatch = route.call === "read_dedup"
            ? id.is_stub && READ_STUB_RE.test(frameText(res.msg))
            : id.result_sha256 === vEntry[`${label}_result_sha256`] && id.text_length === vEntry[`${label}_text_length`];
          const invariantOk = route.call === "read_first" ? !id.is_stub
            : route.call === "read_dedup" ? id.is_stub
            : true;
          sample = {
            r, ms,
            exit: id.is_error ? 1 : 0,
            stdout_bytes: res.raw.length,
            stderr_bytes: 0,
            result_sha256: id.result_sha256,
            text_length: id.text_length,
            is_stub: id.is_stub,
          };
          sample.valid = !id.is_error && payloadMatch && invariantOk;
          if (!sample.valid) {
            sample.invalid_reason = !invariantOk
              ? `invariant violated: expected ${route.call === "read_first" ? "full read" : "dedup stub"}, got ${id.is_stub ? "stub" : "full"}`
              : "result payload hash/length deviated from this side's validation run";
          }
        } else {
          const stdinBuf = route.stdin ? stdinBufs[route.stdin] : null;
          const out = runCliRaw(impl, route.argv(r), stdinBuf, route.daemonEnv ? daemonEnv : null);
          sample = {
            r, ms: out.ms, exit: out.code,
            stdout_bytes: out.stdout.length, stderr_bytes: out.stderr.length,
          };
          // Hash + exit checks computed after the captured timer and compared
          // against this side's validated reference — content hashes, not just
          // lengths. A flagged cross-side content difference never voids a
          // sample while a real per-side drift still does.
          const outHash = sha256(out.stdout);
          const errHash = sha256(out.stderr);
          sample.stdout_sha256 = outHash;
          sample.stderr_sha256 = errHash;
          const expect = report.validation[route.id][label];
          sample.valid = out.code === expect.exit && outHash === expect.stdout_sha256 && errHash === expect.stderr_sha256;
          if (!sample.valid) sample.invalid_reason = "exit code or stdout/stderr hash deviated from this side's validation run";
        }
        samples[label].push(sample);
      }
    }

    const tsStats = stats(samples.ts);
    const goStats = stats(samples.go);
    report.timing[route.id] = {
      scope: route.scope,
      note: route.note,
      validation_key: validationKey,
      output_match: v ? (v.match ?? null) : null,
      excluded_from_matched_summary: v ? !(v.match ?? false) : true,
      repeat_orders: repeatOrders,
      output_bytes: v ? { ts: v.ts.stdout_bytes, go: v.go.stdout_bytes } : null,
      samples,
      stats: { ts: tsStats, go: goStats },
      p50_ratio_ts_over_go: tsStats.p50_ms && goStats.p50_ms ? tsStats.p50_ms / goStats.p50_ms : null,
    };
    const t = report.timing[route.id];
    console.error(
      `  ${route.id.padEnd(18)} p50 ts=${(tsStats.p50_ms ?? NaN).toFixed(1)}ms go=${(goStats.p50_ms ?? NaN).toFixed(1)}ms ` +
      `p95 ts=${(tsStats.p95_ms ?? NaN).toFixed(1)}ms go=${(goStats.p95_ms ?? NaN).toFixed(1)}ms ` +
      `n=${tsStats.n_valid}/${goStats.n_valid}${t.excluded_from_matched_summary ? "  [FLAGGED: output mismatch]" : ""}`,
    );
  }

  for (const client of Object.values(mcpClients)) await client.close();
  for (const impl of Object.values(impls)) {
    if (daemonRunning[impl.label]) daemonStop(impl);
  }

  // -------------------------------------------------------------- memory
  // Separate runs AFTER timing so they never contaminate the samples.
  if (cfg.memory) {
    console.error("bench-ts-go: memory phase (separate runs)");
    const hasTime = existsSync("/usr/bin/time");
    for (const route of routes) {
      if (route.kind === "mcp") continue;
      const v = report.validation[route.id];
      if (v && v.skipped) continue;
      if (!hasTime) {
        report.memory[route.id] = { method: "/usr/bin/time -v", unrun: "/usr/bin/time not installed" };
        continue;
      }
      const mem = { method: "/usr/bin/time -v (Maximum resident set size), 3 separate runs per side; CLI wrapper process only", ts: [], go: [] };
      for (const impl of Object.values(impls)) {
        if (route.daemonEnv) await daemonWarmup(impl); // steady-state daemon, untimed
        for (let i = 0; i < 3; i++) {
          const env = { ...impl.env, ...(route.daemonEnv ? daemonEnv : {}) };
          const res = spawnSync("/usr/bin/time", ["-v", impl.cmd, ...impl.prefix, ...route.argv(`mem-${i}`)], {
            input: route.stdin ? stdinBufs[route.stdin] : Buffer.alloc(0),
            env, cwd: impl.cwd, encoding: "utf8", timeout: 300_000, maxBuffer: 64 * 1024 * 1024,
          });
          const m = /Maximum resident set size \(kbytes\): (\d+)/.exec(res.stderr ?? "");
          mem[impl.label].push(m ? Number(m[1]) : null);
        }
        if (route.daemonEnv) daemonStop(impl);
      }
      report.memory[route.id] = mem;
    }
    // MCP server memory: VmHWM after one validated triple (Linux /proc).
    if (mcpRoutes.length > 0) {
      const mem = { method: "/proc/<pid>/status VmHWM of a fresh MCP server after init + warmups + one validated triple (not /usr/bin/time)", ts: [], go: [] };
      for (const impl of Object.values(impls)) {
        const client = new McpClient(impl);
        client.readFixture = path.join(fxD, "read-fixture.txt");
        client.compressText = compressText;
        await client.initialize();
        await warmupClient(client);
        await mcpInvoke(client, "read_first", "mem-first");
        await mcpInvoke(client, "read_dedup", "mem-dedup");
        await mcpInvoke(client, "compress", "mem-compress");
        const statusPath = `/proc/${client.child.pid}/status`;
        let vmHwm = null;
        if (existsSync(statusPath)) {
          const m = /VmHWM:\s+(\d+) kB/.exec(readFileSync(statusPath, "utf8"));
          vmHwm = m ? Number(m[1]) : null;
        }
        mem[impl.label].push(vmHwm);
        await client.close();
      }
      report.memory.mcp_server = mem;
    }
    // Daemon process memory: VmHWM after warmup + one call.
    if (daemonRoutes.length > 0 && existsSync("/proc/self/status")) {
      const mem = { method: "/proc/<pid>/status VmHWM of the auto-started daemon after warmup + 1 call", ts: [], go: [] };
      for (const impl of Object.values(impls)) {
        const pid = await daemonWarmup(impl);
        let vmHwm = null;
        if (pid !== null && existsSync(`/proc/${pid}/status`)) {
          const m = /VmHWM:\s+(\d+) kB/.exec(readFileSync(`/proc/${pid}/status`, "utf8"));
          vmHwm = m ? Number(m[1]) : null;
        }
        mem[impl.label].push(vmHwm);
        daemonStop(impl);
      }
      report.memory.daemon_server = mem;
    }
  }

  // --------------------------------------------------------------- report
  report.summary = { matched: [], flagged: [] };
  for (const route of routes) {
    const t = report.timing[route.id];
    if (!t) continue;
    const row = {
      route: route.id,
      n: { ts: t.stats.ts.n_valid, go: t.stats.go.n_valid },
      out_bytes: t.output_bytes,
      ts_p50_ms: t.stats.ts.p50_ms,
      go_p50_ms: t.stats.go.p50_ms,
      ts_p95_ms: t.stats.ts.p95_ms,
      go_p95_ms: t.stats.go.p95_ms,
      p50_ratio_ts_over_go: t.p50_ratio_ts_over_go,
      // For MCP routes: whether the ToolResult payloads are equivalent even
      // when the byte-strict envelope comparison flagged the route.
      payload_equivalent: t.validation_key.includes(":")
        ? (report.validation[t.validation_key]?.result_match ?? null)
        : t.output_match,
    };
    (t.excluded_from_matched_summary ? report.summary.flagged : report.summary.matched).push(row);
  }

  const outPath = cfg.out ? path.resolve(cfg.out) : path.join(runDir, "results.json");
  mkdirSync(path.dirname(outPath), { recursive: true });
  writeFileSync(outPath, JSON.stringify(report, null, 2));
  report.notes.length === 0 || console.error(`notes:\n- ${report.notes.join("\n- ")}`);

  console.log(`\n${"route".padEnd(18)} ${"impl".padEnd(4)} ${"n".padEnd(4)} ${"p50ms".padStart(9)} ${"p95ms".padStart(9)} ${"min".padStart(8)} ${"max".padStart(9)} ${"outB".padStart(8)}`);
  for (const route of routes) {
    const t = report.timing[route.id];
    if (!t) continue;
    for (const label of ["ts", "go"]) {
      const s = t.stats[label];
      const f = (x) => (x == null ? "-" : x.toFixed(1));
      console.log(
        `${route.id.padEnd(18)} ${label.padEnd(4)} ${String(s.n_valid).padEnd(4)} ${f(s.p50_ms).padStart(9)} ${f(s.p95_ms).padStart(9)} ${f(s.min_ms).padStart(8)} ${f(s.max_ms).padStart(9)} ${String(t.output_bytes?.[label] ?? "-").padStart(8)}` +
        (t.excluded_from_matched_summary ? "  [flagged: output mismatch]" : ""),
      );
    }
  }
  console.log(`\nresults: ${outPath}`);
  console.log(`sha256:  ${sha256(readFileSync(outPath))}`);
  runCleanup();
}

main().catch((error) => {
  console.error("bench-ts-go: failed:", error);
  runCleanup();
  process.exit(1);
});
