// Regression tests for the privacy-safe persistent tool-error log.
// Imports the built output (dist/) directly, so run `pnpm build` first or use
// `pnpm test` / `pnpm test:reliability`, which build once before the runner.
// Isolated: every test uses a fresh CAVE_TOOLS_DATA_DIR and the script points
// CLAUDE_CONFIG_DIR at its own scratch directory.
//
// Run: node scripts/test-tool-errors.mjs
import { strict as assert } from "node:assert";
import { spawnSync } from "node:child_process";
import {
  existsSync,
  lstatSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const TMP_BASE = process.platform === "linux" ? join("/tmp", "opencode") : tmpdir();
mkdirSync(TMP_BASE, { recursive: true, mode: 0o700 });
const SCRATCH = mkdtempSync(join(TMP_BASE, "tool-errors-"));
process.env.CLAUDE_CONFIG_DIR = join(SCRATCH, "claude");
mkdirSync(process.env.CLAUDE_CONFIG_DIR, { recursive: true, mode: 0o700 });
const DIST = join(ROOT, "dist");

let pass = 0;
function ok(name) {
  pass += 1;
  console.log(`PASS ${name}`);
}

function freshDataDir(tag) {
  const dir = mkdtempSync(join(SCRATCH, `data-${tag}-`));
  process.env.CAVE_TOOLS_DATA_DIR = dir;
  return dir;
}

// Test the actual built output. Fail loudly on missing/stale dist instead of
// compiling a private slice that can hide integration or type errors.
function requireFreshDist() {
  const pairs = [
    ["src/runtime/tool-errors.ts", "runtime/tool-errors.js"],
    ["src/runtime/tool-errors-cli.ts", "runtime/tool-errors-cli.js"],
    ["src/dispatch.ts", "dispatch.js"],
    ["src/cli.ts", "cli.js"],
    ["src/cli-tools.ts", "cli-tools.js"],
  ];
  for (const [source, built] of pairs) {
    const sourcePath = join(ROOT, source);
    const builtPath = join(DIST, built);
    assert.ok(existsSync(builtPath), `missing ${builtPath} — run pnpm build first`);
    assert.ok(
      statSync(builtPath).mtimeMs >= statSync(sourcePath).mtimeMs,
      `stale ${builtPath} — rebuild before testing`,
    );
  }
}

requireFreshDist();
const Terr = await import(pathToFileURL(join(DIST, "runtime", "tool-errors.js")).href);
const TerrCli = await import(pathToFileURL(join(DIST, "runtime", "tool-errors-cli.js")).href);
const Dispatch = await import(pathToFileURL(join(DIST, "dispatch.js")).href);
const { default: Database } = await import("better-sqlite3");

const SESSION = "test-session";

// Drain the scheduled sink synchronously (tests never rely on
// implicit timing).
function drain() {
  Terr.flushToolErrorQueue();
}

function dbBytes() {
  return readFileSync(join(process.env.CAVE_TOOLS_DATA_DIR, "cave-tools.db"));
}

// 1. soft capture: result unchanged, row recorded with finite code.
{
  freshDataDir("soft");
  Terr.resetToolErrorStateForTests();
  const bad = resolve("/nonexistent-xyz-123");
  const via = await Dispatch.callTool("cave__ls", { path: "/nonexistent-xyz-123" }, { sessionId: SESSION });
  const ls = await Dispatch.loadTool("cave__ls");
  const direct = await ls.handler({ path: bad });
  assert.deepEqual(via, direct, "dispatch result must equal direct handler result");
  assert.equal(via.isError, true);
  drain();
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].outcome, "soft");
  assert.equal(rows[0].tool, "cave__ls");
  assert.equal(rows[0].reasonCode, "not_found");
  assert.deepEqual(rows[0].argKeys, ["path"]);
  assert.equal(rows[0].argTypes.path, "string");
  assert.equal(rows[0].unknownArgCount, 0);
  assert.ok(rows[0].elapsedMs >= 0);
  assert.match(rows[0].sessionHash, /^[0-9a-f]{16}$/);
  ok("soft capture + result unchanged");
}

// 2. thrown capture: unknown tool rethrows identically, row logged.
{
  freshDataDir("thrown-unknown");
  Terr.resetToolErrorStateForTests();
  let caught = null;
  try {
    await Dispatch.callTool("cave__nope", {}, { sessionId: SESSION });
  } catch (e) {
    caught = e;
  }
  assert.ok(caught instanceof Error);
  assert.equal(caught.message, "Unknown tool: cave__nope");
  drain();
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].outcome, "thrown");
  assert.equal(rows[0].reasonCode, "internal");
  assert.equal(rows[0].errorKind, "Error");
  ok("thrown capture + throw unchanged");
}

// 2b. unknown-tool observation honors caller env + namespaced session.
{
  // Disabled via caller env (daemon per-client): records nothing, throw identical.
  freshDataDir("unknown-disabled");
  Terr.resetToolErrorStateForTests();
  let caught = null;
  try {
    await Dispatch.callTool("arbitrary-unknown", {}, {
      sessionId: SESSION,
      clientId: "daemon-conn",
      env: { CAVE_TOOLS_ERROR_LOG: "0" },
    });
  } catch (e) {
    caught = e;
  }
  assert.ok(caught instanceof Error);
  assert.equal(caught.message, "Unknown tool: arbitrary-unknown");
  drain();
  assert.equal(Terr.countToolErrors().total, 0);
  assert.equal(Terr.toolErrorQueueDepthForTests(), 0);
  ok("unknown tool + caller disable records nothing, throw unchanged");
}
{
  // Enabled: session hash matches the normal namespaced failure; caller
  // mode kept; secret name stays sanitized.
  freshDataDir("unknown-enabled");
  Terr.resetToolErrorStateForTests();
  const secretName = "cave__sk-ant-fake0000111122223333";
  const ctxOpts = { sessionId: SESSION, sessionNamespace: "cli-shared", env: { CAVE_TOOLS_MODE: "hint" } };
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-ns" }, ctxOpts);
  let caught = null;
  try {
    await Dispatch.callTool(secretName, {}, ctxOpts);
  } catch (e) {
    caught = e;
  }
  assert.ok(caught instanceof Error);
  assert.equal(caught.message, `Unknown tool: ${secretName}`);
  drain();
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 2);
  const byTool = Object.fromEntries(rows.map((r) => [r.tool, r]));
  assert.ok(byTool["cave__ls"] && byTool["unknown_tool"]);
  assert.equal(byTool["unknown_tool"].outcome, "thrown");
  assert.equal(byTool["unknown_tool"].reasonCode, "internal");
  assert.equal(byTool["unknown_tool"].sessionHash, byTool["cave__ls"].sessionHash, "same namespaced session");
  assert.equal(byTool["unknown_tool"].mode, "hint", "caller mode honored");
  assert.equal(byTool["cave__ls"].mode, "hint");
  assert.ok(!JSON.stringify(rows).includes("sk-ant-fake0000111122223333"), "secret name never in records");
  assert.ok(!dbBytes().includes("sk-ant-fake0000111122223333"), "secret name never in db bytes");
  ok("unknown tool honors namespace, caller env, name sanitized");
}

// 3. success records nothing.
{
  freshDataDir("success");
  Terr.resetToolErrorStateForTests();
  const r = await Dispatch.callTool("cave__ls", { path: ROOT }, { sessionId: SESSION });
  assert.ok(!r.isError);
  drain();
  assert.deepEqual(Terr.listToolErrors(10), []);
  assert.equal(Terr.countToolErrors().total, 0);
  ok("success records nothing");
}

// 4. secrets never persist: path value, mode value, tool name, kind, arg key.
{
  freshDataDir("secret-path");
  Terr.resetToolErrorStateForTests();
  const secret = "sk-ant-fake0123456789abcdef";
  await Dispatch.callTool("cave__read", { file_path: `/nope/${secret}/f.txt` }, { sessionId: SESSION });
  drain();
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 1);
  assert.match(rows[0].fileHash ?? "", /^[0-9a-f]{16}$/);
  assert.ok(!JSON.stringify(rows).includes(secret), "record JSON must not contain secret");
  assert.ok(!dbBytes().includes(secret), "db file bytes must not contain secret");
  ok("secret path isolation + hashed correlation");
}
{
  // Caller-controlled tool name and mode are sanitized, never stored raw.
  freshDataDir("secret-meta");
  Terr.resetToolErrorStateForTests();
  const secret = "sk-ant-fake9999888877776666";
  process.env.CAVE_TOOLS_MODE = secret;
  try {
    await Dispatch.callTool("cave__ls", { path: "/nonexistent-meta" }, { sessionId: SESSION });
  } finally {
    delete process.env.CAVE_TOOLS_MODE;
  }
  let caught = null;
  try {
    await Dispatch.callTool(`cave__${secret}`, {}, { sessionId: SESSION });
  } catch (e) {
    caught = e;
  }
  assert.ok(caught instanceof Error);
  Terr.observeToolCall({
    tool: "cave__ls",
    args: { [secret]: "v", path: "x" },
    schemaProps: ["path"],
    sessionId: SESSION,
    startedAt: Date.now(),
    thrown: { name: secret, message: secret },
  });
  drain();
  const rows = Terr.listToolErrors(20);
  assert.ok(rows.length >= 2, `expected >=2 rows, got ${rows.length}`);
  const dump = JSON.stringify(rows);
  assert.ok(!dump.includes(secret), "records must not contain secret tool/mode/kind/key");
  assert.ok(!dbBytes().includes(secret), "db bytes must not contain secret");
  assert.ok(rows.every((r) => r.tool === "cave__ls" || r.tool === "unknown_tool"));
  assert.ok(rows.every((r) => ["default", "off", "hint", "enforce", "strict"].includes(r.mode)));
  assert.ok(rows.every((r) => r.errorKind === null || r.errorKind === "Error"));
  ok("tool/mode/kind/argkey sanitization");
}
{
  // Per-call disable flag respects caller env (daemon path), not just process env.
  freshDataDir("percall-disable");
  Terr.resetToolErrorStateForTests();
  Terr.observeToolCall({
    tool: "cave__ls",
    args: { path: "/x" },
    schemaProps: ["path"],
    sessionId: SESSION,
    startedAt: Date.now(),
    env: { CAVE_TOOLS_ERROR_LOG: "0" },
    result: { content: [{ type: "text", text: "Path not found" }], isError: true },
  });
  drain();
  assert.equal(Terr.countToolErrors().total, 0);
  assert.equal(Terr.toolErrorQueueDepthForTests(), 0);
  ok("per-call CAVE_TOOLS_ERROR_LOG=0 disables");
}

// 5. disabled via process env.
{
  freshDataDir("disabled");
  Terr.resetToolErrorStateForTests();
  process.env.CAVE_TOOLS_ERROR_LOG = "0";
  const r = await Dispatch.callTool("cave__ls", { path: "/nonexistent-xyz-123" }, { sessionId: SESSION });
  assert.equal(r.isError, true, "tool behavior unchanged when logging disabled");
  drain();
  assert.equal(Terr.countToolErrors().total, 0);
  delete process.env.CAVE_TOOLS_ERROR_LOG;
  Terr.resetToolErrorStateForTests();
  ok("disabled via CAVE_TOOLS_ERROR_LOG=0");
}

// 6. retention: rows older than 7 days pruned on next flush.
{
  freshDataDir("retention");
  Terr.resetToolErrorStateForTests();
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-a" }, { sessionId: SESSION });
  drain();
  assert.equal(Terr.countToolErrors().total, 1);
  const db = new Database(join(process.env.CAVE_TOOLS_DATA_DIR, "cave-tools.db"));
  db.prepare("UPDATE tool_errors SET created_at = ?").run(Date.now() - 8 * 24 * 60 * 60 * 1000);
  db.close();
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-b" }, { sessionId: SESSION });
  drain();
  assert.equal(Terr.countToolErrors().total, 1, "old row pruned, new row kept");
  ok("7-day retention pruning");
}

// 7. cap: store never exceeds 5000 rows.
{
  freshDataDir("cap");
  Terr.resetToolErrorStateForTests();
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-cap" }, { sessionId: SESSION });
  drain();
  const db = new Database(join(process.env.CAVE_TOOLS_DATA_DIR, "cave-tools.db"));
  const now = Date.now();
  const tx = db.transaction(() => {
    const stmt = db.prepare(
      "INSERT OR IGNORE INTO tool_errors (id, created_at, tool, outcome, reason_code, elapsed_ms, mode, agent, session_hash, arg_keys, arg_types) VALUES (?, ?, 'cave__ls', 'soft', 'not_found', 1, 'default', 'unknown', 'aaaa', '[]', '{}')",
    );
    for (let i = 0; i < 5005; i++) stmt.run(`bulk-${i}`, now - i);
  });
  tx();
  db.close();
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-cap2" }, { sessionId: SESSION });
  drain();
  const total = Terr.countToolErrors().total;
  assert.ok(total <= 5000, `total ${total} exceeds cap`);
  ok(`5000-row cap enforced (total=${total})`);
}

// 8a. unwritable store isolation: calls still succeed.
{
  const file = join(SCRATCH, "not-a-dir");
  writeFileSync(file, "x");
  process.env.CAVE_TOOLS_DATA_DIR = file;
  Terr.resetToolErrorStateForTests();
  const r = await Dispatch.callTool("cave__ls", { path: "/nonexistent-unwritable" }, { sessionId: SESSION });
  assert.equal(r.isError, true, "call succeeds despite unwritable store");
  drain();
  Terr.resetToolErrorStateForTests();
  ok("unwritable store isolation");
}

// 8b. locked store initialization: zero lock wait, call succeeds fast.
{
  const dir = freshDataDir("busy-init");
  Terr.resetToolErrorStateForTests();
  const lock = new Database(join(dir, "cave-tools.db"));
  lock.exec("BEGIN EXCLUSIVE");
  const start = Date.now();
  const r = await Dispatch.callTool("cave__ls", { path: "/nonexistent-busy-init" }, { sessionId: SESSION });
  const elapsed = Date.now() - start;
  assert.equal(r.isError, true, "call succeeds while store is locked");
  assert.ok(elapsed < 2000, `logging blocked the call for ${elapsed}ms`);
  lock.exec("ROLLBACK");
  lock.close();
  drain();
  assert.equal(Terr.countToolErrors().total, 1, "deferred row flushed after unlock");
  ok("locked init: zero wait + deferred flush");
}

// 8c. queue bound under a locked store: oldest dropped, no spin.
{
  const dir = freshDataDir("queue");
  Terr.resetToolErrorStateForTests();
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-q0" }, { sessionId: SESSION });
  drain();
  const lock2 = new Database(join(dir, "cave-tools.db"));
  lock2.exec("BEGIN EXCLUSIVE");
  const soft = { content: [{ type: "text", text: "Path not found" }], isError: true };
  for (let i = 0; i < 300; i++) {
    Terr.observeToolCall({
      tool: "cave__ls",
      args: { path: `/q${i}` },
      schemaProps: ["path", "limit", "offset"],
      sessionId: SESSION,
      startedAt: Date.now(),
      result: soft,
    });
  }
  // Give scheduled microtasks a chance to (harmlessly) attempt flushes.
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(Terr.toolErrorQueueDepthForTests(), 256);
  lock2.exec("ROLLBACK");
  lock2.close();
  drain();
  assert.equal(Terr.toolErrorQueueDepthForTests(), 0, "queue drains after unlock");
  assert.ok(Terr.countToolErrors().total >= 200, "overflow rows persisted");
  ok("bounded queue (256) under locked store");
}

// 8d. no cross-contamination when the data dir changes with queued events.
{
  const dirA = freshDataDir("xdir-a");
  Terr.resetToolErrorStateForTests();
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-xa" }, { sessionId: SESSION });
  const dirB = freshDataDir("xdir-b");
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-xb" }, { sessionId: SESSION });
  drain();
  const countA = new Database(join(dirA, "cave-tools.db")).prepare("SELECT COUNT(*) AS n FROM tool_errors").get().n;
  const countB = new Database(join(dirB, "cave-tools.db")).prepare("SELECT COUNT(*) AS n FROM tool_errors").get().n;
  assert.equal(countA, 1, "dir A keeps its own event");
  assert.equal(countB, 1, "dir B keeps its own event");
  ok("no cross-dir contamination for queued events");
}

// 8e. owner-only files under a permissive umask; symlink db refused.
{
  const prev = process.umask(0o022);
  const dir = freshDataDir("perms");
  Terr.resetToolErrorStateForTests();
  try {
    await Dispatch.callTool("cave__ls", { path: "/nonexistent-perms" }, { sessionId: SESSION });
    drain();
    assert.equal(Terr.countToolErrors().total, 1);
    assert.equal(statSync(dir).mode & 0o777, 0o700, "data dir 0700");
    assert.equal(statSync(join(dir, "cave-tools.db")).mode & 0o777, 0o600, "db 0600");
    for (const sidecar of ["cave-tools.db-wal", "cave-tools.db-shm"]) {
      const p = join(dir, sidecar);
      if (existsSync(p)) assert.equal(statSync(p).mode & 0o777, 0o600, `${sidecar} 0600`);
    }
  } finally {
    process.umask(prev);
  }
  ok("owner-only files under permissive umask");
}
{
  const dir = freshDataDir("symlink");
  Terr.resetToolErrorStateForTests();
  const target = join(dir, "real-target.db");
  writeFileSync(target, "x");
  symlinkSync(target, join(dir, "cave-tools.db"));
  assert.ok(lstatSync(join(dir, "cave-tools.db")).isSymbolicLink());
  const r = await Dispatch.callTool("cave__ls", { path: "/nonexistent-sym" }, { sessionId: SESSION });
  assert.equal(r.isError, true, "call succeeds with symlinked db");
  drain();
  assert.equal(readFileSync(target, "utf8"), "x", "symlink target untouched");
  Terr.resetToolErrorStateForTests();
  ok("symlink db refused, target untouched");
}

// 9. classifier units with real edit strings (prose never stored).
{
  const c = Terr.classifySoftReasonCode;
  assert.equal(c("cave__ls", "Path not found or not a directory: /x"), "not_found");
  assert.equal(c("cave__websearch", "Error: query is required"), "invalid_args");
  assert.equal(c("cave__read", "Offset 5 is out of range"), "invalid_args");
  assert.equal(c("cave__bash", "out\n\n[exit: 1]"), "exec_failed");
  assert.equal(c("cave__webfetch", "Error: Request failed with status 500"), "upstream");
  assert.equal(c("cave__read", "Error: File is not valid UTF-8: f"), "unreadable");
  // Real edit strings.
  assert.equal(
    c("cave__edit", "Error: expected_hash mismatch — file changed since read. Re-run cave__read."),
    "stale_hash",
  );
  assert.equal(
    c("cave__edit", "Error: expected_range_checksum mismatch for lines 3-5. Re-read with cave__read."),
    "stale_range",
  );
  assert.equal(c("cave__edit", "Error: edits[2]: checksum failed"), "stale_range");
  assert.equal(c("cave__edit", "Error: edits[0]: hash failed"), "stale_hash");
  assert.equal(c("cave__edit", "Error: old_string is not unique (2+ matches). Add more context."), "not_unique");
  assert.equal(c("cave__edit", "Error: edits[4]: old_string is not unique after prior edits."), "not_unique");
  assert.equal(c("cave__edit", "Error: edits overlap. Merge them into one edit."), "overlap");
  assert.equal(c("cave__edit", "Error: cannot mix move and range edits in one batch."), "overlap");
  assert.equal(
    c("cave__edit", "Error: old_string not found — new_string already present, edit already applied?"),
    "not_found",
  );
  assert.equal(c("cave__edit", "Error: edits[1]: old_string not found in file."), "not_found");
  assert.equal(c("cave__edit", "Error: file_path is required"), "invalid_args");
  assert.equal(c("cave__edit", "Error: edits array is empty"), "invalid_args");
  assert.equal(c("cave__edit", "Error: expected_hash must be at least 8 hex characters"), "invalid_args");
  assert.equal(c("cave__edit", "Error: edits[1]: start_line and end_line must be integers"), "invalid_args");
  // Anchored prefix wins over a content window mentioning something else.
  assert.equal(
    c(
      "cave__edit",
      "Error: expected_hash mismatch — file changed.\nexpected: abcd\nactual: ef12\nnote: file not found fallback",
    ),
    "stale_hash",
  );
  assert.equal(c("cave__ls", ""), "unknown");
  assert.equal(c("cave__ls", "Error: something totally novel"), "unknown");
  // Batch index only from the anchored generated prefix.
  assert.equal(Terr.parseBatchIndex("Error: edits[12]: checksum failed"), 12);
  assert.equal(Terr.parseBatchIndex("edits[0]: hash failed"), 0);
  assert.equal(Terr.parseBatchIndex("note: see edits[3] later"), null);
  assert.equal(Terr.parseBatchIndex(""), null);
  // Thrown classifier.
  assert.equal(Terr.classifyThrownReasonCode("cave__edit", "TypeError", "Cannot read properties of undefined (reading 'edits')"), "invalid_args");
  assert.equal(Terr.classifyThrownReasonCode("cave__x", "Error", "Unknown tool: cave__x"), "internal");
  assert.equal(Terr.classifyThrownReasonCode("cave__webfetch", "CloudflareChallengeError", "challenge"), "upstream");
  assert.equal(Terr.classifyThrownReasonCode("cave__x", "Error", "File changed after it was read: f"), "stale_write");
  assert.equal(Terr.classifyThrownReasonCode("cave__bash", "Error", "weird boom"), "internal");
  assert.equal(Terr.normalizeThrownKind("TypeError"), "TypeError");
  assert.equal(Terr.normalizeThrownKind("WeirdCustom"), "Error");
  // Tool/mode sanitization.
  assert.equal(Terr.sanitizeToolName("cave__ls"), "cave__ls");
  assert.equal(Terr.sanitizeToolName("cave__evil"), "unknown_tool");
  assert.equal(Terr.sanitizeToolName(123), "unknown_tool");
  assert.equal(Terr.sanitizeMode("enforce"), "enforce");
  assert.equal(Terr.sanitizeMode("weird"), "default");
  assert.equal(Terr.sanitizeMode(undefined), "default");
  const s = Terr.summarizeArgs({ file_path: "f", limit: 3, bogus: 1, __sessionId: "s" }, ["file_path", "limit"]);
  assert.deepEqual(s.keys, ["file_path", "limit"]);
  assert.deepEqual(s.types, { file_path: "string", limit: "number" });
  assert.equal(s.count, 3);
  assert.equal(s.unknownCount, 1);
  assert.equal(Terr.deriveOperation("cave__edit", { edits: [] }), "batch");
  assert.equal(Terr.deriveOperation("cave__read", { offset: 2 }), "paged");
  assert.equal(Terr.deriveOperation("cave__ls", { path: "x" }), null);
  ok("classifier + sanitizer + arg summary units");
}

// 10. batch index flows from the anchored prefix into the stored row.
{
  freshDataDir("batchidx");
  Terr.resetToolErrorStateForTests();
  Terr.observeToolCall({
    tool: "cave__edit",
    args: { file_path: "f", edits: [{}, {}, {}] },
    schemaProps: ["file_path", "edits"],
    sessionId: SESSION,
    startedAt: Date.now(),
    result: {
      content: [{ type: "text", text: "Error: edits[2]: old_string is not unique (2+ matches)." }],
      isError: true,
    },
  });
  drain();
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].reasonCode, "not_unique");
  assert.equal(rows[0].batchIndex, 2);
  assert.equal(rows[0].operation, "batch");
  ok("anchored batch index stored, prose not");
}

// 11. CLI reader: empty, populated (event id shown), strict rejections.
{
  freshDataDir("cli");
  Terr.resetToolErrorStateForTests();
  let out = "";
  const origLog = console.log;
  console.log = (...a) => {
    out += `${a.join(" ")}\n`;
  };
  try {
    assert.equal(await TerrCli.runErrorsCommand([]), 0);
  } finally {
    console.log = origLog;
  }
  assert.ok(out.includes("No tool errors recorded."), `empty output:\n${out}`);
  await Dispatch.callTool("cave__ls", { path: "/nonexistent-cli" }, { sessionId: SESSION });
  drain();
  out = "";
  console.log = (...a) => {
    out += `${a.join(" ")}\n`;
  };
  let code = -1;
  try {
    code = await TerrCli.runErrorsCommand(["--limit", "5"]);
  } finally {
    console.log = origLog;
  }
  assert.equal(code, 0);
  assert.ok(out.includes("tool=cave__ls"), `cli output:\n${out}`);
  assert.ok(out.includes("by code:"), `cli counts:\n${out}`);
  assert.ok(out.match(/id=[0-9a-f-]{36}/), "event id shown for correlation");
  // Strict rejections run in children (usage exits the process).
  const cliRunner = join(SCRATCH, "tool-errors-cli-run.mjs");
  writeFileSync(
    cliRunner,
    `import { runErrorsCommand } from ${JSON.stringify(pathToFileURL(join(DIST, "runtime", "tool-errors-cli.js")).href)};\nconst code = await runErrorsCommand(JSON.parse(process.argv[2]));\nprocess.exit(code);\n`,
  );
  for (const bad of [["--limit", "0"], ["--limit", "2.5"], ["--limit", "abc"], ["--bogus"], ["extra"], ["--limit"]]) {
    const child = spawnSync(process.execPath, [cliRunner, JSON.stringify(bad)], {
      encoding: "utf8",
    });
    assert.equal(child.status, 2, `args ${JSON.stringify(bad)} must exit 2 (got ${child.status}: ${child.stderr})`);
  }
  ok("CLI reader (empty + populated + strict rejections)");
}

assert.equal(Terr.TOOL_ERROR_QUEUE_CAP, 256);
assert.equal(Terr.TOOL_ERROR_MAX_ROWS, 5000);
assert.equal(Terr.TOOL_ERROR_RETENTION_DAYS, 7);

// A. sink deferred past observer return; setImmediate turns, never microtasks.
{
  freshDataDir("defer");
  Terr.resetToolErrorStateForTests();
  Terr.observeToolCall({
    tool: "cave__ls",
    args: { path: "/x" },
    schemaProps: ["path"],
    sessionId: SESSION,
    startedAt: Date.now(),
    result: { content: [{ type: "text", text: "Path not found" }], isError: true },
  });
  assert.equal(Terr.toolErrorQueueDepthForTests(), 1, "queued, sink not yet run");
  assert.equal(Terr.countToolErrors().total, 0, "no write before observer returns");
  for (let i = 0; i < 20; i++) await Promise.resolve();
  assert.equal(Terr.toolErrorQueueDepthForTests(), 1, "microtasks must never drain");
  assert.equal(Terr.countToolErrors().total, 0, "no write on microtask turns");
  for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r));
  assert.equal(Terr.countToolErrors().total, 1, "event-loop turn flushed");
  assert.equal(Terr.toolErrorQueueDepthForTests(), 0);
  ok("sink deferred past observer return; no microtask saturation");
}
{
  // 300 events, microtask rounds only: nothing flushes until a loop turn.
  freshDataDir("nosat");
  Terr.resetToolErrorStateForTests();
  const soft = { content: [{ type: "text", text: "Path not found" }], isError: true };
  for (let i = 0; i < 300; i++) {
    Terr.observeToolCall({
      tool: "cave__ls",
      args: { path: `/n${i}` },
      schemaProps: ["path"],
      sessionId: SESSION,
      startedAt: Date.now(),
      result: soft,
    });
  }
  for (let i = 0; i < 20; i++) await Promise.resolve();
  assert.equal(Terr.toolErrorQueueDepthForTests(), 256, "cap holds, no microtask drain");
  Terr.flushToolErrorQueue();
  assert.equal(Terr.countToolErrors().total, 256);
  ok("bounded per-turn batches; explicit drain complete");
}

// B. dangling symlink db: target must never be created.
{
  const dir = freshDataDir("dangling");
  Terr.resetToolErrorStateForTests();
  const ghost = join(dir, "no-such-target.db");
  symlinkSync(ghost, join(dir, "cave-tools.db"));
  const r = await Dispatch.callTool("cave__ls", { path: "/nonexistent-dangle" }, { sessionId: SESSION });
  assert.equal(r.isError, true, "call succeeds with dangling db symlink");
  Terr.flushToolErrorQueue();
  assert.ok(!existsSync(ghost), "dangling target must remain nonexistent");
  assert.equal(Terr.countToolErrors().total, 0);
  Terr.resetToolErrorStateForTests();
  ok("dangling symlink db refused, target never created");
}

// C. thrown null/undefined recorded by presence; values untouched.
{
  freshDataDir("nullthrow");
  Terr.resetToolErrorStateForTests();
  const base = { tool: "cave__ls", args: { path: "/x" }, schemaProps: ["path"], sessionId: SESSION, startedAt: Date.now() };
  Terr.observeToolCall({ ...base, thrown: null });
  Terr.observeToolCall({ ...base, args: { path: "/y" }, thrown: undefined });
  Terr.observeToolCall({
    ...base,
    args: { path: "/z" },
    result: { content: [{ type: "text", text: "ok" }] },
  });
  drain();
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 2, "both null and undefined throws recorded; success ignored");
  assert.ok(rows.every((r) => r.outcome === "thrown" && r.errorKind === "Error"));
  ok("thrown null/undefined recorded by presence, values preserved");
}

// D. disable matrix: caller-off drops, global-off drops, else records.
{
  const mk = (env) => ({
    tool: "cave__ls",
    args: { path: "/m" },
    schemaProps: ["path"],
    sessionId: SESSION,
    startedAt: Date.now(),
    ...(env === undefined ? {} : { env }),
    result: { content: [{ type: "text", text: "Path not found" }], isError: true },
  });
  delete process.env.CAVE_TOOLS_ERROR_LOG;
  freshDataDir("mx-on"); Terr.resetToolErrorStateForTests();
  Terr.observeToolCall(mk(undefined)); drain();
  assert.equal(Terr.countToolErrors().total, 1, "proc on + caller unset records");
  freshDataDir("mx-calloff"); Terr.resetToolErrorStateForTests();
  Terr.observeToolCall(mk({ CAVE_TOOLS_ERROR_LOG: "0" })); drain();
  assert.equal(Terr.countToolErrors().total, 0, "caller disable wins over proc on");
  assert.equal(Terr.toolErrorQueueDepthForTests(), 0, "disabled call never queued");
  freshDataDir("mx-procoff"); Terr.resetToolErrorStateForTests();
  process.env.CAVE_TOOLS_ERROR_LOG = "0";
  Terr.observeToolCall(mk(undefined)); drain();
  assert.equal(Terr.countToolErrors().total, 0, "global disable drops");
  freshDataDir("mx-both"); Terr.resetToolErrorStateForTests();
  Terr.observeToolCall(mk({ CAVE_TOOLS_ERROR_LOG: "1" })); drain();
  assert.equal(Terr.countToolErrors().total, 0, "global disable wins over caller on");
  delete process.env.CAVE_TOOLS_ERROR_LOG;
  ok("disable matrix: caller-off and global-off drop, else records");
}

// E. corrupt store reads empty; reader never leaks the handle.
{
  freshDataDir("corrupt");
  Terr.resetToolErrorStateForTests();
  writeFileSync(join(process.env.CAVE_TOOLS_DATA_DIR, "cave-tools.db"), "not a database at all");
  assert.deepEqual(Terr.listToolErrors(5), []);
  assert.equal(Terr.countToolErrors().total, 0);
  assert.deepEqual(Terr.listToolErrors(5), [], "repeat reads stay clean");
  ok("corrupt store reads empty without leaking");
}

// F. short-lived CLI persists its last failure through the exit funnel.
{
  const dir = freshDataDir("cli-flush");
  Terr.resetToolErrorStateForTests();
  const child = spawnSync(process.execPath, [join(DIST, "cli.js"), "ls", "/nonexistent-cliflush"], {
    encoding: "utf8",
    cwd: ROOT,
    env: { ...process.env, CAVE_TOOLS_DATA_DIR: dir },
  });
  assert.equal(child.status, 1, `cli exits 1 on soft error (got ${child.status}: ${child.stderr})`);
  const rows = Terr.listToolErrors(10);
  assert.equal(rows.length, 1, "last failure persisted despite process.exit");
  assert.equal(rows[0].tool, "cave__ls");
  assert.equal(rows[0].reasonCode, "not_found");
  ok("CLI exit funnel flushes last failure");
}

rmSync(SCRATCH, { recursive: true, force: true });
console.log(`\nAll ${pass} tool-error checks passed.`);
