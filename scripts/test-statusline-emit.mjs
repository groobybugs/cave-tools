#!/usr/bin/env node
// Dedicated tests for the in-process statusline badge refresh.
//
// Covers the debounced emitter, the atomic symlink-safe write, the MCP
// server's post-call refresh (no `status --emit-statusline` child), the
// opt-out, and byte equality with `cave-tools status --emit-statusline`.

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const CLI = join(ROOT, "dist", "cli.js");
const SUFFIX = ".cave-tools-statusline-suffix";

function fixtureRoot() {
  const preferred = process.platform === "linux" ? join("/tmp", "opencode") : tmpdir();
  try {
    mkdirSync(preferred, { recursive: true, mode: 0o700 });
    return mkdtempSync(join(preferred, "cave-statusline-"));
  } catch {
    return mkdtempSync(join(tmpdir(), "cave-statusline-"));
  }
}

function cleanEnv(extra) {
  const env = { ...process.env };
  for (const key of Object.keys(env)) {
    if (key.startsWith("CLAUDE_CODE_") || key.startsWith("CAVE_TOOLS_")) delete env[key];
  }
  delete env.CAVE_TOOLS_IMPL;
  delete env.CAVE_TOOLS_GO_BIN;
  return { ...env, ...extra };
}

function sleep(ms) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms));
}

async function waitForFile(path, timeoutMs = 3000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      return readFileSync(path, "utf8");
    } catch {
      if (Date.now() > deadline) return null;
      await sleep(10);
    }
  }
}

async function testEmitterDebounce() {
  const { createStatuslineEmitter } = await import("../dist/statusline.js");
  const writes = [];
  let builds = 0;
  const emitter = createStatuslineEmitter({
    debounceMs: 40,
    build: () => {
      builds++;
      return "badge";
    },
    write: (suffix) => writes.push(suffix),
    disabled: () => false,
  });
  for (let i = 0; i < 5; i++) {
    emitter.schedule();
    await sleep(3);
  }
  await sleep(150);
  assert.deepEqual(writes, ["badge"], "burst must collapse into one write");
  assert.equal(builds, 1, "one aggregate per burst");
  console.log("ok - emitter debounce");
}

async function testEmitterFlush() {
  const { createStatuslineEmitter } = await import("../dist/statusline.js");
  const writes = [];
  const emitter = createStatuslineEmitter({
    debounceMs: 60000,
    build: () => "flushed",
    write: (suffix) => writes.push(suffix),
    disabled: () => false,
  });
  emitter.schedule();
  emitter.flush();
  emitter.flush();
  assert.deepEqual(writes, ["flushed"], "flush writes pending once, then nothing");
  console.log("ok - emitter flush");
}

async function testEmitterOptOut() {
  const { createStatuslineEmitter } = await import("../dist/statusline.js");
  const previous = process.env.CAVE_TOOLS_STATUSLINE_SAVINGS;
  process.env.CAVE_TOOLS_STATUSLINE_SAVINGS = "0";
  try {
    let builds = 0;
    const writes = [];
    const emitter = createStatuslineEmitter({
      debounceMs: 10,
      build: () => {
        builds++;
        return "badge";
      },
      write: (suffix) => writes.push(suffix),
    });
    emitter.schedule();
    await sleep(60);
    assert.equal(builds, 0, "opt-out must skip the aggregate read");
    assert.deepEqual(writes, [], "opt-out must skip the write");
  } finally {
    if (previous === undefined) delete process.env.CAVE_TOOLS_STATUSLINE_SAVINGS;
    else process.env.CAVE_TOOLS_STATUSLINE_SAVINGS = previous;
  }
  console.log("ok - emitter opt-out");
}

async function testEmitterFailuresAreSilent() {
  const { createStatuslineEmitter } = await import("../dist/statusline.js");
  let builds = 0;
  const emitter = createStatuslineEmitter({
    debounceMs: 10,
    build: () => {
      builds++;
      throw new Error("aggregate failed");
    },
    write: () => {},
    disabled: () => false,
  });
  emitter.schedule();
  await sleep(60);
  assert.equal(builds, 1, "a failed aggregate is not retried by flush");
  emitter.schedule();
  await sleep(60);
  assert.ok(builds >= 2, "emitter stays usable after a thrown build");
  console.log("ok - emitter failures are silent");
}

async function testAtomicWrite() {
  const { writeStatuslineSuffix } = await import("../dist/statusline.js");
  const root = fixtureRoot();
  try {
    const claudeDir = join(root, "claude");
    const previous = process.env.CLAUDE_CONFIG_DIR;
    process.env.CLAUDE_CONFIG_DIR = claudeDir;
    try {
      writeStatuslineSuffix("v38% | 1.1M tok");
      writeStatuslineSuffix("v40% | 2k tok");
      const path = join(claudeDir, SUFFIX);
      assert.equal(readFileSync(path, "utf8"), "v40% | 2k tok");
      if (process.platform !== "win32") {
        assert.equal(statSync(path).mode & 0o777, 0o600, "badge file is 0600");
      }
      assert.deepEqual(readdirSync(claudeDir), [SUFFIX], "no temp files left behind");
    } finally {
      if (previous === undefined) delete process.env.CLAUDE_CONFIG_DIR;
      else process.env.CLAUDE_CONFIG_DIR = previous;
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
  console.log("ok - atomic write");
}

// The writer must refuse a symlinked suffix target instead of following it
// (mirrors Go TestWriteRefusesSymlinkTarget).
async function testWriteRefusesSymlinkTarget() {
  const { writeStatuslineSuffix } = await import("../dist/statusline.js");
  const root = fixtureRoot();
  try {
    const claudeDir = join(root, "claude");
    mkdirSync(claudeDir, { recursive: true, mode: 0o700 });
    const victim = join(root, "victim");
    writeFileSync(victim, "keep");
    try {
      symlinkSync(victim, join(claudeDir, SUFFIX));
    } catch (error) {
      console.log(`skip - symlink refusal (${error.code ?? error.message})`);
      return;
    }
    const previous = process.env.CLAUDE_CONFIG_DIR;
    process.env.CLAUDE_CONFIG_DIR = claudeDir;
    try {
      writeStatuslineSuffix("new badge");
      assert.equal(readFileSync(victim, "utf8"), "keep", "must not follow symlink");
    } finally {
      if (previous === undefined) delete process.env.CLAUDE_CONFIG_DIR;
      else process.env.CLAUDE_CONFIG_DIR = previous;
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
  console.log("ok - symlink refusal");
}

// startMCP launches the TS server and returns a minimal JSON-RPC client.
function startMCP(cwd, env) {
  const child = spawn(process.execPath, [CLI, "mcp"], {
    cwd,
    env: cleanEnv(env),
    stdio: ["pipe", "pipe", "pipe"],
  });
  const pending = new Map();
  let buffer = "";
  let stderr = "";
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  child.stdout.on("data", (chunk) => {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf("\n")) !== -1) {
      const line = buffer.slice(0, newline);
      buffer = buffer.slice(newline + 1);
      if (!line.trim()) continue;
      const message = JSON.parse(line);
      const waiter = pending.get(message.id);
      if (waiter) {
        pending.delete(message.id);
        waiter(message);
      }
    }
  });
  let nextId = 0;
  const send = (message) => child.stdin.write(`${JSON.stringify(message)}\n`);
  const request = (method, params) =>
    new Promise((resolveResponse, rejectResponse) => {
      const id = ++nextId;
      pending.set(id, resolveResponse);
      send({ jsonrpc: "2.0", id, method, params });
      setTimeout(() => {
        if (pending.delete(id)) rejectResponse(new Error(`timeout: ${method} (${stderr})`));
      }, 10000);
    });
  return { child, send, request, stderr: () => stderr };
}

async function handshake(client) {
  await client.request("initialize", {
    protocolVersion: "2025-06-18",
    capabilities: {},
    clientInfo: { name: "statusline-test", version: "0" },
  });
  client.send({ jsonrpc: "2.0", method: "notifications/initialized" });
}

function closeMCP(client) {
  return new Promise((resolveClose) => {
    client.child.once("exit", () => resolveClose());
    client.child.stdin.end();
    setTimeout(() => {
      if (client.child.exitCode === null) client.child.kill("SIGKILL");
    }, 5000).unref?.();
  });
}

async function testMCPRefreshMatchesStatusCommand() {
  const root = fixtureRoot();
  const dataDir = join(root, "data");
  const mcpClaude = join(root, "mcp-claude");
  const cliClaude = join(root, "cli-claude");
  const cwd = join(root, "work");
  mkdirSync(cwd, { recursive: true });
  const target = join(cwd, "note.txt");
  writeFileSync(target, "statusline payload\n");
  const baseEnv = {
    CAVE_TOOLS_DATA_DIR: dataDir,
    CAVE_TOOLS_DAEMON: "0",
    CAVE_TOOLS_ASCII: "1",
    CAVE_TOOLS_STATUS_RTK: "0",
    HOME: root,
  };
  try {
    const client = startMCP(cwd, { ...baseEnv, CLAUDE_CONFIG_DIR: mcpClaude });
    await handshake(client);
    const call = (id, args) =>
      client.request("tools/call", { name: "cave__read", arguments: args, _meta: { sessionID: `s${id}` } });
    const first = await call(1, { file_path: target });
    assert.ok(first.result, `read failed: ${JSON.stringify(first)}`);
    const second = await call(2, { file_path: target });
    assert.ok(second.result, `second read failed: ${JSON.stringify(second)}`);

    const badgePath = join(mcpClaude, SUFFIX);
    const badge = await waitForFile(badgePath);
    assert.notEqual(badge, null, "MCP server must write the badge after a call");
    assert.notEqual(badge, "", "badge must not be empty");
    assert.match(badge, /^v\d+% \| .+$/, `unexpected badge: ${JSON.stringify(badge)}`);

    await closeMCP(client);

    const cli = spawnSync(
      process.execPath,
      [CLI, "status", "--emit-statusline"],
      { cwd, env: cleanEnv({ ...baseEnv, CLAUDE_CONFIG_DIR: cliClaude }), encoding: "utf8", timeout: 30000 },
    );
    assert.equal(cli.status, 0, cli.stderr);
    assert.equal(cli.stdout, "", "status --emit-statusline writes no stdout");
    const expected = readFileSync(join(cliClaude, SUFFIX), "utf8");
    assert.equal(badge, expected, "in-process badge must equal status --emit-statusline output");
    console.log("ok - MCP refresh matches status --emit-statusline");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

async function testMCPWriteFailureIsSilent() {
  const root = fixtureRoot();
  const dataDir = join(root, "data");
  const cwd = join(root, "work");
  mkdirSync(cwd, { recursive: true });
  const target = join(cwd, "note.txt");
  writeFileSync(target, "still works\n");
  const blocker = join(root, "blocker");
  writeFileSync(blocker, "not a directory");
  const baseEnv = {
    CAVE_TOOLS_DATA_DIR: dataDir,
    CAVE_TOOLS_DAEMON: "0",
    CAVE_TOOLS_ASCII: "1",
    HOME: root,
    // mkdir(config dir) fails: the badge write must stay silent.
    CLAUDE_CONFIG_DIR: join(blocker, "claude"),
  };
  try {
    const client = startMCP(cwd, baseEnv);
    await handshake(client);
    const first = await client.request("tools/call", {
      name: "cave__read",
      arguments: { file_path: target },
    });
    assert.ok(first.result, `tool call lost its result: ${JSON.stringify(first)}`);
    assert.notEqual(first.result.isError, true, "tool result must not become an error");
    await sleep(450); // let the debounce fire
    const second = await client.request("tools/call", {
      name: "cave__read",
      arguments: { file_path: target },
    });
    assert.ok(second.result, `server unusable after write failure: ${JSON.stringify(second)}`);
    assert.equal(existsSync(join(blocker, "claude")), false, "no config dir was created");
    await closeMCP(client);
    console.log("ok - write failure does not affect the tool result");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

async function main() {
  assert.equal(existsSync(CLI), true, "dist/cli.js missing; run pnpm build");
  await testEmitterDebounce();
  await testEmitterFlush();
  await testEmitterOptOut();
  await testEmitterFailuresAreSilent();
  await testAtomicWrite();
  await testWriteRefusesSymlinkTarget();
  await testMCPRefreshMatchesStatusCommand();
  await testMCPWriteFailureIsSilent();
  console.log("statusline emit tests passed");
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
