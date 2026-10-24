#!/usr/bin/env node
// Tests for scripts/analyze-usage.mjs — Claude route classification and the
// OpenCode SQLite analyzer. Uses isolated temp fixtures under /tmp/opencode
// (never the developer's real ~/.claude or OpenCode DB). No network. Spawns
// the analyzer with --json and asserts on the parsed report.

import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT = fileURLToPath(new URL("./analyze-usage.mjs", import.meta.url));
const PREFERRED = process.platform === "linux" ? "/tmp/opencode" : tmpdir();

let failures = 0;
let passed = 0;
const pass = (name) => {
  passed += 1;
  console.log(`  ok ${name}`);
};
const fail = (name, detail) => {
  failures += 1;
  console.error(`  FAIL ${name}${detail ? `: ${detail}` : ""}`);
};

function assert(cond, name, detail) {
  if (cond) pass(name);
  else fail(name, detail);
}

function fixtureRoot() {
  try {
    mkdirSync(PREFERRED, { recursive: true, mode: 0o700 });
    return mkdtempSync(join(PREFERRED, "usage-analyzer-"));
  } catch {
    return mkdtempSync(join(tmpdir(), "usage-analyzer-"));
  }
}

function runClaude(root) {
  const out = execFileSync(process.execPath, [SCRIPT, "--json"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, CLAUDE_CONFIG_DIR: join(root, "claude") },
  });
  return JSON.parse(out);
}

function runClaudeArgs(args, root) {
  return spawnSync(process.execPath, [SCRIPT, "--json", ...args], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, CLAUDE_CONFIG_DIR: join(root, "claude") },
  });
}

// Explicit line builders for Claude JSONL fixtures.
function assistantUse(sessionId, cwd, timestamp, toolUse) {
  return JSON.stringify({
    type: "assistant",
    sessionId,
    cwd,
    timestamp,
    message: { id: `msg_${sessionId}`, content: [{ type: "tool_use", id: toolUse.id, name: toolUse.name, input: toolUse.input }] },
  });
}
function assistantUsage(sessionId, cwd, timestamp, usage) {
  return JSON.stringify({
    type: "assistant",
    sessionId,
    cwd,
    timestamp,
    message: {
      id: `msg_${sessionId}`,
      content: [],
      usage: {
        input_tokens: usage.input || 0,
        output_tokens: usage.output || 0,
        cache_creation_input_tokens: 0,
        cache_read_input_tokens: 0,
      },
    },
  });
}
function userResult(sessionId, cwd, timestamp, useId, chars, isError) {
  return JSON.stringify({
    type: "user",
    sessionId,
    cwd,
    timestamp,
    message: {
      id: `ures_${useId}`,
      content: [
        {
          type: "tool_result",
          tool_use_id: useId,
          is_error: isError === true,
          content: [{ type: "text", text: "x".repeat(chars) }],
        },
      ],
    },
  });
}

// ---------------------------------------------------------------- fixtures
const root = fixtureRoot();
const claudeDir = join(root, "claude", "projects", "proj");
mkdirSync(claudeDir, { recursive: true });

const TS = "2026-06-15T10:00:00.000Z";
const cwd = "/home/dev/work";

writeFileSync(
  join(claudeDir, "s1.jsonl"),
  [
    assistantUsage("s1", cwd, TS, { input: 100, output: 50 }),
    assistantUse("s1", cwd, TS, { id: "u1", name: "mcp__cave-tools__cave__bash", input: { command: "ls" } }),
    userResult("s1", cwd, TS, "u1", 200, false),
    assistantUse("s1", cwd, TS, { id: "u2", name: "Bash", input: { command: "ls -la" } }),
    userResult("s1", cwd, TS, "u2", 300, false),
    assistantUse("s1", cwd, TS, { id: "u3", name: "Bash", input: { command: "cave-tools bash ls" } }),
    userResult("s1", cwd, TS, "u3", 40, false),
    assistantUse("s1", cwd, TS, { id: "u4", name: "Bash", input: { command: "RAWDATA=1 node ./bin/cave-tools filter" } }),
    userResult("s1", cwd, TS, "u4", 60, true),
    assistantUse("s1", cwd, TS, { id: "u5", name: "Bash", input: { command: "cd /x && cave-tools bash ls" } }),
    userResult("s1", cwd, TS, "u5", 70, false),
    assistantUse("s1", cwd, TS, { id: "u6", name: "cave__read", input: {} }),
    userResult("s1", cwd, TS, "u6", 1000, false),
    assistantUse("s1", cwd, TS, { id: "u7", name: "Bash", input: { command: "cave-tools status" } }),
    userResult("s1", cwd, TS, "u7", 20, false),
    "",
  ].join("\n"),
);

// ------------------------------------------------------- Claude validation
{
  const badSource = spawnSync(process.execPath, [SCRIPT, "--source", "bogus"], { encoding: "utf8" });
  assert(badSource.status !== 0, "rejects invalid --source");
  const badDb = spawnSync(process.execPath, [SCRIPT, "--db", "/tmp/x.db"], { encoding: "utf8" });
  assert(badDb.status !== 0, "rejects --db with Claude");
  const defSource = spawnSync(process.execPath, [SCRIPT, "--source", "boguses"], { encoding: "utf8" });
  assert(defSource.status !== 0, "rejects typo'd --source value");
}

// ------------------------------------------------------------ Claude report
try {
  const r = runClaude(root);
  assert(r.totals && r.sessions && r.files === 1 && r.lines === 15, "claude report shape preserved", JSON.stringify(r).slice(0, 200));
  assert(r.tool_result_chars === 1690, "claude result chars", String(r.tool_result_chars));

  const byName = (n) => r.tools.find((t) => t.name === n) || {};
  assert(byName("cave__bash").calls === 1 && byName("cave__bash").route === "mcp-bash", "cave__bash row route mcp-bash",
    JSON.stringify(byName("cave__bash")));
  const bashRow = byName("Bash");
  assert(bashRow.calls === 5 && bashRow.results === 5 && bashRow.errors === 1, "Bash row counts",
    JSON.stringify(bashRow));
  assert(bashRow.route === "mixed", "mixed tool routes are not hidden by a majority label", JSON.stringify(bashRow));
  assert(byName("cave__read").route === "other", "non-shell tool route other", JSON.stringify(byName("cave__read")));

  const R = r.routes || {};
  assert(R["mcp-bash"]?.calls === 1, "routes mcp-bash calls", JSON.stringify(R));
  assert(R["cli-bash"]?.calls === 1, "routes cli-bash calls");
  assert(R["cli-filter"]?.calls === 1, "routes cli-filter calls (node ./bin/cave-tools filter)");
  assert(R["native-shell"]?.calls === 3, "routes native-shell calls (plain + non-anchored + non-filter CLI)", JSON.stringify(R["native-shell"]));
  assert(R["native-shell"]?.errors === 0, "native-shell errors counted");
  assert(R["native-shell"]?.total_chars === 390, "native-shell chars 300+70+20", JSON.stringify(R["native-shell"]));
  // cli-bash result 40 chars; cli-filter error text 60 chars; mcp-bash 200.
  assert(R["cli-bash"]?.total_chars === 40, "cli-bash chars");
  assert(R["cli-filter"]?.total_chars === 60, "cli-filter chars");
  assert(R["cli-filter"]?.errors === 1, "cli-filter errors");
  assert(R["other"]?.total_chars === 1000, "other chars (cave__read)");
} catch (e) {
  fail("claude report", e.message);
}

// ------------------------------------------------------------- Claude filters
{
  const r = runClaudeArgs(["--project", "/nope"], root);
  const rep = JSON.parse(r.stdout);
  assert(rep.sessions.length === 0 && rep.tools.length === 0, "claude --project mismatch filters everything",
    JSON.stringify(rep.sessions.length));
  const r2JSON = runClaudeArgs(["--since", "2026-06-15", "--until", "2026-06-16"], root);
  const r2 = JSON.parse(r2JSON.stdout);
  assert(r2.sessions.length === 1, "claude --since/--until inclusive window");
}

// ------------------------------------------------------------ OpenCode DB
const Database = (await import("better-sqlite3")).default;
const dbFile = join(root, "opencode", "opencode.db");
mkdirSync(join(root, "opencode"), { recursive: true });
{
  const db = new Database(dbFile);
  db.exec("CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL);");
  db.exec(
    "CREATE TABLE part (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL);",
  );
  db.prepare("INSERT INTO session (id, directory) VALUES (?, ?)").run("sess-1", "/home/dev/work");
  db.prepare("INSERT INTO session (id, directory) VALUES (?, ?)").run("sess-2", "/other/place");
  db.prepare("INSERT INTO session (id, directory) VALUES (?, ?)").run("sess-3", "/home/dev/other-project");
  db.prepare("INSERT INTO session (id, directory) VALUES (?, ?)").run("sess-4", "/home/dev/work");

  const TIME = Date.parse("2026-06-16T10:00:00.000Z");
  const inserts = [
    // completed shell call
    ["p1", "sess-1", TIME, { type: "tool", tool: "bash", state: { status: "completed", input: { command: "ls" }, output: "a".repeat(500) } }],
    // cave-tools MCP bash (opencode plugin naming)
    ["p2", "sess-1", TIME + 1000, { type: "tool", tool: "cave-tools_cave__bash", state: { status: "completed", input: { command: "ls" }, output: "b".repeat(300) } } ],
    // error tool
    ["p3", "sess-1", TIME + 2000, { type: "tool", tool: "bash", state: { status: "error", input: { command: "exit 1" }, error: "E".repeat(120) } }],
    // cli-bash anchored invocation
    ["p4", "sess-1", TIME + 3000, { type: "tool", tool: "shell", state: { status: "completed", input: { command: "FOO=1 ./bin/cave-tools bash ls" }, output: "c".repeat(70) } }],
    // cli-filter invocation
    ["p5", "sess-1", TIME + 4000, { type: "tool", tool: "shell", state: { status: "completed", input: { command: "cave-tools filter" }, output: "d".repeat(40) } }],
    // non-shell
    ["p6", "sess-1", TIME + 5000, { type: "tool", tool: "cave-tools_cave__read", state: { status: "completed", input: {}, output: "e".repeat(1000) } }],
    // pending tool → call but no result
    ["p7", "sess-2", TIME + 6000, { type: "tool", tool: "bash", state: { status: "running", input: { command: "sleep" } } }],
    // MCP normalization: claude-style mcp__ server prefix
    ["p8", "sess-2", TIME + 7000, { type: "tool", tool: "mcp__foo_cave-tools__cave__grep", state: { status: "completed", input: {}, output: "f".repeat(200) } }],
    // non-tool part (assistant text) — counted as part, not tool call
    ["p9", "sess-1", TIME + 8000, { type: "text", text: "hi" }],
    // malformed JSON row
    ["p10", "sess-1", TIME + 9000, "not-json{{{"],
    // session under a different project dir
    ["p11", "sess-3", TIME + 500, { type: "text", text: "excluded dir content" }],
    // second session in the same directory still counts as its own session
    ["p12", "sess-4", TIME + 10000, { type: "text", text: "same cwd, different session" }],
  ];
  const ins = db.prepare("INSERT INTO part (id, session_id, time_created, data) VALUES (?, ?, ?, ?)");
  for (const [id, sid, ts, data] of inserts) ins.run(id, sid, ts, typeof data === "string" ? data : JSON.stringify(data));
  db.close();
}

function runOpencode(args) {
  return spawnSync(process.execPath, [SCRIPT, "--source", "opencode", "--db", dbFile, "--json", ...args], {
    cwd: root,
    encoding: "utf8",
  });
}

try {
  const res = runOpencode([]);
  assert(res.status === 0, "opencode analyzer runs", res.stderr);
  const r = JSON.parse(res.stdout);
  assert(r.source === "opencode" && r.database === dbFile, "opencode report identity");
  assert(r.sessions === 4, "opencode sessions counted by id, not directory", String(r.sessions));
  assert(r.parts === 12, "opencode parts in range", String(r.parts));
  assert(r.malformed_parts === 1, "opencode malformed parts", String(r.malformed_parts));
  assert(r.tool_calls === 8, "opencode tool calls", String(r.tool_calls));
  assert(r.tool_results === 7, "opencode results (completed+error)", String(r.tool_results));
  assert(r.tool_errors === 1, "opencode errors", String(r.tool_errors));
  // chars: completed outputs 500+300+70+40+1000+200 = 2110, error 120
  assert(r.tool_result_chars === 2230, "opencode result chars incl error.error", String(r.tool_result_chars));

  const byName = (n) => r.tools.find((t) => t.name === n) || {};
  assert(!!byName("cave__bash"), "opencode cave__bash normalization", JSON.stringify(r.tools?.map((t) => t.name)));
  assert(byName("cave__bash").route === "mcp-bash", "opencode cave__bash route");
  assert(byName("bash").route === "native-shell", "opencode bash native route", JSON.stringify(byName("bash")));
  assert(byName("shell").route === "mixed", "opencode shell row reports mixed routes", JSON.stringify(byName("shell")));
  assert(!!byName("cave__grep"), "opencode mcp__ normalization to cave__", JSON.stringify(byName("cave__grep")));
  assert(r.groups?.cave?.calls === 3, "opencode cave group calls (bash+read+grep)", JSON.stringify(r.groups?.cave));

  const R = r.routes || {};
  assert(R["native-shell"]?.calls === 3, "opencode routes native-shell calls (ls + error + running)", JSON.stringify(R["native-shell"]));
  assert(R["native-shell"]?.total_chars === 620, "opencode native-shell chars (500+120)", JSON.stringify(R["native-shell"]));
  assert(R["mcp-bash"]?.calls === 1, "opencode routes mcp-bash");
  assert(R["mcp-bash"]?.total_chars === 300, "opencode mcp-bash chars");
  assert(R["cli-bash"]?.calls === 1 && R["cli-bash"]?.total_chars === 70, "opencode routes cli-bash", JSON.stringify(R["cli-bash"]));
  assert(R["cli-filter"]?.calls === 1 && R["cli-filter"]?.total_chars === 40, "opencode routes cli-filter");
  assert(R["other"]?.calls === 2 && R["other"]?.total_chars === 1200, "opencode routes other (read+grep)", JSON.stringify(R["other"]));
  // running tool: counted as call but not result
  assert(byName("bash").calls === 3 && byName("bash").results === 2 && byName("bash").errors === 1,
    "running tool call counted, no result",
    JSON.stringify(byName("bash")));
} catch (e) {
  fail("opencode report", e.message);
}

// ------------------------------------------------------- OpenCode filters
try {
  const proj = JSON.parse(runOpencode(["--project", "/other/place"]).stdout);
  assert(proj.sessions === 1 && proj.parts === 2 && proj.tool_calls === 2, "opencode --project filters by session.directory",
    JSON.stringify({ sessions: proj.sessions, parts: proj.parts, calls: proj.tool_calls }));

  const since = JSON.parse(runOpencode(["--since", "2026-06-16", "--until", "2026-06-17"]).stdout);
  assert(since.parts === 12, "opencode --since/--until on time_created", String(since.parts));
  const future = JSON.parse(runOpencode(["--since", "2027-01-01"]).stdout);
  assert(future.parts === 0, "opencode --since future excludes everything", String(future.parts));
} catch (e) {
  fail("opencode filters", e.message);
}

// ---------------------------------------------------- OpenCode validation / missing DB
{
  const missing = spawnSync(process.execPath, [SCRIPT, "--source", "opencode", "--db", join(root, "no-such.db")], {
    cwd: root,
    encoding: "utf8",
  });
  assert(missing.status !== 0 && /cannot read/i.test(missing.stderr), "opencode missing DB fails cleanly", missing.stderr);
  const noDb = spawnSync(process.execPath, [SCRIPT, "--source", "opencode"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, OPENCODE_DB_PATH: "", OPENCODE_DB: "", XDG_DATA_HOME: join(root, "xdg-empty") },
  });
  assert(noDb.status !== 0, "opencode default path missing fails cleanly", noDb.stderr);
  // OPENCODE_DB_PATH is honored without --db
  const envRun = spawnSync(process.execPath, [SCRIPT, "--source", "opencode", "--json"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, OPENCODE_DB_PATH: dbFile },
  });
  assert(envRun.status === 0 && envRun.stdout.includes("opencode.db"), "OPENCODE_DB_PATH honored", envRun.stderr);
  const officialRun = spawnSync(process.execPath, [SCRIPT, "--source", "opencode", "--json"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, OPENCODE_DB_PATH: "", OPENCODE_DB: dbFile },
  });
  assert(officialRun.status === 0 && officialRun.stdout.includes("opencode.db"), "official OPENCODE_DB honored", officialRun.stderr);
}

// --------------------------------------------------------------- text mode
{
  const text = spawnSync(process.execPath, [SCRIPT, "--source", "opencode", "--db", dbFile, "--top", "3"], {
    cwd: root,
    encoding: "utf8",
  });
  assert(text.status === 0 && /SHELL ROUTES/.test(text.stdout) && /post-filtered/.test(text.stdout),
    "opencode text output shows routes + post-filter note", text.stdout.slice(0, 200));
  const ctext = spawnSync(process.execPath, [SCRIPT, "--top", "3"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, CLAUDE_CONFIG_DIR: join(root, "claude") },
  });
  assert(ctext.status === 0 && /SHELL ROUTES/.test(ctext.stdout), "claude text output shows SHELL ROUTES",
    ctext.stdout.slice(-400));
}

// cleanup
try {
  rmSync(root, { recursive: true, force: true });
} catch (e) {
  console.error(`warning: failed to remove fixture root ${root}: ${e.message}`);
}

console.log(`\n${passed} passed, ${failures} failed ${failures === 0 ? "✓" : "✗"}`);
process.exit(failures === 0 ? 0 : 1);
