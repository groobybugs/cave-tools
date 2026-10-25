#!/usr/bin/env node
// Dedicated tests for the opt-in Go implementation switch (W5b).
//
// Covers the direct `node dist/cli.js` entry point, the generated POSIX
// launcher shim, argv/stdin/env/cwd/exit/signal forwarding, missing and
// non-executable binaries, exact environment matching and a real Go smoke.

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:http";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { brotliCompressSync } from "node:zlib";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const CLI = join(ROOT, "dist", "cli.js");
const INSTALLER = join(ROOT, "scripts", "install-agent-instructions.mjs");
const REMOVER = join(ROOT, "scripts", "remove-agent-instructions.mjs");
const GO_BIN = join(ROOT, "go", "bin", process.platform === "win32" ? "cave-tools-go.exe" : "cave-tools-go");
const POSIX = process.platform !== "win32";

function fixtureRoot() {
  const preferred = process.platform === "linux" ? "/tmp/opencode" : tmpdir();
  mkdirSync(preferred, { recursive: true, mode: 0o700 });
  return mkdtempSync(join(preferred, "cave-tools-switch-"));
}

const ROOT_FIXTURE = fixtureRoot();

// Subagent-like keys change CLI behavior (fresh reads) and make the child log
// "unknown subagent-like env keys" to stderr (src/compression/utils.ts).
const SUBAGENT_CANDIDATE = /(?:SUBAGENT|^CLAUDE_CODE_)/;
const TASK_ID_STYLE = /(?:^|_)TASK_ID$/;

function cleanEnv() {
  const env = { ...process.env };
  for (const key of ["CAVE_TOOLS_IMPL", "CAVE_TOOLS_GO_BIN", "CAVE_TOOLS_DATA_DIR", "CLAUDE_CONFIG_DIR", "CAVE_TOOLS_FORCE_FRESH"]) {
    delete env[key];
  }
  for (const key of Object.keys(env)) {
    if (SUBAGENT_CANDIDATE.test(key) || TASK_ID_STYLE.test(key)) delete env[key];
  }
  return env;
}

function cliEnv(extra = {}) {
  return { ...cleanEnv(), ...extra };
}

function runCli(args, options = {}) {
  const result = spawnSync(process.execPath, [CLI, ...args], {
    cwd: options.cwd ?? ROOT,
    env: cliEnv(options.env),
    input: options.input,
    encoding: options.encoding === undefined ? "utf8" : options.encoding,
    stdio: options.stdio,
    timeout: options.timeout ?? 15000,
  });
  if (result.error) throw result.error;
  return result;
}

function writeExecutable(file, body) {
  writeFileSync(file, body, { mode: 0o755 });
  chmodSync(file, 0o755);
  return file;
}

const FAKE_GO_BODY = `#!/bin/sh
if [ -n "\${CAVE_TOOLS_SWITCH_MARKER:-}" ]; then
  : > "$CAVE_TOOLS_SWITCH_MARKER"
fi
printf 'argc=%s\\n' "$#"
i=0
for arg in "$@"; do
  printf 'arg%d=%s\\n' "$i" "$arg"
  i=$((i + 1))
done
printf 'cwd=%s\\n' "$PWD"
printf 'env=%s\\n' "\${CAVE_TOOLS_SWITCH_ENV:-}"
printf 'stderr=%s\\n' "\${CAVE_TOOLS_SWITCH_STDERR:-}" >&2
exit "\${CAVE_TOOLS_SWITCH_EXIT:-0}"
`;

function fakeGo(dir, name = "fake-go") {
  mkdirSync(dir, { recursive: true });
  return writeExecutable(join(dir, name), FAKE_GO_BODY);
}

function fakeCat(dir, name = "fake-cat") {
  mkdirSync(dir, { recursive: true });
  return writeExecutable(join(dir, name), "#!/bin/sh\ncat\n");
}

function fakeSleeper(dir, name = "fake-sleep") {
  mkdirSync(dir, { recursive: true });
  return writeExecutable(join(dir, name), "#!/bin/sh\nprintf 'ready\\n'\nexec sleep 30\n");
}

async function waitForLine(stream, timeoutMs = 10000) {
  return new Promise((resolvePromise, reject) => {
    let buffer = "";
    const timer = setTimeout(() => {
      stream.removeListener("data", onData);
      reject(new Error(`timed out waiting for stdout (${timeoutMs}ms)`));
    }, timeoutMs);
    function onData(chunk) {
      buffer += chunk.toString();
      const index = buffer.indexOf("\n");
      if (index === -1) return;
      clearTimeout(timer);
      stream.removeListener("data", onData);
      resolvePromise(buffer.slice(0, index));
    }
    stream.on("data", onData);
  });
}

function waitForClose(child, timeoutMs = 10000) {
  return new Promise((resolvePromise, reject) => {
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`timed out waiting for child close (${timeoutMs}ms)`));
    }, timeoutMs);
    child.once("close", (code, signal) => {
      clearTimeout(timer);
      resolvePromise({ code, signal });
    });
  });
}

async function testResolutionModule() {
  const modulePath = pathToFileURL(join(ROOT, "dist", "impl-switch.js")).href;
  const mod = await import(modulePath);

  for (const value of ["go"]) {
    assert.equal(mod.shouldUseGo({ CAVE_TOOLS_IMPL: value }), true, `selects ${value}`);
  }
  for (const value of [undefined, "", "Go", "GO", "go ", " go", "gO", "typescript", "1", "true"]) {
    const env = value === undefined ? {} : { CAVE_TOOLS_IMPL: value };
    assert.equal(mod.shouldUseGo(env), false, `does not select ${JSON.stringify(value)}`);
  }

  const win = mod.resolveGoBinary({ env: {}, platform: "win32", packageRoot: join(ROOT, "pkg") });
  assert.equal(win.path, join(ROOT, "pkg", "go", "bin", "cave-tools-go.exe"));
  assert.ok(win.path.endsWith("cave-tools-go.exe"), "Windows default uses .exe");
  assert.equal(win.source, "default");
  const posix = mod.resolveGoBinary({ env: {}, platform: "linux", packageRoot: join(ROOT, "pkg") });
  assert.equal(posix.path, join(ROOT, "pkg", "go", "bin", "cave-tools-go"));
  assert.ok(!posix.path.endsWith(".exe"), "POSIX default has no .exe suffix");

  const spaced = join(ROOT_FIXTURE, "override dir ü", "my go");
  const override = mod.resolveGoBinary({
    env: { CAVE_TOOLS_GO_BIN: spaced },
    cwd: join(ROOT_FIXTURE, "somewhere else"),
    platform: "linux",
  });
  assert.equal(override.path, resolve(spaced), "absolute override preserved verbatim");
  assert.equal(override.source, "env");

  // Routing rule shared with the POSIX shim generator.
  assert.deepEqual(
    [...mod.TS_ROUTED_COMMANDS],
    ["mcp", "webfetch", "websearch"],
    "canonical TS-routed command list",
  );
  assert.deepEqual(
    [...mod.TS_ROUTED_TOOLS],
    ["webfetch", "websearch"],
    "canonical TS-routed tool list",
  );
  assert.equal(mod.routesToTypeScript([]), true, "bare invocation is the stdio MCP server");
  for (const command of mod.TS_ROUTED_COMMANDS) {
    assert.equal(mod.routesToTypeScript([command]), true, `routes ${command} to TS`);
    assert.equal(mod.routesToTypeScript([command, "--flag"]), true, `first token decides for ${command}`);
  }
  // The explicit `tool <name>` dispatcher loads the same handler in src/cli.ts.
  for (const tool of mod.TS_ROUTED_TOOLS) {
    assert.equal(mod.routesToTypeScript(["tool", tool]), true, `routes tool ${tool} to TS`);
    assert.equal(mod.routesToTypeScript(["tool", tool, "--flag"]), true, `tool ${tool} trailing flags stay in TS`);
  }
  for (const argv of [["tool"], ["tool", "filter"], ["tool", "read"], ["tool", "webfetch "], ["tool", "Webfetch"]]) {
    assert.equal(mod.routesToTypeScript(argv), false, `keeps ${JSON.stringify(argv)} on the Go path`);
  }
  for (const command of [
    "__job-worker",
    "filter",
    "status",
    "raw",
    "bash",
    "jobs",
    "daemon",
    "help",
    "--version",
    "not-a-command",
    "MCP",
    "webfetch ",
  ]) {
    assert.equal(mod.routesToTypeScript([command]), false, `keeps ${JSON.stringify(command)} on the Go path`);
  }
  const relative = mod.resolveGoBinary({
    env: { CAVE_TOOLS_GO_BIN: join("relative ü", "go bin") },
    cwd: join(ROOT_FIXTURE, "cwd with spaces"),
    platform: "linux",
  });
  assert.equal(relative.path, resolve(join(ROOT_FIXTURE, "cwd with spaces"), "relative ü", "go bin"));
  const emptyOverride = mod.resolveGoBinary({
    env: { CAVE_TOOLS_GO_BIN: "" },
    platform: "linux",
    packageRoot: join(ROOT, "pkg"),
  });
  assert.equal(emptyOverride.source, "default", "empty override falls back to the local binary");

  const missing = join(ROOT_FIXTURE, "missing", "go");
  assert.equal(mod.goBinaryProblem(missing, "linux"), "missing");
  if (POSIX) {
    const plain = join(ROOT_FIXTURE, "plain-go");
    writeFileSync(plain, "not executable", { mode: 0o644 });
    assert.equal(mod.goBinaryProblem(plain, "linux"), "not-executable");
    assert.equal(mod.goBinaryProblem(plain, "win32"), null, "Windows has no POSIX X_OK check");
  }
  const executable = fakeGo(join(ROOT_FIXTURE, "unit"));
  assert.equal(mod.goBinaryProblem(executable, process.platform), null);
}

function testDirectSwitch() {
  if (!POSIX) {
    console.log("SKIP fake Go forwarding: POSIX shell fixture");
    return;
  }
  const dir = join(ROOT_FIXTURE, "direct");
  const fake = fakeGo(join(dir, "bin with spaces ü"), "cave tools-go ü");
  const marker = join(dir, "marker");
  const cwd = join(ROOT_FIXTURE, "cwd with spaces ü");
  mkdirSync(cwd, { recursive: true });

  // Unset and every non-exact value keep the TypeScript CLI.
  for (const value of [undefined, "", "Go", "GO", "go ", "typescript", "1"]) {
    rmSync(marker, { force: true });
    const env = {
      CAVE_TOOLS_GO_BIN: fake,
      CAVE_TOOLS_SWITCH_MARKER: marker,
    };
    if (value === undefined) delete env.CAVE_TOOLS_IMPL;
    else env.CAVE_TOOLS_IMPL = value;
    const result = runCli(["--version"], { env });
    assert.equal(result.status, 2, `TS default for ${JSON.stringify(value)}`);
    assert.match(result.stderr, /^Usage: cave-tools /, `TS usage for ${JSON.stringify(value)}`);
    assert.equal(existsSync(marker), false, `Go binary not started for ${JSON.stringify(value)}`);
  }

  // Exact go forwards argv, cwd, env, stdout, stderr and the exit code.
  const args = ["alpha beta", 'quote"x', "single'quote", "back\\slash", "ünïcode", "", "$HOME", "*"];
  const result = runCli(args, {
    cwd,
    env: {
      CAVE_TOOLS_IMPL: "go",
      CAVE_TOOLS_GO_BIN: fake,
      CAVE_TOOLS_SWITCH_MARKER: marker,
      CAVE_TOOLS_SWITCH_ENV: "value with spaces ü",
      CAVE_TOOLS_SWITCH_STDERR: "err with spaces ü",
      CAVE_TOOLS_SWITCH_EXIT: "7",
    },
  });
  assert.equal(result.status, 7, "Go exit code forwarded");
  assert.equal(existsSync(marker), true, "Go binary ran");
  assert.equal(
    result.stdout,
    [
      "argc=8",
      "arg0=alpha beta",
      'arg1=quote"x',
      "arg2=single'quote",
      "arg3=back\\slash",
      "arg4=ünïcode",
      "arg5=",
      "arg6=$HOME",
      "arg7=*",
      `cwd=${cwd}`,
      "env=value with spaces ü",
      "",
    ].join("\n"),
    "argv/cwd/env forwarded exactly",
  );
  assert.equal(result.stderr, "stderr=err with spaces ü\n", "stderr forwarded");

  // The stdio MCP server (bare invocation or `mcp`) and the Node-fetch-backed
  // web tools stay in TypeScript under the opt-in. The fake Go marker proves
  // the binary never started; the TS usage text proves TS handled the command.
  const tsDataDir = join(dir, "ts data");
  const routedEnv = {
    CAVE_TOOLS_IMPL: "go",
    CAVE_TOOLS_GO_BIN: fake,
    CAVE_TOOLS_SWITCH_MARKER: marker,
    CAVE_TOOLS_DATA_DIR: tsDataDir,
    CAVE_TOOLS_DAEMON: "0",
  };
  for (const argv of [[], ["mcp"]]) {
    rmSync(marker, { force: true });
    const routed = runCli(argv, { env: routedEnv, input: "" });
    assert.equal(routed.status, 0, `${JSON.stringify(argv)}: ${routed.stderr}`);
    assert.equal(existsSync(marker), false, `${JSON.stringify(argv)} must not start Go`);
    assert.match(routed.stderr, /Cave Tools MCP server running on stdio/, "TS MCP server started");
  }
  for (const [command, missing] of [
    ["webfetch", /cave-tools webfetch: missing required: url/],
    ["websearch", /cave-tools websearch: missing required: query/],
  ]) {
    for (const argv of [[command], ["tool", command]]) {
      rmSync(marker, { force: true });
      const routed = runCli(argv, { env: routedEnv, input: "" });
      assert.equal(routed.status, 2, `${JSON.stringify(argv)} stays in TS and reports a usage error`);
      assert.match(routed.stderr, missing, `${JSON.stringify(argv)} usage comes from TS`);
      assert.equal(existsSync(marker), false, `${JSON.stringify(argv)} must not start Go`);
    }
  }

  // Everything else keeps going to Go: the durable job worker, the CLI
  // commands and the tool dispatcher.
  for (const argv of [
    ["--version"],
    ["__job-worker", "job", "log", "result", "/bin/sh", "echo"],
    ["status"],
    ["filter", "--json"],
    ["raw", "cat"],
    ["jobs"],
    ["daemon", "status"],
    ["tool", "filter", "--json"],
  ]) {
    rmSync(marker, { force: true });
    const forwarded = runCli(argv, {
      env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake, CAVE_TOOLS_SWITCH_MARKER: marker },
    });
    assert.equal(forwarded.status, 0, `${JSON.stringify(argv)}: ${forwarded.stderr}`);
    assert.equal(existsSync(marker), true, `${JSON.stringify(argv)} runs Go`);
    assert.match(
      forwarded.stdout,
      new RegExp(`^argc=${argv.length}\\narg0=${argv[0]}\\n`),
      `${JSON.stringify(argv)} forwarded untouched`,
    );
  }

  // The durable job worker selector is forwarded before any flag parsing.
  const jobWorker = runCli(["__job-worker", "job", "log", "result", "/bin/sh", "echo"], {
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake },
  });
  assert.equal(jobWorker.status, 0);
  assert.match(jobWorker.stdout, /^argc=6\narg0=__job-worker\n/, "__job-worker argv forwarded untouched");

  // Binary bytes on stdin reach Go unchanged.
  const cat = fakeCat(join(dir, "cat"));
  const bytes = Buffer.from([0, 1, 2, 10, 13, 26, 65, 255, 0]);
  const stdinResult = runCli(["filter", "--json"], {
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: cat },
    input: bytes,
    encoding: null,
  });
  assert.equal(stdinResult.status, 0);
  assert.equal(Buffer.compare(stdinResult.stdout, bytes), 0, "stdin bytes forwarded unchanged");

  // Missing, non-executable and directory targets fail without TS fallback.
  const missing = join(ROOT_FIXTURE, "missing dir ü", "cave-tools-go");
  const missingResult = runCli(["--version"], {
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: missing },
  });
  assert.equal(missingResult.status, 127, "missing Go binary exit");
  assert.match(missingResult.stderr, /CAVE_TOOLS_IMPL=go but the Go binary was not found/);
  assert.match(missingResult.stderr, /cave-tools-go/);
  assert.doesNotMatch(missingResult.stderr, /Usage: cave-tools|Benchmark mode/, "no TS fallback on missing Go");

  const plain = join(dir, "plain-go");
  writeFileSync(plain, "plain", { mode: 0o644 });
  const plainResult = runCli(["--version"], {
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: plain },
  });
  assert.equal(plainResult.status, 126, "non-executable Go binary exit");
  assert.match(plainResult.stderr, /not executable/);
  assert.doesNotMatch(plainResult.stderr, /Usage: cave-tools/, "no TS fallback on non-executable Go");

  const directoryResult = runCli(["--version"], {
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: dir },
  });
  assert.equal(directoryResult.status, 126, "directory target rejected");
  assert.match(directoryResult.stderr, /not executable/);

  // A relative override resolves against the caller's cwd, like spawn does.
  const relativeCwd = join(ROOT_FIXTURE, "relative cwd ü");
  fakeGo(join(relativeCwd, "rel bin"), "go ü");
  const relative = runCli(["--version"], {
    cwd: relativeCwd,
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: join("rel bin", "go ü") },
  });
  assert.equal(relative.status, 0, relative.stderr);
  assert.match(relative.stdout, /^argc=1\n/, "relative Go override accepted");

  // A bare name has no slash, so POSIX exec would search PATH instead of the
  // caller cwd that passed the existence check. Direct resolution is cwd-based.
  const bareCwd = join(ROOT_FIXTURE, "bare cwd ü");
  fakeGo(bareCwd, "bare go");
  const bare = runCli(["--version"], {
    cwd: bareCwd,
    env: {
      CAVE_TOOLS_IMPL: "go",
      CAVE_TOOLS_GO_BIN: "bare go",
      PATH: dirname(process.execPath),
    },
  });
  assert.equal(bare.status, 0, bare.stderr);
  assert.match(bare.stdout, /^argc=1\n/, "bare-name cwd override accepted");
}

async function testDirectSignalRelay() {
  if (!POSIX) {
    console.log("SKIP direct signal relay: POSIX shell fixture");
    return;
  }
  const fake = fakeSleeper(join(ROOT_FIXTURE, "signals"));

  for (const signal of ["SIGTERM", "SIGINT"]) {
    const child = spawn(process.execPath, [CLI, "--version"], {
      cwd: ROOT,
      env: cliEnv({ CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake }),
      stdio: ["ignore", "pipe", "pipe"],
    });
    await waitForLine(child.stdout);
    child.kill(signal);
    const { code, signal: closeSignal } = await waitForClose(child);
    assert.equal(code, null, `${signal}: parent exits by signal, not a code`);
    assert.equal(closeSignal, signal, `${signal}: signal status preserved`);
  }
}

async function testRealGoSmoke() {
  if (!existsSync(GO_BIN)) {
    console.log("SKIP real Go smoke: build go/bin/cave-tools-go first");
    return;
  }

  const version = runCli(["--version"], { env: { CAVE_TOOLS_IMPL: "go" } });
  assert.equal(version.status, 0, version.stderr);
  assert.match(version.stdout, /^cave-tools-go \d+\.\d+\.\d+\r?\n$/, "local Go binary selected by default");

  const help = runCli(["help", "read"], { env: { CAVE_TOOLS_IMPL: "go" } });
  assert.equal(help.status, 0, help.stderr);
  assert.match(help.stdout, /^read: /, "non-MCP command routed to Go");

  const unknown = runCli(["not-a-command"], { env: { CAVE_TOOLS_IMPL: "go" } });
  assert.equal(unknown.status, 2, "Go usage errors keep their exit code");
  assert.match(unknown.stderr, /^Usage: cave-tools /);

  // The no-arg stdio MCP server stays in TypeScript under the opt-in. Point
  // the Go override at a fake that would fail loudly (exit 99) if selected.
  const mcpFakeDir = join(ROOT_FIXTURE, "mcp-route");
  const mcpFake = fakeGo(mcpFakeDir, "fake-go");
  const mcpMarker = join(mcpFakeDir, "marker");
  rmSync(mcpMarker, { force: true });
  const init = JSON.stringify({
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: { name: "switch-test", version: "1" },
    },
  });
  const dataDir = join(ROOT_FIXTURE, "mcp-data");
  const child = spawn(process.execPath, [CLI], {
    cwd: ROOT,
    env: cliEnv({
      CAVE_TOOLS_IMPL: "go",
      CAVE_TOOLS_GO_BIN: mcpFake,
      CAVE_TOOLS_SWITCH_MARKER: mcpMarker,
      CAVE_TOOLS_SWITCH_EXIT: "99",
      CAVE_TOOLS_DATA_DIR: dataDir,
      CAVE_TOOLS_DAEMON: "0",
    }),
    stdio: ["pipe", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (chunk) => (stderr += chunk));
  child.stdin.write(`${init}\n`);
  const line = await waitForLine(child.stdout);
  child.stdin.end();
  const { code } = await waitForClose(child);
  assert.equal(code, 0, stderr);
  assert.equal(existsSync(mcpMarker), false, "no-arg MCP server must not exec Go");
  const response = JSON.parse(line.trim());
  assert.equal(response.jsonrpc, "2.0");
  assert.equal(response.id, 1);
  assert.equal(response.result?.serverInfo?.name, "cave-tools");
  assert.match(stderr, /Cave Tools MCP server running on stdio/);
}

function runAsync(file, args, options = {}) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(file, args, {
      cwd: options.cwd ?? ROOT,
      env: options.env ?? cliEnv(),
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`timed out (${options.timeout ?? 30000}ms): ${file} ${args.join(" ")}`));
    }, options.timeout ?? 30000);
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.once("close", (code) => {
      clearTimeout(timer);
      resolvePromise({ status: code, stdout: Buffer.concat(stdout), stderr: Buffer.concat(stderr) });
    });
  });
}

const BROTLI_BODY = "hello brotli world";

// Loopback origin that always answers with a Brotli-encoded text body, which is
// exactly the encoding the Go webfetch port passes through untouched. undici
// decodes it whether or not the request advertised `br`, so the body is sent
// unconditionally and the difference is purely in the client.
async function startBrotliServer() {
  const compressed = brotliCompressSync(Buffer.from(BROTLI_BODY));
  const requests = [];
  const server = createServer((request, response) => {
    requests.push(String(request.headers["accept-encoding"] ?? ""));
    response.writeHead(200, {
      "content-type": "text/plain; charset=utf-8",
      "content-encoding": "br",
      "content-length": compressed.length,
    });
    response.end(compressed);
  });
  await new Promise((resolvePromise) => server.listen(0, "127.0.0.1", resolvePromise));
  const { port } = server.address();
  return { server, url: `http://127.0.0.1:${port}/brotli`, requests };
}

// Live parity for the two dispatch forms that reach the Node-fetch webfetch
// handler: `webfetch <url>` and `tool webfetch <url>`. Both must decode Brotli
// identically to plain TypeScript, on the direct and generated-shim entries.
async function testLiveWebfetchRouting() {
  const { server, url, requests } = await startBrotliServer();
  try {
    const dataDir = join(ROOT_FIXTURE, "live webfetch data");
    const args = ["--format", "text"];
    const expected = Buffer.from(`${BROTLI_BODY}\n`);
    const baseline = await runAsync(process.execPath, [CLI, "webfetch", url, ...args], {
      env: cliEnv({ CAVE_TOOLS_DATA_DIR: dataDir, CAVE_TOOLS_DAEMON: "0" }),
    });
    assert.equal(baseline.status, 0, baseline.stderr.toString());
    assert.equal(Buffer.compare(baseline.stdout, expected), 0, "plain TS decodes the Brotli body");
    assert.ok(requests.length > 0, "baseline reached the loopback server");

    const fake = fakeGo(join(ROOT_FIXTURE, "live webfetch"));
    const marker = join(ROOT_FIXTURE, "live webfetch", "marker");
    const optInEnv = {
      CAVE_TOOLS_IMPL: "go",
      CAVE_TOOLS_GO_BIN: fake,
      CAVE_TOOLS_SWITCH_MARKER: marker,
      CAVE_TOOLS_DATA_DIR: dataDir,
      CAVE_TOOLS_DAEMON: "0",
    };
    for (const argv of [
      ["webfetch", url, ...args],
      ["tool", "webfetch", url, ...args],
    ]) {
      rmSync(marker, { force: true });
      const routed = await runAsync(process.execPath, [CLI, ...argv], { env: cliEnv(optInEnv) });
      assert.equal(routed.status, 0, `${JSON.stringify(argv)}: ${routed.stderr.toString()}`);
      assert.equal(Buffer.compare(routed.stdout, expected), 0, `${JSON.stringify(argv)}: TS bytes, not raw Brotli`);
      assert.equal(Buffer.compare(routed.stdout, baseline.stdout), 0, `${JSON.stringify(argv)}: byte-identical to plain TS`);
      assert.equal(routed.stderr.toString(), baseline.stderr.toString(), `${JSON.stringify(argv)}: stderr matches TS`);
      assert.equal(existsSync(marker), false, `${JSON.stringify(argv)} must not start Go`);
    }

    if (POSIX) {
      const context = installFakeHome();
      for (const argv of [
        ["webfetch", url, ...args],
        ["tool", "webfetch", url, ...args],
      ]) {
        rmSync(marker, { force: true });
        const routed = await runAsync(context.shim, argv, { env: shimEnv(context, optInEnv) });
        assert.equal(routed.status, 0, `shim ${JSON.stringify(argv)}: ${routed.stderr.toString()}`);
        assert.equal(Buffer.compare(routed.stdout, expected), 0, `shim ${JSON.stringify(argv)}: TS bytes`);
        assert.equal(Buffer.compare(routed.stdout, baseline.stdout), 0, `shim ${JSON.stringify(argv)}: byte-identical to plain TS`);
        assert.equal(existsSync(marker), false, `shim ${JSON.stringify(argv)} must not start Go`);
      }
    }
  } finally {
    await new Promise((resolvePromise) => server.close(resolvePromise));
  }
}

function installFakeHome(name = "home with spaces ü") {
  installFakeHome.contexts ??= new Map();
  if (installFakeHome.contexts.has(name)) return installFakeHome.contexts.get(name);
  const home = join(ROOT_FIXTURE, name);
  const nvm = join(ROOT_FIXTURE, "fake nvm");
  const nodeDir = join(nvm, "versions", "node", `v${process.versions.node}`, "bin");
  mkdirSync(nodeDir, { recursive: true });
  if (!existsSync(join(nodeDir, "node"))) symlinkSync(process.execPath, join(nodeDir, "node"));
  mkdirSync(join(nvm, "alias"), { recursive: true });
  writeFileSync(join(nvm, "alias", "default"), `${process.versions.node}\n`);
  mkdirSync(join(home, ".claude"), { recursive: true });

  const result = spawnSync(process.execPath, [INSTALLER, "--agent", "claude"], {
    cwd: ROOT,
    env: {
      ...cleanEnv(),
      HOME: home,
      NVM_DIR: nvm,
      CLAUDE_CONFIG_DIR: join(home, ".claude"),
    },
    encoding: "utf8",
    timeout: 30000,
  });
  assert.equal(result.status, 0, result.stderr);
  const context = { home, nvm, shim: join(home, ".local", "bin", "cave-tools") };
  installFakeHome.contexts.set(name, context);
  return context;
}

function shimEnv(context, extra = {}) {
  return {
    ...cleanEnv(),
    HOME: context.home,
    NVM_DIR: context.nvm,
    CLAUDE_CONFIG_DIR: join(context.home, ".claude"),
    CAVE_TOOLS_DATA_DIR: join(ROOT_FIXTURE, "shim-data"),
    ...extra,
  };
}

function runShim(context, args, options = {}) {
  const result = spawnSync(context.shim, args, {
    cwd: options.cwd ?? ROOT,
    env: shimEnv(context, options.env),
    input: options.input,
    encoding: options.encoding === undefined ? "utf8" : options.encoding,
    timeout: options.timeout ?? 15000,
  });
  if (result.error) throw result.error;
  return result;
}

function testLauncherShim() {
  if (!POSIX) {
    console.log("SKIP POSIX launcher shim: not on POSIX");
    return;
  }
  const installerSyntax = spawnSync(process.execPath, ["--check", INSTALLER], { encoding: "utf8" });
  assert.equal(installerSyntax.status, 0, installerSyntax.stderr);
  const context = installFakeHome();
  assert.ok(existsSync(context.shim), "generated cave-tools shim exists");
  assert.equal(statSync(context.shim).mode & 0o777, 0o755, "shim is executable");
  const shim = readFileSync(context.shim, "utf8");
  assert.match(shim, /\[ "\$\{CAVE_TOOLS_IMPL:-\}" = "go" \]/, "exact go comparison");
  const syntax = spawnSync("sh", ["-n", context.shim], { encoding: "utf8" });
  assert.equal(syntax.status, 0, syntax.stderr);
  const goExec = shim.indexOf('exec "$cave_go_bin" "$@"');
  const nodeExec = shim.lastIndexOf("exec ");
  assert.ok(goExec > 0, "shim execs the Go binary");
  assert.ok(nodeExec > goExec, "Go selection precedes the Node launcher");
  assert.match(shim, /exit 127/, "missing Go binary has a hard failure");

  // The shim carries the same TS-routed first arguments as src/impl-switch.ts,
  // decided before the Go exec (bare invocation included).
  assert.match(shim, /if \[ "\$#" -eq 0 \]; then\n  cave_tools_use_go=0/, "bare invocation stays in TS");
  const routedCase = shim.indexOf("mcp|webfetch|websearch) cave_tools_use_go=0");
  assert.ok(routedCase > 0, "shim routes mcp/webfetch/websearch to TS");
  assert.ok(routedCase < goExec, "TS routing precedes the Go exec");
  const toolCase = shim.indexOf("tool)\n      case \"${2:-}\" in");
  assert.ok(toolCase > 0, "shim inspects the second token after `tool`");
  assert.match(shim, /webfetch\|websearch\) cave_tools_use_go=0/, "shim routes tool webfetch/websearch to TS");
  assert.ok(toolCase < goExec, "tool-form TS routing precedes the Go exec");

  const settings = JSON.parse(readFileSync(join(context.home, ".claude", "settings.json"), "utf8"));
  const sessionCommands = (settings.hooks?.SessionStart ?? [])
    .flatMap((group) => group.hooks ?? [])
    .map((hook) => String(hook.command ?? ""));
  assert.ok(
    sessionCommands.includes(`"${context.shim}" status --emit-statusline`),
    "statusline hook quotes the launcher path (spaces safe)",
  );

  const dir = join(ROOT_FIXTURE, "shim");
  const fake = fakeGo(join(dir, "bin with spaces ü"), "cave tools-go ü");
  const marker = join(dir, "marker");
  const cwd = join(ROOT_FIXTURE, "shim cwd with spaces ü");
  mkdirSync(cwd, { recursive: true });

  const ok = runShim(context, ["alpha beta", 'quote"x', "single'quote", "back\\slash", "ünïcode", "", "$HOME", "*"], {
    cwd,
    env: {
      CAVE_TOOLS_IMPL: "go",
      CAVE_TOOLS_GO_BIN: fake,
      CAVE_TOOLS_SWITCH_MARKER: marker,
      CAVE_TOOLS_SWITCH_ENV: "value with spaces ü",
      CAVE_TOOLS_SWITCH_STDERR: "shim stderr ü",
      CAVE_TOOLS_SWITCH_EXIT: "7",
    },
  });
  assert.equal(ok.status, 7, "shim forwards Go exit code");
  assert.equal(existsSync(marker), true);
  assert.equal(
    ok.stdout,
    [
      "argc=8",
      "arg0=alpha beta",
      'arg1=quote"x',
      "arg2=single'quote",
      "arg3=back\\slash",
      "arg4=ünïcode",
      "arg5=",
      "arg6=$HOME",
      "arg7=*",
      `cwd=${cwd}`,
      "env=value with spaces ü",
      "",
    ].join("\n"),
  );
  assert.equal(ok.stderr, "stderr=shim stderr ü\n");

  // Bare invocation and explicit `mcp` start the TS MCP server, not Go.
  for (const argv of [[], ["mcp"]]) {
    rmSync(marker, { force: true });
    const noArgs = runShim(context, argv, {
      env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake, CAVE_TOOLS_SWITCH_MARKER: marker },
      input: "",
    });
    assert.equal(noArgs.status, 0, `${JSON.stringify(argv)}: ${noArgs.stderr}`);
    assert.equal(existsSync(marker), false, `${JSON.stringify(argv)} must not start Go`);
    assert.match(noArgs.stderr, /Cave Tools MCP server running on stdio/, "shim starts the TS MCP server");
  }

  // webfetch/websearch stay in TS: their usage errors come from Node, and the
  // fake Go marker must stay absent.
  for (const [command, missing] of [
    ["webfetch", /cave-tools webfetch: missing required: url/],
    ["websearch", /cave-tools websearch: missing required: query/],
  ]) {
    for (const argv of [[command], ["tool", command]]) {
      rmSync(marker, { force: true });
      const routed = runShim(context, argv, {
        env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake, CAVE_TOOLS_SWITCH_MARKER: marker },
        input: "",
      });
      assert.equal(routed.status, 2, `${JSON.stringify(argv)} stays in TS`);
      assert.match(routed.stderr, missing, `shim ${JSON.stringify(argv)} usage comes from TS`);
      assert.equal(existsSync(marker), false, `${JSON.stringify(argv)} must not start Go`);
    }
  }

  // Representative non-routed forms still exec Go from the shim.
  for (const argv of [["--version"], ["__job-worker", "job", "log", "result", "/bin/sh", "echo"], ["filter", "--json"], ["jobs"]]) {
    rmSync(marker, { force: true });
    const forwarded = runShim(context, argv, {
      env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake, CAVE_TOOLS_SWITCH_MARKER: marker },
    });
    assert.equal(forwarded.status, 0, `${JSON.stringify(argv)}: ${forwarded.stderr}`);
    assert.equal(existsSync(marker), true, `shim ${JSON.stringify(argv)} runs Go`);
    assert.match(forwarded.stdout, new RegExp(`^argc=${argv.length}\\narg0=${argv[0]}\\n`), "argv forwarded");
  }

  // The shim must execute the cwd binary that passed the check, not a PATH
  // lookup for the same slashless name. Compare it with direct Node.
  const bareCwd = join(ROOT_FIXTURE, "shim bare cwd ü");
  fakeGo(bareCwd, "bare go shim");
  const bareEnv = {
    CAVE_TOOLS_IMPL: "go",
    CAVE_TOOLS_GO_BIN: "bare go shim",
    PATH: dirname(process.execPath),
  };
  const directBare = runCli(["--version"], { cwd: bareCwd, env: bareEnv });
  const shimBare = runShim(context, ["--version"], { cwd: bareCwd, env: bareEnv });
  assert.equal(shimBare.status, 0, shimBare.stderr);
  assert.equal(shimBare.stdout, directBare.stdout, "direct and shim resolve the same cwd binary");
  assert.equal(shimBare.stderr, directBare.stderr);

  const cat = fakeCat(join(dir, "cat"));
  const bytes = Buffer.from([0, 1, 2, 10, 13, 255, 65, 0]);
  const piped = runShim(context, ["filter", "--json"], {
    env: { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: cat },
    input: bytes,
    encoding: null,
  });
  assert.equal(piped.status, 0);
  assert.equal(Buffer.compare(piped.stdout, bytes), 0, "shim forwards stdin bytes");

  const missing = runShim(context, ["--version"], {
    env: {
      CAVE_TOOLS_IMPL: "go",
      CAVE_TOOLS_GO_BIN: join(ROOT_FIXTURE, "missing shim ü", "go"),
    },
  });
  assert.equal(missing.status, 127);
  assert.match(missing.stderr, /missing or not executable/);
  assert.doesNotMatch(missing.stderr, /Usage: cave-tools/, "shim does not fall back to Node");

  // Non-go values keep TypeScript. The fake Go marker must stay absent.
  for (const value of [undefined, "Go", "typescript"]) {
    rmSync(marker, { force: true });
    const env = { CAVE_TOOLS_GO_BIN: fake, CAVE_TOOLS_SWITCH_MARKER: marker };
    if (value === undefined) delete env.CAVE_TOOLS_IMPL;
    else env.CAVE_TOOLS_IMPL = value;
    const ts = runShim(context, ["--version"], { env });
    assert.equal(ts.status, 2, `TS default for ${JSON.stringify(value)}`);
    assert.match(ts.stderr, /^Usage: cave-tools /);
    assert.equal(existsSync(marker), false, "Go not started for non-go values");
  }

  if (existsSync(GO_BIN)) {
    const real = runShim(context, ["--version"], { env: { CAVE_TOOLS_IMPL: "go" } });
    assert.equal(real.status, 0, real.stderr);
    assert.match(real.stdout, /^cave-tools-go \d+\.\d+\.\d+\r?\n$/, "shim selects the local Go binary");
  } else {
    console.log("SKIP shim real Go smoke: build go/bin/cave-tools-go first");
  }

  // Static launcher paths must be single-quoted too: a directory containing
  // $, backticks or a single quote must not be shell-expanded.
  const special = installFakeHome("home $dollar `tick` 'quote");
  const specialShim = readFileSync(special.shim, "utf8");
  const nodeExecLine = specialShim
    .split("\n")
    .find((line) => line.startsWith("exec ") && line.includes("cave-node"));
  assert.ok(nodeExecLine, "generated shim has a Node exec line");
  assert.match(nodeExecLine, /^exec '/, "static node path is single-quoted");
  assert.match(nodeExecLine, /^exec '.+' '.+' "\$@"$/, "static node and CLI paths are single-quoted");
  const specialMarker = join(ROOT_FIXTURE, "special marker");
  const specialTs = runShim(special, ["--version"], {
    env: { CAVE_TOOLS_GO_BIN: fake, CAVE_TOOLS_SWITCH_MARKER: specialMarker },
  });
  assert.equal(specialTs.status, 2, specialTs.stderr);
  assert.match(specialTs.stderr, /^Usage: cave-tools /, "special-char HOME reaches TypeScript");
  assert.equal(existsSync(specialMarker), false);
}

async function testShimSignal() {
  if (!POSIX) return;
  const context = installFakeHome();
  const fake = fakeSleeper(join(ROOT_FIXTURE, "shim-signals"));
  for (const signal of ["SIGTERM", "SIGINT"]) {
    const child = spawn(context.shim, ["--version"], {
      cwd: ROOT,
      env: shimEnv(context, { CAVE_TOOLS_IMPL: "go", CAVE_TOOLS_GO_BIN: fake }),
      stdio: ["ignore", "pipe", "pipe"],
    });
    await waitForLine(child.stdout);
    child.kill(signal);
    const { code, signal: closeSignal } = await waitForClose(child);
    assert.equal(code, null, `${signal}: shim execs Go without a wrapper`);
    assert.equal(closeSignal, signal);
  }
}

// runInstaller reruns the Claude installer against an existing fake home.
function runInstaller(context) {
  const result = spawnSync(process.execPath, [INSTALLER, "--agent", "claude"], {
    cwd: ROOT,
    env: {
      ...cleanEnv(),
      HOME: context.home,
      NVM_DIR: context.nvm,
      CLAUDE_CONFIG_DIR: join(context.home, ".claude"),
    },
    encoding: "utf8",
    timeout: 30000,
  });
  assert.equal(result.status, 0, result.stderr);
  return result;
}

function settingsHooks(context) {
  const settings = JSON.parse(
    readFileSync(join(context.home, ".claude", "settings.json"), "utf8"),
  );
  return settings.hooks ?? {};
}

function hookCommands(context, event) {
  return (settingsHooks(context)[event] ?? [])
    .flatMap((group) => group.hooks ?? [])
    .map((hook) => String(hook.command ?? ""));
}

// The statusline badge is refreshed in-process by the MCP server: the
// installer must not register a per-call `status --emit-statusline` hook, must
// remove one left by an older install, and must keep the SessionStart warm-up.
function testStatuslineHookMigration() {
  const context = installFakeHome();
  for (const command of hookCommands(context, "PostToolUse")) {
    assert.doesNotMatch(
      command,
      /emit-statusline/,
      "fresh install must not register a PostToolUse statusline hook",
    );
  }
  assert.ok(
    hookCommands(context, "SessionStart").some((command) => command.includes("emit-statusline")),
    "SessionStart statusline warm-up stays registered",
  );

  // Simulate an upgrade from an installer that registered the per-call hook.
  const settingsPath = join(context.home, ".claude", "settings.json");
  const settings = JSON.parse(readFileSync(settingsPath, "utf8"));
  settings.hooks.PostToolUse ??= [];
  settings.hooks.PostToolUse.push({
    matcher: "mcp__cave-tools__.*",
    hooks: [
      {
        type: "command",
        command: `"${context.shim}" status --emit-statusline`,
        timeout: 3,
        async: true,
      },
    ],
  });
  settings.hooks.PostToolUse.push({
    matcher: "Unrelated",
    hooks: [{ type: "command", command: "true" }],
  });
  writeFileSync(settingsPath, `${JSON.stringify(settings, null, 2)}\n`);

  const upgrade = runInstaller(context);
  assert.match(upgrade.stderr + upgrade.stdout, /legacy PostToolUse statusline hook/);
  const postToolUse = hookCommands(context, "PostToolUse");
  assert.ok(
    !postToolUse.some((command) => command.includes("emit-statusline")),
    "upgrade removes the legacy PostToolUse statusline hook",
  );
  assert.ok(
    postToolUse.includes("true"),
    "upgrade leaves unrelated PostToolUse hooks untouched",
  );
  assert.ok(
    hookCommands(context, "SessionStart").some((command) => command.includes("emit-statusline")),
    "upgrade keeps the SessionStart statusline hook",
  );
  console.log("ok - installer drops legacy PostToolUse statusline hook");
}

// Uninstall removes every cave-tools hook, including the SessionStart
// statusline warm-up.
function testStatuslineHookUninstall() {
  const context = installFakeHome("uninstall home ü");
  const result = spawnSync(process.execPath, [REMOVER, "--agent", "claude"], {
    cwd: ROOT,
    env: {
      ...cleanEnv(),
      HOME: context.home,
      NVM_DIR: context.nvm,
      CLAUDE_CONFIG_DIR: join(context.home, ".claude"),
    },
    encoding: "utf8",
    timeout: 30000,
  });
  assert.equal(result.status, 0, result.stderr);
  const hooks = settingsHooks(context);
  for (const [event, groups] of Object.entries(hooks)) {
    const commands = (groups ?? [])
      .flatMap((group) => group.hooks ?? [])
      .map((hook) => String(hook.command ?? ""));
    assert.ok(
      !commands.some((command) => command.includes("cave-tools")),
      `${event} keeps a cave-tools hook after uninstall: ${commands.join(" | ")}`,
    );
  }
  assert.ok(
    !existsSync(join(context.home, ".claude", "hooks", "cave-tools-statusline.sh")),
    "uninstall removes the statusline script",
  );
  console.log("ok - uninstall removes statusline hooks");
}

async function main() {
  try {
    await testResolutionModule();
    testDirectSwitch();
    await testDirectSignalRelay();
    await testRealGoSmoke();
    await testLiveWebfetchRouting();
    testLauncherShim();
    testStatuslineHookMigration();
    testStatuslineHookUninstall();
    await testShimSignal();
    console.log("impl switch tests passed");
  } finally {
    rmSync(ROOT_FIXTURE, { recursive: true, force: true });
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
