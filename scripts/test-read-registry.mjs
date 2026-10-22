#!/usr/bin/env node
// Isolated integration/regression tests for the legacy flat read registry.
//
// What this covers:
//   - a successful built cave__read appends the exact absolute path before it
//     resolves (deterministic barrier for strict-mode built-in guards)
//   - a cache-hit stub also waits for a still-pending compatibility append
//   - repeated reads do not duplicate the line
//   - unrelated pre-existing lines survive, including a missing trailing LF
//   - a symlinked registry is refused and its target is untouched
//   - registry IO failures never fail a read
//   - paths containing CR/LF (and relative paths) are rejected
//   - concurrent processes registering different paths do not lose entries
//   - registry file/dir permissions are owner-only
//   - pre-existing permissive modes are tightened while seeding known paths
//   - a symlinked final registry directory is never traversed while seeding
//   - seed reads stay bounded when the registry exceeds the seed byte cap
//
// Every scenario runs in a child process with isolated CAVE_TOOLS_DATA_DIR and
// CLAUDE_CONFIG_DIR under /tmp/opencode. The caller's live registry, mode flag,
// and SQLite data are never touched.
//
// Requires a fresh build (`pnpm build`) because integration imports dist/.
// Exits non-zero on a stale or missing dist rather than testing old behavior.
//
// Usage: node scripts/test-read-registry.mjs
//        KEEP=1 node scripts/test-read-registry.mjs   # keep temp fixtures

import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, readFileSync, statSync } from "node:fs";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const SELF = fileURLToPath(import.meta.url);
const ROOT = dirname(dirname(SELF));
const DIST = join(ROOT, "dist");
const DIST_HELPER = join(DIST, "runtime", "read-registry.js");
const DIST_UTILS = join(DIST, "compression", "utils.js");
const DIST_READ = join(DIST, "tools", "read.js");
const SRC_HELPER = join(ROOT, "src", "runtime", "read-registry.ts");
const SRC_UTILS = join(ROOT, "src", "compression", "utils.ts");

let passed = 0;

function check(condition, message) {
  if (!condition) throw new Error(message);
  passed++;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function registryPath() {
  return join(process.env.CLAUDE_CONFIG_DIR, "cave-tools", "read-registry.txt");
}

function tryRead(filePath) {
  try {
    return readFileSync(filePath, "utf8");
  } catch {
    return null;
  }
}

function registryLines(body) {
  return body === null ? [] : body.split("\n").map((line) => line.trim()).filter(Boolean);
}

function emit(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

function loadBuilt(filePath) {
  return import(pathToFileURL(filePath).href);
}

async function runScenario(name) {
  const registry = registryPath();
  switch (name) {
    case "helper-once": {
      const { registerReadPathCompat } = await loadBuilt(DIST_HELPER);
      await registerReadPathCompat(process.env.TEST_FILE);
      emit({ body: tryRead(registry) });
      return;
    }
    case "helper-twice": {
      const { registerReadPathCompat } = await loadBuilt(DIST_HELPER);
      await registerReadPathCompat(process.env.TEST_FILE);
      const afterFirst = tryRead(registry);
      await registerReadPathCompat(process.env.TEST_FILE);
      emit({ afterFirst, afterSecond: tryRead(registry) });
      return;
    }
    case "helper-after-start": {
      const start = process.env.TEST_START;
      const deadline = Date.now() + 10_000;
      while (!existsSync(start)) {
        if (Date.now() > deadline) throw new Error("timed out waiting for start barrier");
        await delay(10);
      }
      const { registerReadPathCompat } = await loadBuilt(DIST_HELPER);
      await registerReadPathCompat(process.env.TEST_FILE);
      emit({ body: tryRead(registry) });
      return;
    }
    case "helper-invalid": {
      const { registerReadPathCompat } = await loadBuilt(DIST_HELPER);
      await registerReadPathCompat(`${process.env.TEST_BASE}/safe\n${process.env.TEST_BASE}/injected`);
      await registerReadPathCompat("relative/path.txt");
      emit({ body: tryRead(registry) });
      return;
    }
    case "helper-count": {
      const { registerReadPathCompat } = await loadBuilt(DIST_HELPER);
      await registerReadPathCompat(process.env.TEST_FILE);
      const lines = registryLines(tryRead(registry));
      emit({
        exactCount: lines.filter((line) => line === process.env.TEST_FILE).length,
        totalLines: lines.length,
      });
      return;
    }
    case "helper-cache-race": {
      const { updateFileCache, isFileUnchanged } = await loadBuilt(DIST_UTILS);
      const file = process.env.TEST_FILE;
      const content = readFileSync(file);
      const mtimeMs = statSync(file).mtimeMs;
      const hash = createHash("sha256").update(content).digest("hex");
      // Precomputed hash+mtime makes updateFileCache publish the entry before
      // its first await. Do not await it yet: isFileUnchanged must observe the
      // cache hit while the compatibility append is still pending.
      const pending = updateFileCache(file, "race", {
        content,
        hash,
        mtimeMs,
        deliveryKey: "race",
      });
      const hit = await isFileUnchanged(file, "race", "race");
      const bodyAfterHit = tryRead(registry);
      await pending;
      emit({ hit, bodyAfterHit, bodyAfterUpdate: tryRead(registry) });
      return;
    }
    case "read-once": {
      const { readTool } = await loadBuilt(DIST_READ);
      const result = await readTool.handler({
        file_path: process.env.TEST_FILE,
        __sessionId: "registry-test",
      });
      // Read the registry synchronously in the same turn the read resolved:
      // this is the strict-mode barrier being tested.
      emit({
        isError: result.isError === true,
        text: String(result.content?.[0]?.text ?? "").slice(0, 120),
        body: tryRead(registry),
      });
      return;
    }
    case "read-repeat": {
      const { readTool } = await loadBuilt(DIST_READ);
      const first = await readTool.handler({
        file_path: process.env.TEST_FILE,
        __sessionId: "registry-test",
      });
      const second = await readTool.handler({
        file_path: process.env.TEST_FILE,
        __sessionId: "registry-test",
        force: true,
      });
      emit({
        firstError: first.isError === true,
        secondError: second.isError === true,
        body: tryRead(registry),
      });
      return;
    }
    default:
      throw new Error(`unknown scenario: ${name}`);
  }
}

function runChild(scenario, env, startPath) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [SELF, "--scenario", scenario], {
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`scenario ${scenario} timed out\n${stderr}`));
    }, 30_000);
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      if (code !== 0) {
        reject(new Error(`scenario ${scenario} exited ${code ?? signal}\n${stderr}`));
        return;
      }
      const last = stdout.trim().split("\n").filter(Boolean).pop();
      if (!last) {
        reject(new Error(`scenario ${scenario} produced no JSON\n${stderr}`));
        return;
      }
      try {
        resolve(JSON.parse(last));
      } catch (error) {
        reject(new Error(`scenario ${scenario} bad JSON: ${error.message}\n${stdout}`));
      }
    });
    if (startPath) {
      writeFile(startPath, "go\n").catch(reject);
    }
  });
}

async function assertFreshDist() {
  for (const [src, dist] of [
    [SRC_HELPER, DIST_HELPER],
    [SRC_UTILS, DIST_UTILS],
  ]) {
    check(existsSync(dist), `missing ${dist} — run pnpm build before this test`);
    const srcStat = await stat(src);
    const distStat = await stat(dist);
    check(
      distStat.mtimeMs >= srcStat.mtimeMs,
      `stale ${dist} — rebuild before testing (source is newer)`,
    );
  }
  check(existsSync(DIST_READ), `missing ${DIST_READ} — run pnpm build before this test`);
}

async function freshDirs(name) {
  const root = join(TMP_ROOT, name);
  const dataDir = join(root, "data");
  const claudeDir = join(root, "claude");
  await mkdir(dataDir, { recursive: true });
  return {
    root,
    dataDir,
    claudeDir,
    registry: join(claudeDir, "cave-tools", "read-registry.txt"),
    env: {
      ...process.env,
      CAVE_TOOLS_DAEMON: "0",
      CAVE_TOOLS_DATA_DIR: dataDir,
      CLAUDE_CONFIG_DIR: claudeDir,
    },
  };
}

async function testHelperDedupPermissionsAndBarrier() {
  const dirs = await freshDirs("helper-basic");
  const file = join(dirs.root, "target.txt");
  await writeFile(file, "hello\nworld\n");

  const result = await runChild("helper-twice", { ...dirs.env, TEST_FILE: file });
  check(result.afterFirst !== null, "barrier: registry must exist when the awaited registration resolves");
  check(registryLines(result.afterFirst).includes(file), "barrier: exact path must be visible immediately after await");
  check(registryLines(result.afterFirst).length === 1, "first registration must write exactly one line");
  check(registryLines(result.afterSecond).length === 1, "repeated registration must not duplicate");
  check(registryLines(result.afterSecond)[0] === file, "registry must store the exact path");

  if (process.platform !== "win32") {
    check(((await stat(dirs.registry)).mode & 0o777) === 0o600, "registry file must be 0600");
    check(((await stat(dirname(dirs.registry))).mode & 0o777) === 0o700, "registry directory must be 0700");
  }
}

async function testBuiltReadBarrierAndDedup() {
  const dirs = await freshDirs("read-integration");
  const file = join(dirs.root, "read-me.txt");
  await writeFile(file, "alpha\nbeta\n");

  const first = await runChild("read-once", { ...dirs.env, TEST_FILE: file });
  check(first.isError === false, `built cave__read failed: ${first.text}`);
  check(first.body !== null, "barrier: registry must exist when cave__read resolves");
  check(registryLines(first.body).includes(file), "built cave__read must register the exact path before resolving");

  const repeat = await runChild("read-repeat", { ...dirs.env, TEST_FILE: file });
  check(repeat.firstError === false && repeat.secondError === false, "repeated cave__read failed");
  check(
    registryLines(repeat.body).filter((line) => line === file).length === 1,
    "repeated built cave__read must not duplicate the path",
  );
}

async function testCacheHitBarrier() {
  const dirs = await freshDirs("cache-hit-race");
  const file = join(dirs.root, "cache-race.txt");
  await writeFile(file, "race payload\n");

  const result = await runChild("helper-cache-race", { ...dirs.env, TEST_FILE: file });
  check(result.hit === true, "race precondition: isFileUnchanged must return a cache hit");
  check(result.bodyAfterHit !== null, "cache-hit barrier: registry must exist when isFileUnchanged resolves");
  check(
    registryLines(result.bodyAfterHit).includes(file),
    "cache-hit barrier: exact path must be visible when the stub resolves",
  );
  check(
    registryLines(result.bodyAfterUpdate).filter((line) => line === file).length === 1,
    "cache-hit barrier: path must still appear exactly once after updateFileCache resolves",
  );
}

async function testPreservesExistingLines() {
  const dirs = await freshDirs("preserve");
  await mkdir(dirname(dirs.registry), { recursive: true, mode: 0o700 });
  const legacyA = join(dirs.root, "legacy-a.txt");
  const legacyB = join(dirs.root, "legacy-b.txt");
  // No trailing newline: the new append must still start on its own line.
  await writeFile(dirs.registry, `${legacyA}\n${legacyB}`);
  const file = join(dirs.root, "new-file.txt");

  const result = await runChild("helper-once", { ...dirs.env, TEST_FILE: file });
  const lines = registryLines(result.body);
  check(lines.includes(legacyA), "existing first line must be preserved");
  check(lines.includes(legacyB), "existing last line without trailing LF must be preserved");
  check(lines.includes(file), "new path must be appended");
  check(lines.length === 3, `expected exactly 3 lines, got ${lines.length}`);
}

async function testSeedTightensKnownPathModes() {
  const dirs = await freshDirs("seed-tighten");
  await mkdir(dirname(dirs.registry), { recursive: true, mode: 0o755 });
  const known = join(dirs.root, "already-known.txt");
  await writeFile(dirs.registry, `${known}\n`);
  if (process.platform !== "win32") {
    await chmod(dirname(dirs.registry), 0o755);
    await chmod(dirs.registry, 0o644);
  }

  const result = await runChild("helper-count", { ...dirs.env, TEST_FILE: known });
  check(result.exactCount === 1, "seeded known path must dedup without appending");
  check(result.totalLines === 1, "seeded registry must keep a single line");

  if (process.platform !== "win32") {
    check(((await stat(dirs.registry)).mode & 0o777) === 0o600, "seeded registry file must be tightened to 0600");
    check(((await stat(dirname(dirs.registry))).mode & 0o777) === 0o700, "seeded registry dir must be tightened to 0700");
  }
}

async function testSeedDoesNotTraverseSymlinkedFinalDir() {
  const dirs = await freshDirs("seed-symlink-dir");
  const realDir = join(dirs.root, "real-cave-tools");
  await mkdir(realDir, { recursive: true, mode: 0o755 });
  const known = join(dirs.root, "known-in-symlink-dir.txt");
  const realRegistry = join(realDir, "read-registry.txt");
  await writeFile(realRegistry, `${known}\n`);
  if (process.platform !== "win32") {
    await chmod(realDir, 0o755);
    await chmod(realRegistry, 0o644);
  }
  await mkdir(dirs.claudeDir, { recursive: true });
  await symlink(realDir, dirname(dirs.registry));

  const result = await runChild("helper-count", { ...dirs.env, TEST_FILE: known });
  check(result.exactCount === 1, "symlinked final dir: seeded entry must not be duplicated");
  check((await readFile(realRegistry, "utf8")) === `${known}\n`, "symlinked final dir: target registry must stay unchanged");
  check((await lstat(dirname(dirs.registry))).isSymbolicLink(), "symlinked final dir must not be replaced");
  if (process.platform !== "win32") {
    check(
      ((await stat(realRegistry)).mode & 0o777) === 0o644,
      "symlinked final dir: target file must not be chmodded through the symlink",
    );
  }
}

async function testSeedReadIsBounded() {
  const dirs = await freshDirs("seed-cap");
  await mkdir(dirname(dirs.registry), { recursive: true, mode: 0o700 });
  const known = join(dirs.root, "known-before-cap.txt");
  // Larger than MAX_SEED_BYTES (1 MiB). The known line is inside the cap, so a
  // bounded seed must find it and dedup; an unlimited/stat-only guard either
  // reads too much or skips the seed and appends a duplicate.
  await writeFile(dirs.registry, `${known}\n${"p".repeat(1024 * 1024 + 4096)}`);

  const result = await runChild("helper-count", { ...dirs.env, TEST_FILE: known });
  check(result.exactCount === 1, "oversized registry: known path inside the cap must dedup");
}

async function testShortWriteSourceGuard() {
  const source = await readFile(SRC_HELPER, "utf8");
  check(
    source.includes("bytesWritten !== payload.length") && source.includes("short read registry append"),
    "source guard: append must reject a short write so dedup state is not marked complete",
  );
}

async function testSymlinkRegistryUntouched() {
  const dirs = await freshDirs("symlink");
  await mkdir(dirname(dirs.registry), { recursive: true, mode: 0o700 });
  const target = join(dirs.root, "sentinel.txt");
  await writeFile(target, "SENTINEL\n");
  await symlink(target, dirs.registry);

  const file = join(dirs.root, "must-not-leak.txt");
  await runChild("helper-once", { ...dirs.env, TEST_FILE: file });

  check((await readFile(target, "utf8")) === "SENTINEL\n", "symlink registry target must not be modified");
  check((await lstat(dirs.registry)).isSymbolicLink(), "registry symlink must not be replaced");
}

async function testIoErrorsDoNotFailRead() {
  const dirs = await freshDirs("io-error");
  // Make `cave-tools` a file so creating the registry directory fails.
  await mkdir(dirs.claudeDir, { recursive: true });
  const blocker = join(dirs.claudeDir, "cave-tools");
  await writeFile(blocker, "not a directory\n");
  const file = join(dirs.root, "still-readable.txt");
  await writeFile(file, "payload\n");

  const result = await runChild("read-once", { ...dirs.env, TEST_FILE: file });
  check(result.isError === false, `registry IO error must not fail read: ${result.text}`);
  check(String(result.text).includes("payload"), "read must still return file content");
  check((await readFile(blocker, "utf8")) === "not a directory\n", "blocking file must be untouched");
}

async function testInvalidPathsRejected() {
  const dirs = await freshDirs("invalid-paths");
  const result = await runChild("helper-invalid", { ...dirs.env, TEST_BASE: dirs.root });
  check(
    result.body === null || !result.body.includes("/injected"),
    "path containing CR/LF must never be written as a split line",
  );
  check(
    result.body === null || !result.body.includes("relative/path.txt"),
    "relative path must not be written",
  );
}

async function testConcurrentRegistration() {
  const dirs = await freshDirs("concurrent");
  const start = join(dirs.root, "start");
  const files = Array.from({ length: 8 }, (_, i) => join(dirs.root, `concurrent-${i}.txt`));
  const runs = files.map((file) =>
    runChild("helper-after-start", { ...dirs.env, TEST_FILE: file, TEST_START: start }, start),
  );
  await Promise.all(runs);

  const body = await readFile(dirs.registry, "utf8");
  const lines = registryLines(body);
  for (const file of files) {
    check(lines.includes(file), `concurrent registration lost ${file}`);
  }
  check(lines.length >= files.length, "concurrent registration truncated entries");
}

let TMP_ROOT;

async function main() {
  await assertFreshDist();
  await mkdir("/tmp/opencode", { recursive: true, mode: 0o700 });
  TMP_ROOT = await mkdtemp(join("/tmp/opencode", "cave-read-registry-"));
  try {
    await testHelperDedupPermissionsAndBarrier();
    await testBuiltReadBarrierAndDedup();
    await testCacheHitBarrier();
    await testPreservesExistingLines();
    await testSeedTightensKnownPathModes();
    await testSeedDoesNotTraverseSymlinkedFinalDir();
    await testSeedReadIsBounded();
    await testShortWriteSourceGuard();
    await testSymlinkRegistryUntouched();
    await testIoErrorsDoNotFailRead();
    await testInvalidPathsRejected();
    await testConcurrentRegistration();
    console.log(`read-registry tests: ${passed} checks passed`);
  } finally {
    if (process.env.KEEP === "1") {
      console.log(`kept fixtures: ${TMP_ROOT}`);
    } else {
      await rm(TMP_ROOT, { recursive: true, force: true });
    }
  }
}

if (process.argv[2] === "--scenario") {
  runScenario(process.argv[3]).catch((error) => {
    process.stderr.write(`${error?.stack || error}\n`);
    process.exit(1);
  });
} else {
  main().catch((error) => {
    console.error(`read-registry tests failed: ${error?.stack || error}`);
    process.exit(1);
  });
}
