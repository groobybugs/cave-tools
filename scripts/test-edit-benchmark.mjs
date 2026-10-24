#!/usr/bin/env node
// Deterministic tests for bench-edit-invocation.mjs. No model calls, no live
// servers (only a local dummy sleep process for the timeout path).
// Run: node scripts/test-edit-benchmark.mjs (from the repo root)

import { mkdtempSync, mkdirSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import {
  parseArgs, parseEventLine, summarizeEvents, normalizeTool, isEditTool,
  isReadTool, buildPrompt, findUnexpected, sha1, runChild, stopServer,
  profileConfig,
} from "./bench-edit-invocation.mjs";
mkdirSync(join(tmpdir(), "opencode"), { recursive: true, mode: 0o700 });


let pass = 0, fail = 0;
const bad = [];
function ok(name, cond, detail = "") {
  if (cond) pass++;
  else { fail++; bad.push(name); console.error(`FAIL ${name} ${detail}`); }
}

// Shape mirrors verified `opencode run --format json` output.
const ev = (o) => JSON.stringify(o);
const toolUse = (id, tool, status, input) => ev({
  type: "tool_use", timestamp: 1, sessionID: "s",
  part: { type: "tool", id, tool, state: { status, input, output: "x" } },
});
const finish = (tokens) => ev({
  type: "step_finish", timestamp: 2, sessionID: "s",
  part: { type: "step-finish", reason: "tool-calls", tokens },
});

// 1. parseEventLine
ok("parse-valid", parseEventLine(toolUse("c1", "edit", "completed", {}))?.type === "tool_use");
ok("parse-blank", parseEventLine("   ") === null);
ok("parse-garbage", parseEventLine("not json {") === null);
ok("parse-scalar", parseEventLine("42") === null);

// 2. exact tool matching — no substring matching
ok("edit-native", isEditTool("edit") && isEditTool("write") && isEditTool("patch") && isEditTool("apply_patch"));
ok("edit-cave", isEditTool("cave__edit") && isEditTool("cave__write") && isEditTool("cave__apply_patch"));
ok("edit-qualified", isEditTool("cave-tools_cave__edit") && isEditTool("cave-tools_cave__write"));
ok("edit-narrow", !isEditTool("read") && !isEditTool("shell") && !isEditTool("credential")
  && !isEditTool("multiedit") && !isEditTool("editor") && !isEditTool("replacement"));
ok("norm-segment", normalizeTool("cave-tools_cave__edit") === "edit" && normalizeTool("read") === "read");
ok("read-exact", isReadTool("read") && isReadTool("cave__read") && !isReadTool("thread"));

// 3. happy path: read then one completed edit, full token set
{
  const events = [
    parseEventLine(toolUse("call-1", "read", "completed", { path: "f" })),
    parseEventLine(toolUse("call-2", "edit", "completed", { path: "f", oldString: "a", newString: "b" })),
    parseEventLine(finish({ input: 100, output: 5, reasoning: 1, cache: { read: 50, write: 0 } })),
  ];
  const s = summarizeEvents(events);
  ok("sum-edits", s.editAttempts === 1 && s.editErrors === 0);
  ok("sum-reads", s.readAttempts === 1);
  ok("sum-first-succeeded", s.firstEditSucceeded === true);
  ok("sum-valid-observed", s.firstEditValid === true);
  ok("sum-tokens", s.tokens.input === 100 && s.tokens.cacheRead === 50 && s.tokens.cacheWrite === 0);
  ok("sum-no-event-error", s.eventError === null);
}

// 4. lifecycle dedup: running then completed for one call id counts once
{
  const events = [
    parseEventLine(toolUse("call-9", "edit", "running", { path: "f", oldString: "a", newString: "b" })),
    parseEventLine(toolUse("call-9", "edit", "completed", { path: "f", oldString: "a", newString: "b" })),
  ];
  const s = summarizeEvents(events);
  ok("dedup-once", s.editAttempts === 1 && s.editErrors === 0);
  ok("dedup-first", s.firstEditSucceeded === true && s.toolUses.length === 1);
}

// 5. malformed edit input still counts as a recognized call; validity unknown
{
  const events = [parseEventLine(toolUse("call-3", "edit", "error", { path: "f" }))];
  const s = summarizeEvents(events);
  ok("malformed-counts", s.editAttempts === 1 && s.editErrors === 1);
  ok("malformed-first", s.firstEditSucceeded === false && s.firstEditValid === null);
}

// 6. per-field token nulls: absent output/cache stay null, not 0
{
  const s = summarizeEvents([parseEventLine(finish({ input: 7 }))]);
  ok("tokens-partial", s.tokens.input === 7 && s.tokens.output === null
    && s.tokens.reasoning === null && s.tokens.cacheRead === null && s.tokens.cacheWrite === null);
  const n = summarizeEvents([parseEventLine(ev({ type: "text", part: { type: "text", text: "x" } }))]);
  ok("tokens-absent", n.tokens.input === null && n.tokens.output === null);
}
// Partial totals must not silently sum only the steps with available fields.
{
  const summary = summarizeEvents([
    parseEventLine(finish({ input: 7 })), parseEventLine(finish({ input: 5, output: 3 })),
  ]);
  ok("tokens-incomplete-total", summary.tokens.input === 12 && summary.tokens.output === null);
}
// Known input rejection versus non-schema failures; reflected input never escapes.
{
  const rejected = parseEventLine(toolUse("bad", "edit", "error", {}));
  rejected.part.state.error = 'Invalid arguments for tool "edit":\\n- path: Missing key\\nArguments provided: SECRET_REPLACEMENT';
  const summary = summarizeEvents([rejected]);
  ok("validator-first-invalid", summary.firstEditValid === false && summary.firstEditSucceeded === false);
  ok("validator-no-reflection", !JSON.stringify(summary).includes("SECRET_REPLACEMENT"));
  const other = summarizeEvents([{ type: "error", error: { type: "SECRET_REPLACEMENT" } }]);
  ok("unknown-error-finite", other.eventError === "event-error");
  ok("foreign-tool-not-edit", !isEditTool("unrelated__edit") && !isReadTool("unrelated__read"));
}


// 7. event-level error maps to a finite code, no raw text
{
  const s = summarizeEvents([parseEventLine(ev({ type: "error", error: { type: "provider.no-route", message: "M unavailable: x" } }))]);
  ok("event-code", s.eventError === "event-error:provider.no-route");
}

// 8. profile configs: codemode false, no deprecated enabled key
{
  const d = mkdtempSync(join(tmpdir(), "opencode", "bench-cfg-"));
  try {
    const trial = { root: d, claudeDir: join(d, "c"), cfgDir: join(d, "g") };
    mkdirSync(trial.claudeDir, { recursive: true });
    mkdirSync(trial.cfgDir, { recursive: true });
    const native = profileConfig("native-only", trial);
    ok("cfg-native", Object.keys(native.mcp.servers).length === 0 && native.plugins.length === 0);
    const mixed = profileConfig("mixed", trial);
    const mcp = mixed.mcp.servers["cave-tools"];
    ok("cfg-codemode", mcp.codemode === false && !("enabled" in mcp));
    ok("cfg-mcp-cmd", mcp.command[0] === process.execPath && mcp.command[2] === "mcp");
    ok("cfg-cave-env", mcp.environment.CAVE_TOOLS_DAEMON === "0");
    const cave = profileConfig("cave-only", trial);
    ok("cfg-plugin", cave.plugins.length === 1 && cave.mcp.servers["cave-tools"].codemode === false);
    let threw = false;
    try { profileConfig("nope", trial); } catch { threw = true; }
    ok("cfg-unknown-throws", threw);
  } finally { rmSync(d, { recursive: true, force: true }); }
}

// 9. args: --db passthrough, unknown profile/arg rejected
{
  const a = parseArgs(["--models", "m1", "--profiles", "mixed", "--repeats", "2", "--timeout-ms", "5000", "--db", "/x/y.db"]);
  ok("args-ok", a.models.join() === "m1" && a.db === "/x/y.db" && a.repeats === 2 && a.timeoutMs === 5000);
  let tp = false, ta = false;
  try { parseArgs(["--profiles", "nope"]); } catch { tp = true; }
  try { parseArgs(["--bogus"]); } catch { ta = true; }
  ok("args-reject", tp && ta);
}

// 10. fixture + hash + prompt helpers
{
  const d = mkdtempSync(join(tmpdir(), "opencode", "bench-fix-"));
  try {
    mkdirSync(join(d, "w"), { recursive: true });
    writeFileSync(join(d, "w", "target.txt"), "x");
    ok("unexpected-clean", findUnexpected(join(d, "w")).length === 0);
    writeFileSync(join(d, "w", "extra.js"), "y");
    ok("unexpected-names", JSON.stringify(findUnexpected(join(d, "w"))) === JSON.stringify(["extra.js"]));
    ok("sha1", sha1("abc").length === 40 && sha1("abc") === sha1("abc"));
    ok("prompt", buildPrompt("/f/target.txt").includes("/f/target.txt"));
  } finally { rmSync(d, { recursive: true, force: true }); }
}

// 11. timeout kills only the owned child group; fast child passes through
{
  const r = await runChild([process.execPath, "-e", "setInterval(()=>{},1000)"],
    { stdio: ["ignore", "pipe", "ignore"] }, 1500);
  ok("kill-timeout", r.timedOut === true && r.error === "timeout" && Number.isFinite(r.elapsedMs));
  const q = await runChild([process.execPath, "-e", "console.log('hi')"],
    { stdio: ["ignore", "pipe", "ignore"] }, 10000);
  ok("child-ok", q.error === null && !q.timedOut && q.out.includes("hi"));
  await stopServer(null);
  ok("stop-null", true);
}

// 12. bench source self-audit: no direct DB access, no process scans,
//     no secret-bearing logic, no raw-message persistence
{
  const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "bench-edit-invocation.mjs"), "utf8");
  const banned = ["better-sqlite", "/proc", "ATTACH", "DETACH", "sqlite_master", "liveDb",
    "Serves", "reap", "waitForTable", "message", "pgrep", "credential"];
  const hits = banned.filter((b) => src.includes(b));
  ok("src-audit", hits.length === 0, hits.join(","));
  ok("src-server-flag", src.includes('"serve"') && src.includes("OPENCODE_PASSWORD"));
  ok("src-db-flag", src.includes("benchmarkDb") && src.includes("OPENCODE_DB"));
}

console.log(`\ntest-edit-benchmark: pass=${pass} fail=${fail}`);
if (fail) { console.error("failures: " + bad.join(", ")); process.exit(1); }
