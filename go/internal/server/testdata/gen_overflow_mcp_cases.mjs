// Generates overflow_mcp_cases.json from the built TypeScript MCP server
// (node dist/cli.js, MCP over stdio), so the Go tests compare tool behaviour
// for JSON numbers outside float64 range against the real implementation.
//
// Run from this directory after `pnpm build`:
//   node gen_overflow_mcp_cases.mjs
//
// JSON.parse turns 1e400 into Infinity before the tool runs; the recorded text
// shows how each numeric schema field treats it (read offset/limit, bash
// timeout, edit lines, ls offset, grep/find limits).
import { spawn } from "node:child_process";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../../../../", import.meta.url));
const cli = join(repoRoot, "dist", "cli.js");
const placeholder = "@@DIR@@";

const dir = mkdtempSync(join(tmpdir(), "cave-overflow-mcp-"));
const home = join(dir, "home");
mkdirSync(home);
mkdirSync(join(dir, "files"));
writeFileSync(join(dir, "files", "note.txt"), "a\nb\n");
mkdirSync(join(dir, "files", "sub"));
writeFileSync(join(dir, "files", "sub", "hit.txt"), "root\nother\n");

// `args` is raw JSON text so 1e400 reaches the server literally, the way a
// client that wrote the number (or Node's JSON.stringify of a finite value)
// would send it; JSON.stringify of Infinity itself would write null.
const calls = [
  {
    name: "read-offset-overflow",
    tool: "cave__read",
    args: `{"file_path":"@@DIR@@/files/note.txt","offset":1e400}`,
  },
  {
    name: "read-limit-overflow",
    tool: "cave__read",
    args: `{"file_path":"@@DIR@@/files/note.txt","force":true,"limit":1e400}`,
  },
  {
    name: "read-offset-negative-overflow",
    tool: "cave__read",
    args: `{"file_path":"@@DIR@@/files/note.txt","force":true,"offset":-1e400}`,
  },
  {
    name: "bash-timeout-overflow",
    tool: "cave__bash",
    args: `{"command":"echo hi","timeout":1e400}`,
  },
  {
    name: "edit-start-line-overflow",
    tool: "cave__edit",
    args: `{"file_path":"@@DIR@@/files/note.txt","start_line":1e400,"end_line":2,"content":"z"}`,
  },
  {
    name: "ls-offset-overflow",
    tool: "cave__ls",
    args: `{"path":"@@DIR@@/files","offset":1e400}`,
  },
  {
    name: "grep-limit-overflow",
    tool: "cave__grep",
    args: `{"pattern":"root","path":"@@DIR@@/files","limit":1e400}`,
  },
  {
    name: "grep-context-overflow",
    tool: "cave__grep",
    args: `{"pattern":"root","path":"@@DIR@@/files","context":1e400}`,
  },
  {
    name: "find-limit-overflow",
    tool: "cave__find",
    args: `{"pattern":"note.txt","path":"@@DIR@@/files","limit":1e400}`,
  },
];

const child = spawn(process.execPath, [cli], {
  cwd: repoRoot,
  env: {
    PATH: process.env.PATH,
    HOME: home,
    TMPDIR: join(home, "tmp"),
    CAVE_TOOLS_DATA_DIR: join(home, "data"),
  },
  stdio: ["pipe", "pipe", "ignore"],
});
const lines = createInterface({ input: child.stdout });
const pending = new Map();
lines.on("line", (line) => {
  let message;
  try {
    message = JSON.parse(line);
  } catch {
    return;
  }
  const waiter = pending.get(message.id);
  if (waiter) {
    pending.delete(message.id);
    waiter(message);
  }
});
const send = (frame) => child.stdin.write(frame + "\n");
const exchange = (id, frame) =>
  new Promise((resolve, reject) => {
    pending.set(id, resolve);
    send(frame);
    setTimeout(() => reject(new Error(`timeout waiting for id ${id}`)), 20000);
  });

const initialize =
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"overflow","version":"1"}}}';

try {
  await exchange(1, initialize);
  send('{"jsonrpc":"2.0","method":"notifications/initialized"}');

  const recorded = [];
  for (let i = 0; i < calls.length; i++) {
    const testCase = calls[i];
    const id = i + 2;
    const frame =
      `{"jsonrpc":"2.0","id":${id},"method":"tools/call","params":{"name":"${testCase.tool}","arguments":${testCase.args}}}`;
    const sent = frame.split("@@DIR@@").join(dir);
    const response = await exchange(id, sent);
    const result = response.result ?? {};
    const content = Array.isArray(result.content) ? result.content : [];
    const first = content[0] ?? {};
    recorded.push({
      name: testCase.name,
      frame,
      isError: result.isError === true,
      text: typeof first.text === "string" ? first.text.split(dir).join(placeholder) : "",
    });
  }

  const nodeVersion = execFileSync("node", ["--version"], { encoding: "utf8" }).trim();
  writeFileSync(
    new URL("./overflow_mcp_cases.json", import.meta.url),
    JSON.stringify({ nodeVersion, template: "node dist/cli.js (MCP stdio)", cases: recorded }, null, 1) + "\n",
  );
  console.log(`wrote ${recorded.length} MCP overflow cases for ${nodeVersion}`);
} finally {
  child.kill();
  rmSync(dir, { recursive: true, force: true });
}
