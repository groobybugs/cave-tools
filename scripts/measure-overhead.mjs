#!/usr/bin/env node
// Measure fixed per-session token overhead of cave-tools:
//   - tools/list JSON size per tool (as the MCP server serves it)
//   - SessionStart / SubagentStart ruleset text (as the hooks emit it)
//   - per-turn UserPromptSubmit reminder text (as the hook emits it)
// Hooks run in a throwaway CLAUDE_CONFIG_DIR so no real flag file is touched.
// Usage: node scripts/measure-overhead.mjs  (needs `pnpm run build` first)

import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const hooksDir = join(root, "claude", "hooks");
const tok = (chars) => Math.round(chars / 4);

function listTools() {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [join(root, "dist", "cli.js"), "mcp"], {
      cwd: root,
      env: process.env,
      stdio: ["pipe", "pipe", "ignore"],
    });
    let buf = "";
    const timer = setTimeout(() => {
      child.kill();
      reject(new Error("timed out waiting for tools/list"));
    }, 15000);
    child.stdout.on("data", (chunk) => {
      buf += chunk;
      let nl;
      while ((nl = buf.indexOf("\n")) !== -1) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        const msg = JSON.parse(line);
        if (msg.id === 1) {
          child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
          child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list" }) + "\n");
        } else if (msg.id === 2) {
          clearTimeout(timer);
          child.kill();
          resolve(msg.result.tools);
        }
      }
    });
    child.on("error", reject);
    child.stdin.write(JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2025-06-18",
        capabilities: {},
        clientInfo: { name: "measure-overhead", version: "0" },
      },
    }) + "\n");
  });
}

function runHook(name, mode, configDir, stdin) {
  const out = execFileSync(process.execPath, [join(hooksDir, name)], {
    input: stdin,
    encoding: "utf8",
    env: { ...process.env, CLAUDE_CONFIG_DIR: configDir, CAVE_TOOLS_MODE: mode },
  });
  if (!out.trim().startsWith("{")) return "";
  return JSON.parse(out).hookSpecificOutput?.additionalContext ?? "";
}

const tools = await listTools();
const rows = tools
  .map((t) => ({ name: t.name, chars: JSON.stringify(t).length }))
  .sort((a, b) => b.chars - a.chars);
const total = JSON.stringify({ tools }).length;

console.log(`tools/list (profile=${process.env.CAVE_TOOLS_PROFILE || "standard"}): ${tools.length} tools`);
for (const r of rows) console.log(`  ${r.name.padEnd(20)} ${String(r.chars).padStart(6)}`);
console.log(`  ${"TOTAL".padEnd(20)} ${String(total).padStart(6)} chars  ~${tok(total)} tok`);

const configDir = mkdtempSync(join(tmpdir(), "cave-measure-"));
try {
  // Pretend the statusline is already configured so the one-off setup nudge
  // does not inflate the SessionStart number.
  writeFileSync(join(configDir, "settings.json"), JSON.stringify({
    statusLine: { type: "command", command: "bash statusline-wrapper.sh" },
  }));
  console.log("\nhooks (bytes, ~tok):");
  for (const mode of ["hint", "enforce", "strict"]) {
    const session = runHook("cave-tools-activate.js", mode, configDir, "{}");
    const subagent = runHook("cave-tools-subagent.js", mode, configDir, "{}");
    // activate.js wrote the flag for `mode`; the tracker reads it back.
    const reminder = runHook("cave-tools-mode-tracker.js", mode, configDir, JSON.stringify({ prompt: "hello" }));
    const b = (s) => Buffer.byteLength(s, "utf8");
    console.log(
      `  ${mode.padEnd(8)} SessionStart ${b(session)} (~${tok(b(session))})  ` +
      `SubagentStart ${b(subagent)} (~${tok(b(subagent))})  ` +
      `per-turn reminder ${b(reminder)} (~${tok(b(reminder))})`,
    );
  }
} finally {
  rmSync(configDir, { recursive: true, force: true });
}
