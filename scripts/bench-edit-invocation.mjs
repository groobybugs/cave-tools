#!/usr/bin/env node
// Live-model invocation-reliability pilot for the edit path.
// One private `opencode serve --hostname 127.0.0.1 --port 0` per trial
// (owned detached process group, URL + ephemeral password from its stdout,
// clients via `opencode run --server <url>`), then graceful stop + bounded
// force-kill of ONLY that group. No global process scans, no PID diffs.
// Sessions run through the official CLI, so they appear in ordinary OpenCode
// history; authentication uses only the official CLI, with no direct DB access.
// Per trial: fresh XDG_CONFIG_HOME + OPENCODE_CONFIG_DIR, single-file fixture
// cwd, fresh CLAUDE_CONFIG_DIR. --db overrides an inherited OPENCODE_DB.
// Stdout: one JSON aggregate per trial (finite codes only — no raw text,
// prompts, args, stderr, or auth). Scratch removed in `finally`.
// Tiny pilot — do NOT claim causal proof or performance gains from output.
// Usage: bench-edit-invocation.mjs [--models a,b] [--profiles a,b]
//   [--repeats N] [--timeout-ms N] [--db path] [--help]

import { spawn } from "node:child_process";
import {
  existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync,
  rmSync, writeFileSync,
} from "node:fs";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "..");

export const DEFAULTS = {
  models: ["opencode-go/mimo-v2.6-pro", "opencode-go/deepseek-v4.1-flash#max",
    "opencode-go/muse-spark-1.3-contributor#xhigh"],
  profiles: ["native-only", "mixed", "cave-only"],
  repeats: 1,
  timeoutMs: 120000,
  serverReadyMs: 30000,
};

const FIXTURE_NAME = "target.txt";
const FIXTURE_BEFORE = "alpha 1\nbeta 2\ngamma 3\n";
const FIXTURE_AFTER = "alpha 1\nbeta TWO\ngamma 3\n";

// Only known native/Cave prefixes count as editing tools, not arbitrary suffixes.
const EDIT_TOOLS = new Set(["edit", "write", "patch", "apply_patch"]);
const READ_TOOLS = new Set(["read"]);

export function normalizeTool(name) {
  const value = String(name || "").toLowerCase();
  for (const prefix of ["cave-tools_cave__", "cave__"]) {
    if (value.startsWith(prefix)) return value.slice(prefix.length);
  }
  return value;
}
export function isEditTool(name) { return EDIT_TOOLS.has(normalizeTool(name)); }
export function isReadTool(name) { return READ_TOOLS.has(normalizeTool(name)); }

export function buildPrompt(targetAbs) {
  return `In ${targetAbs} replace 'beta 2' with 'beta TWO'.`
    + ` First read the file, then make exactly that edit and nothing else.`
    + ` Do not touch any other file. Reply with exactly: done`;
}

export function parseArgs(argv) {
  const out = { ...DEFAULTS, models: [...DEFAULTS.models], profiles: [...DEFAULTS.profiles], help: false, db: null };
  const split = (v) => v.split(",").map((s) => s.trim()).filter(Boolean);
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i], nx = () => argv[++i] ?? "";
    if (a === "--help" || a === "-h") out.help = true;
    else if (a === "--models") out.models = split(nx());
    else if (a === "--profiles") out.profiles = split(nx());
    else if (a === "--repeats") out.repeats = Math.max(1, Number.parseInt(nx() || "1", 10) || 1);
    else if (a === "--timeout-ms") out.timeoutMs = Math.max(1000, Number.parseInt(nx(), 10) || DEFAULTS.timeoutMs);
    else if (a === "--db") out.db = nx() || null;
    else throw new Error("unknown arg");
  }
  for (const p of out.profiles) {
    if (!DEFAULTS.profiles.includes(p)) throw new Error("unknown profile");
  }
  return out;
}

// One JSONL line -> event object, or null when unparseable. Never throws.
export function parseEventLine(line) {
  const t = String(line || "").trim();
  if (!t) return null;
  try {
    const e = JSON.parse(t);
    return e && typeof e === "object" ? e : null;
  } catch { return null; }
}

// Keep only finite outcomes, never input values or reflected error payloads.
// Deduplicate lifecycle updates; incomplete token totals stay unknown.
export function summarizeEvents(events) {
  const calls = new Map();
  let anon = 0, tokenSteps = 0, eventError = null;
  const tokAcc = { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 };
  const tokComplete = Object.fromEntries(Object.keys(tokAcc).map((key) => [key, true]));
  for (const e of events) {
    if (!e) continue;
    if (e.type === "tool_use" && e.part) {
      const key = e.part.id ?? e.part.partID ?? e.part.callId ?? `anon-${anon++}`;
      const name = normalizeTool(e.part.tool);
      const tool = EDIT_TOOLS.has(name) || READ_TOOLS.has(name) ? String(e.part.tool) : "other";
      const status = ["pending", "running", "completed", "error"].includes(e.part.state?.status)
        ? e.part.state.status : "unknown";
      // A successful call passed validation. Only the known host validator
      // prefix proves rejection; matcher/permission failures remain unknown.
      const inputValid = status === "completed" ? true
        : status === "error" && /^Invalid arguments for tool /.test(e.part.state?.error ?? "") ? false : null;
      if (calls.has(key)) Object.assign(calls.get(key), { status, inputValid });
      else calls.set(key, { tool, status, inputValid });
    } else if (e.type === "step_finish") {
      tokenSteps++;
      const t = e.part?.tokens ?? {};
      const take = (field, value) => {
        if (typeof value === "number" && Number.isFinite(value) && value >= 0) tokAcc[field] += value;
        else tokComplete[field] = false;
      };
      take("input", t.input); take("output", t.output); take("reasoning", t.reasoning);
      take("cacheRead", t.cache?.read); take("cacheWrite", t.cache?.write);
    } else if (e.type === "error" && !eventError) {
      eventError = e.error?.type === "provider.no-route" ? "event-error:provider.no-route" : "event-error";
    }
  }
  const ordered = [...calls.values()];
  const edits = ordered.filter((call) => isEditTool(call.tool));
  const tokens = Object.fromEntries(Object.keys(tokAcc).map((key) => [key,
    tokenSteps && tokComplete[key] ? tokAcc[key] : null]));
  return {
    toolUses: ordered, editAttempts: edits.length,
    editErrors: edits.filter((call) => call.status !== "completed").length,
    readAttempts: ordered.filter((call) => isReadTool(call.tool)).length,
    firstEditSucceeded: edits.length ? edits[0].status === "completed" : null,
    firstEditValid: edits.length ? edits[0].inputValid : null,
    tokens, eventError,
  };
}

export function sha1(s) { return createHash("sha1").update(s).digest("hex"); }

// Names in workDir besides the fixture. Names only.
export function findUnexpected(workDir) {
  return readdirSync(workDir).filter((n) => n !== FIXTURE_NAME).sort();
}

export function profileConfig(profile, trial) {
  if (!DEFAULTS.profiles.includes(profile)) throw new Error("unknown profile");
  const cfg = { $schema: "https://opencode.ai/config.json", mcp: { servers: {} }, plugins: [] };
  if (profile === "mixed" || profile === "cave-only") {
    // Native V2 shape: codemode false exposes real Cave schemas (Code Mode
    // would hide edit attempts behind execute). No deprecated `enabled` key.
    cfg.mcp.servers["cave-tools"] = { type: "local", codemode: false,
      command: [process.execPath, join(repo, "dist", "cli.js"), "mcp"],
      environment: { CAVE_TOOLS_DAEMON: "0", CLAUDE_CONFIG_DIR: trial.claudeDir,
        CAVE_TOOLS_DATA_DIR: join(trial.root, "data") } };
  }
  if (profile === "cave-only") {
    cfg.plugins = [join(repo, "src", "plugins", "opencode")];
    const flagDir = join(trial.root, "opencode");
    mkdirSync(flagDir, { recursive: true });
    writeFileSync(join(flagDir, ".cave-tools-active"), "enforce");
  }
  return cfg;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Start ONE private server as an owned detached group. Resolves with its
// advertised URL + ephemeral password (memory only), or a finite code.
export function startServer(env, workdir, readyMs) {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn("opencode", ["serve", "--hostname", "127.0.0.1", "--port", "0"],
        { cwd: workdir, env, stdio: ["ignore", "pipe", "pipe"], detached: true });
    } catch { resolve({ ok: false, code: "server-spawn-fail" }); return; }
    let buf = "", done = false;
    const finish = (r) => { if (!done) { done = true; clearTimeout(timer); resolve(r); } };
    const timer = setTimeout(() => {
      try { process.kill(-child.pid, "SIGKILL"); } catch { /* exited */ }
      finish({ ok: false, code: "server-ready-timeout" });
    }, readyMs);
    const onData = (c) => {
      buf += c; if (buf.length > 65536) buf = buf.slice(-65536);
      const u = /server listening on (\S+)/.exec(buf);
      const p = /^server password (\S+)/m.exec(buf);
      if (u && p) finish({ ok: true, url: u[1], password: p[1], pid: child.pid });
    };
    child.stdout?.on("data", onData);
    child.stderr?.on("data", onData);
    child.on("error", () => finish({ ok: false, code: "server-spawn-fail" }));
    child.on("close", (code) => finish({ ok: false, code: "server-exit-" + code }));
  });
}

// Stop ONLY the owned server group: graceful TERM, bounded wait, then KILL.
export async function stopServer(pid, graceMs = 3000) {
  if (pid === null || pid === undefined) return;
  try { process.kill(-pid, "SIGTERM"); } catch { return; }
  const t0 = Date.now();
  while (Date.now() - t0 < graceMs) {
    await sleep(200);
    try { process.kill(-pid, 0); } catch { return; }
  }
  try { process.kill(-pid, "SIGKILL"); } catch { /* exited */ }
}

export function runChild(args, opts, timeoutMs) {
  return new Promise((resolve) => {
    const start = Date.now();
    const child = spawn(args[0], args.slice(1), { ...opts, detached: true });
    let out = "", timedOut = false, done = false, forceTimer;
    const finish = (err) => {
      if (done) return;
      done = true; clearTimeout(killTimer); clearTimeout(forceTimer);
      try { process.kill(-child.pid, "SIGKILL"); } catch { /* exited */ }
      resolve({ out, timedOut, elapsedMs: Date.now() - start, error: err });
    };
    const killTimer = setTimeout(() => {
      timedOut = true;
      try { process.kill(-child.pid, "SIGTERM"); } catch { /* exited */ }
      forceTimer = setTimeout(() => {
        if (!done) { try { process.kill(-child.pid, "SIGKILL"); } catch { /* exited */ } }
      }, 2000);
    }, timeoutMs);
    child.stdout?.on("data", (c) => { out += c; });
    child.on("error", () => finish("spawn-fail"));
    child.on("close", (code) => finish(timedOut ? "timeout" : code === 0 ? null : "exit-" + code));
  });
}

export async function runTrial({ model, profile, repetition, scratch, timeoutMs, benchmarkDb }) {
  const t0 = Date.now();
  const slug = model.replace(/[^a-zA-Z0-9]+/g, "_").slice(0, 40);
  const trial = {
    root: join(scratch, `${slug}_${profile}_r${repetition}`),
    get work() { return join(this.root, "work"); },
    get cfgDir() { return join(this.root, "cfg"); },
    get claudeDir() { return join(this.root, "claude"); },
  };
  mkdirSync(trial.work, { recursive: true });
  mkdirSync(trial.cfgDir, { recursive: true });
  mkdirSync(trial.claudeDir, { recursive: true });
  const targetAbs = join(trial.work, FIXTURE_NAME);
  writeFileSync(targetAbs, FIXTURE_BEFORE);
  writeFileSync(join(trial.cfgDir, "opencode.json"), JSON.stringify(profileConfig(profile, trial)));
  const env = { ...process.env, XDG_CONFIG_HOME: trial.root, OPENCODE_CONFIG_DIR: trial.cfgDir,
    CLAUDE_CONFIG_DIR: trial.claudeDir, CAVE_TOOLS_DATA_DIR: join(trial.root, "data"),
    CAVE_TOOLS_MODE: profile === "cave-only" ? "enforce" : "hint" };
  for (const key of ["OPENCODE_PASSWORD", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CLI_CONFIG_CONTENT"]) delete env[key];
  if (benchmarkDb) env.OPENCODE_DB = resolve(benchmarkDb);
  const srv = await startServer(env, trial.work, DEFAULTS.serverReadyMs);
  if (!srv.ok) return baseResult(model, profile, repetition, srv.code, Date.now() - t0);
  try {
    const res = await runChild(["opencode", "run", "--server", srv.url, "--auto", "--format", "json",
      "--model", model, "--title", `Cave edit benchmark ${profile} r${repetition}`, buildPrompt(targetAbs)],
    { cwd: trial.work, env: { ...env, OPENCODE_PASSWORD: srv.password }, stdio: ["ignore", "pipe", "ignore"] },
    timeoutMs);
    const sum = summarizeEvents(String(res.out || "").split("\n").map(parseEventLine).filter(Boolean));
    let after = null, unexpected = [];
    try { after = readFileSync(targetAbs, "utf8"); } catch { /* deleted */ }
    try { unexpected = findUnexpected(trial.work); } catch { /* gone */ }
    const error = res.error ?? sum.eventError;
    return { model, profile, repetition, completed: after === FIXTURE_AFTER && !unexpected.length && !res.timedOut && !error,
      resultHash: after === null ? null : sha1(after), expectedHash: sha1(FIXTURE_AFTER),
      unexpectedChanges: unexpected.length, editAttempts: sum.editAttempts, editErrors: sum.editErrors,
      readAttempts: sum.readAttempts, firstEditSucceeded: sum.firstEditSucceeded,
      firstEditValid: sum.firstEditValid,
      toolUses: sum.toolUses.map((t) => t.tool + ":" + t.status), tokens: sum.tokens,
      elapsedMs: Date.now() - t0, timedOut: res.timedOut, error };
  } finally {
    await stopServer(srv.pid);
  }
}

function baseResult(model, profile, repetition, error, elapsedMs) {
  return { model, profile, repetition, completed: false, resultHash: null, expectedHash: sha1(FIXTURE_AFTER),
    unexpectedChanges: 0, editAttempts: 0, editErrors: 0, readAttempts: 0, firstEditSucceeded: null,
    firstEditValid: null, toolUses: [],
    tokens: { input: null, output: null, reasoning: null, cacheRead: null, cacheWrite: null },
    elapsedMs, timedOut: error === "server-ready-timeout", error };
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help || !opts.models.length || !opts.profiles.length) {
    console.error("usage: bench-edit-invocation.mjs [--models a,b] [--profiles a,b] [--repeats N] [--timeout-ms N] [--db path]");
    console.error("defaults: models=" + DEFAULTS.models.join(",") + " profiles=" + DEFAULTS.profiles.join(","));
    process.exit(opts.help ? 0 : 2);
  }
  if (!existsSync(join(repo, "dist", "cli.js"))) { console.error("missing dist/cli.js"); process.exit(2); }
  const base = join(tmpdir(), "opencode");
  mkdirSync(base, { recursive: true, mode: 0o700 });
  const scratch = mkdtempSync(join(base, "bench-edit-"));
  try {
    for (const model of opts.models) for (const profile of opts.profiles) for (let r = 1; r <= opts.repeats; r++) {
      console.error(`trial ${model} ${profile} rep=${r}`);
      console.log(JSON.stringify(await runTrial({ model, profile, repetition: r, scratch,
        timeoutMs: opts.timeoutMs, benchmarkDb: opts.db })));
    }
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href;
if (isMain) main().catch(() => { console.error("fatal"); process.exit(1); });
