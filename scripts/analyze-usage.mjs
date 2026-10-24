#!/usr/bin/env node
// Analyze real token usage from local Claude Code transcripts (read-only, no
// API calls). Reads every *.jsonl under $CLAUDE_CONFIG_DIR/projects (default
// ~/.claude/projects), including nested subagent transcripts.
//
// Usage: node scripts/analyze-usage.mjs [--source claude|opencode] [--db <path>]
//          [--since YYYY-MM-DD] [--until YYYY-MM-DD]
//          [--project <cwd substring>] [--top N] [--json]
//
// --source claude (default) analyzes Claude Code transcripts under
// $CLAUDE_CONFIG_DIR/projects; --db is not valid for Claude.
// --source opencode reads the OpenCode V2 SQLite database directly (read-only):
//   --db <path>, or $OPENCODE_DB_PATH, or OpenCode's $OPENCODE_DB, or
//   $XDG_DATA_HOME/opencode/opencode.db / ~/.local/share/opencode/opencode.db.
//   Requires better-sqlite3
//   (dynamically imported only for OpenCode). The OpenCode report is honest
//   about what the DB stores: tool calls, routes, result sizes — no fake LLM
//   token totals.
//
// --since/--until filter Claude by line timestamp and OpenCode by
// part.time_created (ms; UTC days, both inclusive). --top limits the printed
// tables; --json always has every row.
// "weighted input" is relative units, not dollars:
//   input + 1.25*cache_creation_5m + 2*cache_creation_1h + 0.1*cache_read
//
// Shell routes classify how a shell call reached the OS:
//   native-shell  native Bash/shell tool (whether or not it was automatically
//                 post-filtered — no separate model call is claimed)
//   mcp-bash      cave__bash MCP tool
//   cli-bash      shell command anchored on a cave-tools CLI 'bash'/'raw'
//   cli-filter    shell command anchored on cave-tools filter
//   other         non-shell tools

import { readdirSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join } from "node:path";

function parseArgs(argv) {
  const opts = { source: "claude", db: null, since: null, until: null, project: null, top: 15, json: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const value = () => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`${arg} needs a value`);
      return v;
    };
    if (arg === "--json") opts.json = true;
    else if (arg === "--source") {
      const v = value();
      if (v !== "claude" && v !== "opencode") throw new Error(`--source must be claude or opencode, got: ${v}`);
      opts.source = v;
    } else if (arg === "--db") opts.db = value();
    else if (arg === "--since") opts.since = Date.parse(value() + "T00:00:00Z");
    else if (arg === "--until") opts.until = Date.parse(value() + "T00:00:00Z") + 86400000;
    else if (arg === "--project") opts.project = value();
    else if (arg === "--top") opts.top = Number(value());
    else throw new Error(`unknown argument: ${arg}`);
  }
  if (Number.isNaN(opts.since) || Number.isNaN(opts.until)) throw new Error("dates must be YYYY-MM-DD");
  if (!Number.isInteger(opts.top) || opts.top < 1) throw new Error("--top must be a positive integer");
  if (opts.db !== null && opts.source === "claude") throw new Error("--db is only valid with --source opencode");
  return opts;
}

function listJsonl(dir) {
  const out = [];
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listJsonl(full));
    else if (entry.isFile() && entry.name.endsWith(".jsonl")) out.push(full);
  }
  return out;
}

const emptyUsage = () => ({
  requests: 0,
  input: 0,
  cache_creation: 0,
  cache_creation_5m: 0,
  cache_creation_1h: 0,
  cache_read: 0,
  output: 0,
  weighted_input: 0,
});

function addUsage(acc, u) {
  const creation = u.cache_creation_input_tokens || 0;
  const c1h = u.cache_creation?.ephemeral_1h_input_tokens || 0;
  // Older transcripts lack the split; treat unsplit creation as 5m.
  const c5m = u.cache_creation ? u.cache_creation.ephemeral_5m_input_tokens || 0 : creation;
  const input = u.input_tokens || 0;
  const read = u.cache_read_input_tokens || 0;
  acc.requests += 1;
  acc.input += input;
  acc.cache_creation += creation;
  acc.cache_creation_5m += c5m;
  acc.cache_creation_1h += c1h;
  acc.cache_read += read;
  acc.output += u.output_tokens || 0;
  acc.weighted_input += input + 1.25 * c5m + 2 * c1h + 0.1 * read;
}

// mcp__cave-tools__cave__read -> cave__read (also plugin-scoped server names).
// OpenCode plugin form: cave-tools_cave__bash -> cave__bash.
function normalizeTool(name) {
  if (name.startsWith("cave-tools_")) {
    const rest = name.slice("cave-tools_".length);
    if (rest.startsWith("cave__")) return { name: rest, group: "cave" };
  }
  const cave = /^mcp__(?:.*_)?cave-tools__(cave__.+)$/.exec(name);
  if (cave) return { name: cave[1], group: "cave" };
  if (name.startsWith("cave__")) return { name, group: "cave" };
  if (name.startsWith("mcp__")) return { name, group: "other-mcp" };
  return { name, group: "built-in" };
}

function resultSize(content) {
  if (typeof content === "string") return { chars: content.length, images: 0 };
  if (!Array.isArray(content)) return { chars: 0, images: 0 };
  let chars = 0;
  let images = 0;
  for (const block of content) {
    if (block?.type === "image") images += 1;
    else if (block?.type === "text") chars += String(block.text ?? "").length;
    else chars += JSON.stringify(block ?? "").length;
  }
  return { chars, images };
}

// --------------------------------------------------------------- routes
// How a shell call reached the OS. Route is a property of the call (name +
// command), aggregated into the `routes` report section.
const SHELL_TOOL_RE = /^(?:bash|shell)$/i;

// Anchored: optional env assignments, then (path)/cave-tools or bare
// cave-tools as the first command word. `cd x && cave-tools ...` is not
// anchored.
const CAVE_CLI_RE =
  /^(?:\s*(?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|\S*)\s+))*(?:node\s+)?(?:[^;&|\s]+\/)?cave-tools(?=\s|$)(?:\s+(\S+))?/;

function routeForCall(name, input) {
  const normalized = normalizeTool(name).name;
  if (normalized === "cave__bash") return "mcp-bash";
  if (!SHELL_TOOL_RE.test(normalized)) return "other";
  const command = typeof input === "string" ? input : typeof input?.command === "string" ? input.command : "";
  const m = CAVE_CLI_RE.exec(command);
  if (!m) return "native-shell";
  if (/^(?:bash|raw)$/.test(m[1] ?? "")) return "cli-bash";
  if (m[1] === "filter") return "cli-filter";
  return "native-shell";
}

function routeLabel(counts) {
  const present = [...counts].filter(([, n]) => n > 0).map(([route]) => route);
  if (present.length === 0) return "other";
  return present.length === 1 ? present[0] : "mixed";
}

function newRouteRow() {
  return { calls: 0, errors: 0, results: 0, total_chars: 0 };
}

const median = (xs) => {
  if (!xs.length) return 0;
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};
const percentile = (xs, p) => {
  if (!xs.length) return 0;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.ceil((p / 100) * s.length) - 1)];
};

export function analyze(opts) {
  const configDir = process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude");
  const root = join(configDir, "projects");
  const files = listJsonl(root);

  const inRange = (line) => {
    const ts = Date.parse(line.timestamp ?? "");
    if ((opts.since !== null || opts.until !== null) && Number.isNaN(ts)) return false;
    if (opts.since !== null && ts < opts.since) return false;
    if (opts.until !== null && ts >= opts.until) return false;
    if (opts.project !== null && !String(line.cwd ?? "").includes(opts.project)) return false;
    return true;
  };

  const seenMessages = new Set();
  const toolUses = new Map(); // tool_use id -> { name, input }
  const results = []; // { id, chars, images, isError }
  const calls = []; // { name, input } — one per tool_use block
  const sessions = new Map();
  const routes = {};
  const routeRow = (route) => (routes[route] ||= newRouteRow());
  const bumpRoute = (name, input, field) => {
    const route = routeForCall(name, input);
    routeRow(route)[field] += 1;
    return route;
  };
  let malformed = 0;
  let lineCount = 0;

  for (const file of files) {
    let text;
    try {
      text = readFileSync(file, "utf8");
    } catch {
      continue;
    }
    for (const raw of text.split("\n")) {
      if (!raw.trim()) continue;
      lineCount += 1;
      let line;
      try {
        line = JSON.parse(raw);
      } catch {
        malformed += 1;
        continue;
      }
      if (!line || typeof line !== "object") {
        malformed += 1;
        continue;
      }
      if (line.type !== "assistant" && line.type !== "user") continue;
      if (!inRange(line)) continue;
      const content = Array.isArray(line.message?.content) ? line.message.content : [];

      if (line.type === "assistant") {
        for (const block of content) {
          if (block?.type === "tool_use" && block.id) {
            const name = String(block.name ?? "?");
            toolUses.set(block.id, { name, input: block.input ?? null });
            calls.push({ name, input: block.input ?? null });
            bumpRoute(name, block.input ?? null, "calls");
          }
        }
        const usage = line.message?.usage;
        if (!usage) continue;
        // One API response spans several lines sharing message.id: count once.
        const key = line.message?.id ?? line.uuid ?? raw;
        if (seenMessages.has(key)) continue;
        seenMessages.add(key);
        const sid = line.sessionId ?? "?";
        let s = sessions.get(sid);
        if (!s) {
          s = { sessionId: sid, cwd: line.cwd ?? "", first: null, last: null, firstRequestContext: 0, ...emptyUsage() };
          sessions.set(sid, s);
        }
        const ts = line.timestamp ?? "";
        if (s.first === null || ts < s.first) {
          s.first = ts;
          s.firstRequestContext =
            (usage.input_tokens || 0) + (usage.cache_creation_input_tokens || 0) + (usage.cache_read_input_tokens || 0);
        }
        if (s.last === null || ts > s.last) s.last = ts;
        addUsage(s, usage);
      } else {
        for (const block of content) {
          if (block?.type !== "tool_result") continue;
          const use = toolUses.get(block.tool_use_id);
          const rs = resultSize(block.content);
          if (use) {
            const rr = routeRow(bumpRoute(use.name, use.input, "results"));
            rr.total_chars += rs.chars;
            if (block.is_error === true) rr.errors += 1;
          }
          results.push({ id: block.tool_use_id, isError: block.is_error === true, ...rs });
        }
      }
    }
  }

  const totals = emptyUsage();
  for (const s of sessions.values()) {
    for (const k of Object.keys(totals)) totals[k] += s[k];
  }

  const tools = new Map();
  const toolRow = (raw) => {
    const { name, group } = normalizeTool(raw);
    let t = tools.get(name);
    if (!t) {
      t = { name, group, calls: 0, errors: 0, results: 0, images: 0, sizes: [] };
      tools.set(name, t);
    }
    return t;
  };
  for (const c of calls) toolRow(c.name).calls += 1;
  for (const r of results) {
    const t = toolRow(toolUses.get(r.id)?.name ?? "(unknown)");
    t.results += 1;
    if (r.isError) t.errors += 1;
    t.images += r.images;
    t.sizes.push(r.chars);
  }
  const allChars = results.reduce((n, r) => n + r.chars, 0);
  const toolRows = [...tools.values()]
    .map((t) => {
      const total = t.sizes.reduce((n, x) => n + x, 0);
      // A tool can use several shell routes. Keep per-tool metadata honest;
      // exact per-route counts live in the routes aggregate.
      const counts = new Map();
      for (const c of calls) {
        if (normalizeTool(c.name).name !== t.name) continue;
        const route = routeForCall(c.name, c.input);
        counts.set(route, (counts.get(route) || 0) + 1);
      }
      return {
        name: t.name,
        group: t.group,
        route: routeLabel(counts),
        calls: t.calls,
        errors: t.errors,
        results: t.results,
        images: t.images,
        total_chars: total,
        avg_chars: t.sizes.length ? Math.round(total / t.sizes.length) : 0,
        p95_chars: percentile(t.sizes, 95),
        max_chars: t.sizes.length ? Math.max(...t.sizes) : 0,
        share: allChars ? total / allChars : 0,
      };
    })
    .sort((a, b) => b.total_chars - a.total_chars || b.calls - a.calls);

  const groups = {};
  for (const g of ["cave", "built-in", "other-mcp"]) {
    const rows = toolRows.filter((t) => t.group === g);
    const total = rows.reduce((n, t) => n + t.total_chars, 0);
    groups[g] = {
      tools: rows.length,
      calls: rows.reduce((n, t) => n + t.calls, 0),
      errors: rows.reduce((n, t) => n + t.errors, 0),
      images: rows.reduce((n, t) => n + t.images, 0),
      total_chars: total,
      share: allChars ? total / allChars : 0,
    };
  }

  const sessionRows = [...sessions.values()].sort((a, b) => b.weighted_input - a.weighted_input);
  return {
    root,
    filters: {
      since: opts.since === null ? null : new Date(opts.since).toISOString().slice(0, 10),
      until: opts.until === null ? null : new Date(opts.until - 86400000).toISOString().slice(0, 10),
      project: opts.project,
    },
    files: files.length,
    lines: lineCount,
    malformed_lines: malformed,
    totals,
    fixed_overhead: {
      median_first_request_context: median(sessionRows.map((s) => s.firstRequestContext)),
      sessions: sessionRows.length,
    },
    tool_result_chars: allChars,
    groups,
    tools: toolRows,
    routes,
    sessions: sessionRows,
  };
}

// ---------------------------------------------------------- OpenCode source
// Reads the OpenCode V2 SQLite store read-only. Only the columns the current
// schema guarantees are queried: session(id, directory), part(session_id,
// time_created, data). Rows stream via iterate() so the whole DB is never
// loaded into memory.
function resolveDatabasePath(opts) {
  if (opts.db) return opts.db;
  if (process.env.OPENCODE_DB_PATH) return process.env.OPENCODE_DB_PATH;
  const xdg = process.env.XDG_DATA_HOME || join(homedir(), ".local", "share");
  if (process.env.OPENCODE_DB) {
    return isAbsolute(process.env.OPENCODE_DB)
      ? process.env.OPENCODE_DB
      : join(xdg, "opencode", process.env.OPENCODE_DB);
  }
  return join(xdg, "opencode", "opencode.db");
}

export function analyzeOpencode(opts) {
  return import("better-sqlite3").then((m) => analyzeOpencodeSync(opts, m.default));
}

function analyzeOpencodeSync(opts, Database) {
  const resolved = resolveDatabasePath(opts);
  let db;
  try {
    db = new Database(resolved, { readonly: true, fileMustExist: true });
  } catch (e) {
    throw new Error(`cannot read OpenCode database ${resolved}: ${e.message}`);
  }
  try {
    const filters = {
      since: opts.since === null ? null : new Date(opts.since).toISOString().slice(0, 10),
      until: opts.until === null ? null : new Date(opts.until - 86400000).toISOString().slice(0, 10),
      project: opts.project,
    };
    const sessionsSeen = new Set();
    const tools = new Map();
    const routes = {};
    let parts = 0;
    let malformedParts = 0;
    let resultChars = 0;
    let callsTotal = 0;
    let errorsTotal = 0;
    let resultsTotal = 0;

    const conditions = [];
    const params = [];
    if (opts.since !== null) {
      conditions.push("p.time_created >= ?");
      params.push(opts.since);
    }
    if (opts.until !== null) {
      conditions.push("p.time_created < ?");
      params.push(opts.until);
    }
    if (opts.project !== null) {
      conditions.push("instr(s.directory, ?) > 0");
      params.push(opts.project);
    }
    const rows = db
      .prepare(
        `SELECT p.time_created AS time_created, p.data AS data, s.id AS session_id
         FROM part p JOIN session s ON s.id = p.session_id
         ${conditions.length ? `WHERE ${conditions.join(" AND ")}` : ""}
         ORDER BY p.time_created`,
      )
      .iterate(...params);

    for (const row of rows) {
      sessionsSeen.add(row.session_id);
      parts += 1;

      let data;
      try {
        data = JSON.parse(row.data);
      } catch {
        malformedParts += 1;
        continue;
      }
      const tool = typeof data?.tool === "string" ? data.tool : null;
      const isToolPart = tool !== null && (data?.type === undefined || data?.type === "tool");
      if (!isToolPart) continue;

      const { name, group } = normalizeTool(tool);
      const state = data?.state ?? {};
      const status = state?.status;
      const isError = status === "error";
      const hasResult = status === "completed" || isError;
      let chars = 0;
      if (status === "completed") chars = typeof state?.output === "string" ? state.output.length : 0;
      else if (isError) chars = typeof state?.error === "string" ? state.error.length : 0;
      const route = routeForCall(name, typeof state?.input === "object" && state?.input !== null ? state.input : null);

      callsTotal += 1;
      if (hasResult) resultsTotal += 1;
      if (isError) errorsTotal += 1;
      resultChars += chars;

      let t = tools.get(name);
      if (!t) {
        t = { name, group, routeCounts: new Map(), calls: 0, errors: 0, results: 0, images: 0, sizes: [] };
        tools.set(name, t);
      }
      t.routeCounts.set(route, (t.routeCounts.get(route) || 0) + 1);
      t.calls += 1;
      if (hasResult) {
        t.results += 1;
        t.sizes.push(chars);
      }
      if (isError) t.errors += 1;
      const rr = (routes[route] ||= newRouteRow());
      rr.calls += 1;
      if (hasResult) {
        rr.results += 1;
        rr.total_chars += chars;
      }
      if (isError) rr.errors += 1;
    }

    const toolRows = [...tools.values()]
      .map((t) => {
        const total = t.sizes.reduce((n, x) => n + x, 0);
        return {
          name: t.name,
          group: t.group,
          route: routeLabel(t.routeCounts),
          calls: t.calls,
          errors: t.errors,
          results: t.results,
          images: 0,
          total_chars: total,
          avg_chars: t.sizes.length ? Math.round(total / t.sizes.length) : 0,
          p95_chars: percentile(t.sizes, 95),
          max_chars: t.sizes.length ? Math.max(...t.sizes) : 0,
          share: resultChars ? total / resultChars : 0,
        };
      })
      .sort((a, b) => b.total_chars - a.total_chars || b.calls - a.calls);

    const groups = {};
    for (const g of ["cave", "built-in", "other-mcp"]) {
      const rows2 = toolRows.filter((t) => t.group === g);
      const total = rows2.reduce((n, t) => n + t.total_chars, 0);
      groups[g] = {
        tools: rows2.length,
        calls: rows2.reduce((n, t) => n + t.calls, 0),
        errors: rows2.reduce((n, t) => n + t.errors, 0),
        images: 0,
        total_chars: total,
        share: resultChars ? total / resultChars : 0,
      };
    }

    return {
      source: "opencode",
      database: resolved,
      filters,
      sessions: sessionsSeen.size,
      parts,
      malformed_parts: malformedParts,
      tool_calls: callsTotal,
      tool_results: resultsTotal,
      tool_errors: errorsTotal,
      tool_result_chars: resultChars,
      groups,
      tools: toolRows,
      routes,
    };
  } finally {
    db.close();
  }
}

const fmt = (n) => Math.round(n).toLocaleString("en-US");
const pct = (x) => (x * 100).toFixed(1) + "%";

function printRoutes(routes) {
  console.log("\nSHELL ROUTES (native shell + cave__bash; native-shell calls may be automatically post-filtered — it is not a separate model call)");
  console.log("  " + "route".padEnd(14) + ["calls", "errors", "results", "total_chars"].map((h) => h.padStart(12)).join(""));
  for (const [name, row] of Object.entries(routes)) {
    console.log("  " + name.padEnd(14) + [row.calls, row.errors, row.results, row.total_chars].map((v) => fmt(v).padStart(12)).join(""));
  }
}

function printReport(r, top) {
  const f = r.filters;
  console.log(`transcripts: ${r.files} files, ${r.lines} lines (${r.malformed_lines} malformed) under ${r.root}`);
  console.log(`filters: since=${f.since ?? "-"} until=${f.until ?? "-"} project=${f.project ?? "-"}`);
  const t = r.totals;
  console.log(`\nTOTAL  requests ${fmt(t.requests)}  input ${fmt(t.input)}  ` +
    `cache_creation ${fmt(t.cache_creation)} (5m ${fmt(t.cache_creation_5m)}, 1h ${fmt(t.cache_creation_1h)})  ` +
    `cache_read ${fmt(t.cache_read)}  output ${fmt(t.output)}`);
  console.log(`weighted input (relative units, not dollars): ${fmt(t.weighted_input)}`);
  console.log(`fixed overhead proxy (median first-request context over ${r.fixed_overhead.sessions} sessions): ` +
    `${fmt(r.fixed_overhead.median_first_request_context)} tok`);

  console.log(`\nSESSIONS by weighted input (top ${Math.min(top, r.sessions.length)} of ${r.sessions.length})`);
  console.log("  session   first       req     input   create5m   create1h     cache_read    output   weighted  first_ctx  cwd");
  for (const s of r.sessions.slice(0, top)) {
    console.log("  " + [
      s.sessionId.slice(0, 8),
      (s.first ?? "").slice(0, 10).padEnd(10),
      String(s.requests).padStart(5),
      fmt(s.input).padStart(9),
      fmt(s.cache_creation_5m).padStart(10),
      fmt(s.cache_creation_1h).padStart(10),
      fmt(s.cache_read).padStart(14),
      fmt(s.output).padStart(9),
      fmt(s.weighted_input).padStart(10),
      fmt(s.firstRequestContext).padStart(10),
      s.cwd,
    ].join(" "));
  }

  console.log(`\nTOOLS by result chars (top ${Math.min(top, r.tools.length)} of ${r.tools.length}; total ${fmt(r.tool_result_chars)} chars)`);
  console.log("  " + "tool".padEnd(34) + "group".padEnd(10) + "route".padEnd(12) +
    ["calls", "errors", "total", "avg", "p95", "max", "imgs", "share"].map((h) => h.padStart(11)).join(""));
  for (const row of r.tools.slice(0, top)) {
    console.log("  " + row.name.slice(0, 33).padEnd(34) + row.group.padEnd(10) + row.route.padEnd(12) + [
      fmt(row.calls), fmt(row.errors), fmt(row.total_chars), fmt(row.avg_chars),
      fmt(row.p95_chars), fmt(row.max_chars), fmt(row.images), pct(row.share),
    ].map((v) => v.padStart(11)).join(""));
  }
  console.log("\nGROUPS");
  for (const [name, g] of Object.entries(r.groups)) {
    console.log(`  ${name.padEnd(10)} tools ${String(g.tools).padStart(3)}  calls ${fmt(g.calls).padStart(7)}  ` +
      `errors ${fmt(g.errors).padStart(5)}  chars ${fmt(g.total_chars).padStart(12)}  imgs ${fmt(g.images).padStart(4)}  share ${pct(g.share)}`);
  }
  printRoutes(r.routes);
}

function printOpencodeReport(r, top) {
  const f = r.filters;
  console.log(`database: ${r.database}`);
  console.log(`filters: since=${f.since ?? "-"} until=${f.until ?? "-"} project=${f.project ?? "-"}`);
  console.log(`sessions: ${r.sessions}  parts: ${r.parts} (${r.malformed_parts} malformed)`);
  console.log(`\nTOOL CALLS ${fmt(r.tool_calls)}  results ${fmt(r.tool_results)}  errors ${fmt(r.tool_errors)}  ` +
    `result chars ${fmt(r.tool_result_chars)}`);

  console.log(`\nTOOLS by result chars (top ${Math.min(top, r.tools.length)} of ${r.tools.length})`);
  console.log("  " + "tool".padEnd(34) + "group".padEnd(10) + "route".padEnd(12) +
    ["calls", "errors", "total", "avg", "p95", "max", "share"].map((h) => h.padStart(11)).join(""));
  for (const row of r.tools.slice(0, top)) {
    console.log("  " + row.name.slice(0, 33).padEnd(34) + row.group.padEnd(10) + row.route.padEnd(12) + [
      fmt(row.calls), fmt(row.errors), fmt(row.total_chars), fmt(row.avg_chars),
      fmt(row.p95_chars), fmt(row.max_chars), pct(row.share),
    ].map((v) => v.padStart(11)).join(""));
  }
  console.log("\nGROUPS");
  for (const [name, g] of Object.entries(r.groups)) {
    console.log(`  ${name.padEnd(10)} tools ${String(g.tools).padStart(3)}  calls ${fmt(g.calls).padStart(7)}  ` +
      `errors ${fmt(g.errors).padStart(5)}  chars ${fmt(g.total_chars).padStart(12)}  share ${pct(g.share)}`);
  }
  printRoutes(r.routes);
}

let opts;
try {
  opts = parseArgs(process.argv.slice(2));
} catch (e) {
  console.error(`analyze-usage: ${e.message}`);
  process.exit(2);
}

if (opts.source === "opencode") {
  try {
    const report = await analyzeOpencode(opts);
    if (opts.json) console.log(JSON.stringify(report, null, 2));
    else printOpencodeReport(report, opts.top);
  } catch (e) {
    console.error(`analyze-usage: ${e.message}`);
    process.exit(2);
  }
} else {
  const report = analyze(opts);
  if (opts.json) console.log(JSON.stringify(report, null, 2));
  else printReport(report, opts.top);
}
