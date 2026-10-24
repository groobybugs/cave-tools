#!/usr/bin/env node
// Regression tests for TypeScript bugs found while porting cave-tools to Go.
// Imports dist/; run after `pnpm run build`.

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const work = mkdtempSync(join(process.env.CAVE_TOOLS_DATA_DIR || tmpdir(), "port-bugs-"));
const dataDir = join(work, "data");
mkdirSync(join(dataDir, "sessions"), { recursive: true });
process.env.CAVE_TOOLS_DATA_DIR = dataDir;

// Legacy session file without budgets/rtk/bounces/byTool and a null leaf.
writeFileSync(
  join(dataDir, "sessions", "4242.json"),
  JSON.stringify({ pid: 4242, sessionStart: 1, updatedAt: 2, cache: { hits: 1, misses: null }, savings: { totalCalls: 3 } }),
);

const db = await import(join(ROOT, "dist/storage/db.js"));
const jobs = await import(join(ROOT, "dist/runtime/jobs.js"));
const { archiveIfLarge } = await import(join(ROOT, "dist/compression/archive.js"));
const { realPathSafe } = await import(join(ROOT, "dist/runtime/path.js"));
const { binDir } = await import(join(ROOT, "dist/runtime/bins.js"));
const { rgGrep } = await import(join(ROOT, "dist/runtime/ripgrep.js"));
const { extractSignatures } = await import(join(ROOT, "dist/compression/signatures.js"));
const { classifyCommand } = await import(join(ROOT, "dist/compression/classify.js"));
const { redactSecrets } = await import(join(ROOT, "dist/compression/redact.js"));
const { runWithContext } = await import(join(ROOT, "dist/runtime/context.js"));
const { webfetchTool } = await import(join(ROOT, "dist/tools/webfetch.js"));
const { editTool } = await import(join(ROOT, "dist/tools/edit.js"));

let passed = 0;
let failed = 0;
async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ok  ${name}`);
  } catch (error) {
    failed++;
    console.log(`  FAIL ${name}\n       ${error.message.split("\n").join("\n       ")}`);
  }
}
const ctx = (cwd, env) => ({ sessionId: "port-bugs", cwd, env, clientId: "test" });
async function renderHtml(html, format) {
  const server = createServer((_req, res) => {
    res.setHeader("content-type", "text/html; charset=utf-8");
    res.end(html);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const { port } = server.address();
    const result = await webfetchTool.handler({
      url: `http://127.0.0.1:${port}/fixture`,
      format,
      redact_secrets: false,
    });
    assert.equal(result.isError, undefined, result.content[0].text);
    return result.content[0].text;
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}
const job = (over) => ({
  jobId: `pb-${Math.random().toString(36).slice(2)}`,
  pid: 999999,
  sessionId: "port-bugs",
  command: "true",
  workdir: null,
  startedAt: 1,
  endedAt: 10,
  exitCode: 5,
  signal: "SIGTERM",
  state: "exited",
  logPath: join(work, "missing.log"),
  ...over,
});

console.log("port bugs");

await test("1 legacy session without budgets/rtk/bounces migrates", () => {
  const row = db.listSessionStats(true).find((s) => s.pid === 4242);
  assert.ok(row, "legacy session 4242 was dropped");
  assert.deepEqual(row.budgets, {});
  assert.equal(row.cache.hits, 1);
  assert.equal(row.cache.misses, 0);
  assert.equal(row.savings.totalCalls, 3);
  assert.equal(row.rtk.rewrites, 0);
  assert.equal(row.bounces.totalBounces, 0);
});

await test("2 updateJobState state-only keeps exit code, signal, ended_at", () => {
  const rec = job();
  db.insertJob(rec);
  db.updateJobState(rec.jobId, { state: "lost" });
  const got = db.getJob(rec.jobId);
  assert.equal(got.state, "lost");
  assert.equal(got.exitCode, 5);
  assert.equal(got.signal, "SIGTERM");
  assert.equal(got.endedAt, 10);
});

await test("3 getJobInfo lost result matches stored row", () => {
  const rec = job({ state: "running", endedAt: null, exitCode: 7, signal: "SIGHUP" });
  db.insertJob(rec);
  const info = jobs.getJobInfo(rec.jobId);
  assert.equal(info.state, "lost");
  assert.deepEqual(info, db.getJob(rec.jobId));
});

await test("4 job worker spawn failure exits 127", () => {
  const log = join(work, "spawnfail.log");
  const res = spawnSync(process.execPath, [
    join(ROOT, "dist/runtime/job-worker.js"), "pb-spawn", log, join(work, "spawnfail.json"), "/nonexistent/shell", "echo hi",
  ]);
  assert.equal(res.status, 127);
  assert.match(readFileSync(log, "utf-8"), /\[cave-job exit=127\]/);
  assert.equal(JSON.parse(readFileSync(join(work, "spawnfail.json"), "utf-8")).exitCode, 127);
});

await test("5 archive summary never prints negative line count", async () => {
  const one = await archiveIfLarge("x".repeat(60000), "one-line");
  assert.ok(one);
  assert.doesNotMatch(one.summary, /-\d+ lines archived/);
  assert.match(one.summary, /chars archived/);
  const many = await archiveIfLarge(Array.from({ length: 10000 }, (_, i) => `line ${i}`).join("\n"), "many");
  assert.match(many.summary, /\(9970 lines archived\)/);
});

await test("6 realPathSafe resolves existing relative path against call cwd", async () => {
  const dir = join(work, "cwd6");
  mkdirSync(dir, { recursive: true });
  // "src" exists in the server's process.cwd() (the repo) but not in dir.
  const server = await realPathSafe(join(ROOT, "src"));
  assert.ok(existsSync(server));
  const prev = process.cwd();
  process.chdir(ROOT);
  try {
    const got = await runWithContext(ctx(dir, process.env), () => realPathSafe("src"));
    assert.equal(got, join(dir, "src"));
    mkdirSync(join(dir, "sub"));
    assert.equal(await runWithContext(ctx(dir, process.env), () => realPathSafe("sub")), join(dir, "sub"));
  } finally {
    process.chdir(prev);
  }
  const missing = await runWithContext(ctx(dir, process.env), () => realPathSafe("nope"));
  assert.equal(missing, join(dir, "nope"));
});

await test("7 binDir uses call env", () => {
  const home = join(work, "home7");
  const got = runWithContext(ctx(work, { ...process.env, HOME: home, USERPROFILE: home, LOCALAPPDATA: join(home, "AppData", "Local") }), () => binDir());
  assert.ok(got.startsWith(home), `${got} not under ${home}`);
});

// Fake rg on the call PATH: emits a match without line_number, and a
// multibyte char split across two writes.
const fakeBin = join(work, "bin");
mkdirSync(fakeBin);
const fakeRg = join(fakeBin, "rg");
writeFileSync(
  fakeRg,
  `#!${process.execPath}
const full = Buffer.from(JSON.stringify({ type: "match", data: { path: { text: "a.txt" }, lines: { text: "héllo ✓\\n" } } }) + "\\n");
const cut = full.indexOf(Buffer.from("✓")) + 1;
process.stdout.write(full.subarray(0, cut));
setTimeout(() => { process.stdout.write(full.subarray(cut)); }, 50);
`,
);
chmodSync(fakeRg, 0o755);
const rgEnv = { ...process.env, PATH: `${fakeBin}:${process.env.PATH}` };

await test("8 rg match without line_number gives line 0, not NaN", async () => {
  const { matches } = await runWithContext(ctx(work, rgEnv), () => rgGrep({ cwd: work, pattern: "h", limit: 10 }));
  assert.equal(matches.length, 1);
  assert.equal(matches[0].line, 0);
});

await test("12 rg stdout keeps multibyte char split across chunks", async () => {
  const { matches } = await runWithContext(ctx(work, rgEnv), () => rgGrep({ cwd: work, pattern: "h", limit: 10 }));
  assert.equal(matches[0].text, "héllo ✓");
});

await test("9 arrow functions are fn signatures; async arrows marked async", () => {
  const sigs = extractSignatures("export const f = async (a: number): Promise<void> => {}\nconst g = (b) => b\nconst h = 1\n", ".ts");
  const [f, g, h] = sigs;
  assert.equal(f.kind, "fn");
  assert.equal(f.name, "f");
  assert.equal(f.isAsync, true);
  assert.equal(f.params, "a: number");
  assert.equal(g.kind, "fn");
  assert.equal(g.isAsync, false);
  assert.equal(h.kind, "const");
});

await test("10 aws --output json is verbatim", () => {
  assert.equal(classifyCommand("aws s3api list-buckets --output json"), "verbatim");
  assert.equal(classifyCommand("aws s3 ls"), "compressible");
});

await test("11 redaction keeps non-secret delimiters", () => {
  assert.equal(
    redactSecrets("postgres://user:hunter2pass@db.local/x"),
    "postgres://user:[REDACTED:Database URL]@db.local/x",
  );
  const key = "-----BEGIN RSA PRIVATE KEY-----\nMIIEsecret\n-----END RSA PRIVATE KEY-----";
  const out = redactSecrets(key);
  assert.equal(out, "-----BEGIN RSA PRIVATE KEY-----[REDACTED:Private key block]-----END RSA PRIVATE KEY-----");
  assert.doesNotMatch(out, /MIIEsecret|hunter2/);
});

await test("13 daemon control skips a malformed reply line instead of crashing", () => {
  // Fake daemon on the real socket path: one garbage line, then the hello reply.
  const script = `
    const net = await import("node:net");
    const fs = await import("node:fs");
    const paths = await import(${JSON.stringify(join(ROOT, "dist/daemon/paths.js"))});
    const control = await import(${JSON.stringify(join(ROOT, "dist/daemon/control.js"))});
    paths.ensureSocketDir();
    const sock = paths.socketPath();
    try { fs.unlinkSync(sock); } catch {}
    const server = net.createServer((c) => c.on("data", () => {
      c.write("not json\\n" + JSON.stringify({ id: 1, result: { pid: 42, buildId: "b" } }) + "\\n");
    }));
    await new Promise((r) => server.listen(sock, r));
    const code = await control.daemonStatus();
    server.close();
    process.exit(code);
  `;
  const r = spawnSync(process.execPath, ["--input-type=module", "-e", script], { encoding: "utf8", env: process.env });
  assert.equal(r.status, 0, r.stderr || r.stdout);
  assert.match(r.stdout, /running pid 42/);
});

const stats = await import(join(ROOT, "dist/compression/utils.js"));

await test("14 buildStats reports the given bucket's bounces", () => {
  runWithContext({ ...ctx(work, process.env), clientPid: 777 }, () => {
    stats.recordRead("/tmp/pb14.ts", true, 100);
    stats.recordRead("/tmp/pb14.ts", false, 500);
  });
  // Written from the local context, whose bucket has no bounces.
  stats.releaseClientStats(777);
  const row = db.listSessionStats(true).find((r) => r.pid === 777);
  assert.ok(row, "client row written");
  assert.equal(row.bounces.totalBounces, 1);
  assert.ok(row.bounces.byExtension[".ts"], "per-extension bounce kept");
});

await test("15 resetStats clears range-edit counters", async () => {
  stats.recordRangeEchoSaved(1234);
  assert.ok(stats.getRangeEditStats().edits > 0);
  await stats.resetStats();
  assert.deepEqual(stats.getRangeEditStats(), { edits: 0, echoSavedChars: 0, estimatedTokensSaved: 0 });
});

await test("16 webfetch markdown renders list markers and blockquote prefixes", async () => {
  const html = "<ul><li>one</li><li>two</li></ul><ol><li>first</li><li>second</li></ol><blockquote>quoted</blockquote>";
  assert.equal(
    await renderHtml(html, "markdown"),
    "- one\n\n- two\n\n1. first\n\n2. second\n\n> quoted",
  );
  assert.equal(await renderHtml(html, "text"), "one\ntwo\nfirst\nsecond\nquoted");
});

await test("17 webfetch markdown treats inherited object keys as ordinary tags", async () => {
  const html = "<constructor>Alpha</constructor><toString>Beta</toString><h2>Real</h2>";
  assert.equal(await renderHtml(html, "markdown"), "AlphaBeta\n\n## Real");
  assert.equal(await renderHtml(html, "text"), "AlphaBetaReal");
});

const strictProofError = /STRICT: range\/move edit requires expected_hash or expected_range_checksum/;
const resultText = (result) => result.content.map((part) => part.text ?? "").join("\n");
async function inStrictMode(fn) {
  const previous = process.env.CAVE_TOOLS_MODE;
  process.env.CAVE_TOOLS_MODE = "strict";
  try {
    return await fn();
  } finally {
    if (previous === undefined) delete process.env.CAVE_TOOLS_MODE;
    else process.env.CAVE_TOOLS_MODE = previous;
  }
}
async function rejectsStrictProof(file, args, prefix = "") {
  const before = readFileSync(file, "utf8");
  const result = await inStrictMode(() => editTool.handler(args));
  assert.equal(result.isError, true, resultText(result));
  assert.match(resultText(result), strictProofError);
  if (prefix) assert.match(resultText(result), new RegExp(`Error: ${prefix}`));
  assert.equal(readFileSync(file, "utf8"), before);
}

await test("18 strict single range rejects null expected_hash", async () => {
  const file = join(work, "strict-single-range.txt");
  writeFileSync(file, "alpha\nbeta\n");
  await rejectsStrictProof(file, {
    file_path: file,
    start_line: 1,
    end_line: 1,
    content: "ALPHA",
    expected_hash: null,
  });
});

await test("19 strict single move rejects null expected_range_checksum", async () => {
  const file = join(work, "strict-single-move.txt");
  writeFileSync(file, "alpha\nbeta\n");
  await rejectsStrictProof(file, {
    file_path: file,
    start_line: 1,
    end_line: 1,
    insert_before: 3,
    expected_range_checksum: null,
  });
});

await test("20 strict batch range rejects null proof values", async () => {
  const file = join(work, "strict-batch-range.txt");
  writeFileSync(file, "alpha\nbeta\n");
  await rejectsStrictProof(file, {
    file_path: file,
    expected_hash: null,
    edits: [{ start_line: 1, end_line: 1, content: "ALPHA", expected_hash: null }],
  }, "edits\\[0\\]: STRICT");
});

await test("21 strict batch move rejects null expected_range_checksum", async () => {
  const file = join(work, "strict-batch-move.txt");
  writeFileSync(file, "alpha\nbeta\n");
  await rejectsStrictProof(file, {
    file_path: file,
    edits: [{ start_line: 1, end_line: 1, insert_before: 3, expected_range_checksum: null }],
  }, "edits\\[0\\]: STRICT");
});

console.log(`port-bugs: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
