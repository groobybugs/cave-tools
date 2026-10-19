#!/usr/bin/env node
// Analyze real token usage from local Claude Code transcripts (read-only, no
// API calls). Reads every *.jsonl under $CLAUDE_CONFIG_DIR/projects (default
// ~/.claude/projects), including nested subagent transcripts.
//
// Usage: node scripts/analyze-usage.mjs [--since YYYY-MM-DD] [--until YYYY-MM-DD]
//          [--project <cwd substring>] [--top N] [--json]
//
// --since/--until filter by line timestamp (UTC days, both inclusive).
// --top limits the printed tool/session tables; --json always has every row.
// "weighted input" is relative units, not dollars:
//   input + 1.25*cache_creation_5m + 2*cache_creation_1h + 0.1*cache_read

import { readdirSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

function parseArgs(argv) {
  const opts = { since: null, until: null, project: null, top: 15, json: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const value = () => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`${arg} needs a value`);
      return v;
    };
    if (arg === "--json") opts.json = true;
    else if (arg === "--since") opts.since = Date.parse(value() + "T00:00:00Z");
    else if (arg === "--until") opts.until = Date.parse(value() + "T00:00:00Z") + 86400000;
    else if (arg === "--project") opts.project = value();
    else if (arg === "--top") opts.top = Number(value());
    else throw new Error(`unknown argument: ${arg}`);
  }
  if (Number.isNaN(opts.since) || Number.isNaN(opts.until)) throw new Error("dates must be YYYY-MM-DD");
  if (!Number.isInteger(opts.top) || opts.top < 1) throw new Error("--top must be a positive integer");
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
function normalizeTool(name) {
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
  const toolUses = new Map(); // tool_use id -> raw name
  const results = []; // { id, chars, images, isError }
  const sessions = new Map();
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
          if (block?.type === "tool_use" && block.id) toolUses.set(block.id, String(block.name ?? "?"));
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
          results.push({ id: block.tool_use_id, isError: block.is_error === true, ...resultSize(block.content) });
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
  for (const raw of toolUses.values()) toolRow(raw).calls += 1;
  for (const r of results) {
    const t = toolRow(toolUses.get(r.id) ?? "(unknown)");
    t.results += 1;
    if (r.isError) t.errors += 1;
    t.images += r.images;
    t.sizes.push(r.chars);
  }
  const allChars = results.reduce((n, r) => n + r.chars, 0);
  const toolRows = [...tools.values()]
    .map((t) => {
      const total = t.sizes.reduce((n, x) => n + x, 0);
      return {
        name: t.name,
        group: t.group,
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
    sessions: sessionRows,
  };
}

const fmt = (n) => Math.round(n).toLocaleString("en-US");
const pct = (x) => (x * 100).toFixed(1) + "%";

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
  console.log("  " + "tool".padEnd(34) + "group".padEnd(10) +
    ["calls", "errors", "total", "avg", "p95", "max", "imgs", "share"].map((h) => h.padStart(11)).join(""));
  for (const row of r.tools.slice(0, top)) {
    console.log("  " + row.name.slice(0, 33).padEnd(34) + row.group.padEnd(10) + [
      fmt(row.calls), fmt(row.errors), fmt(row.total_chars), fmt(row.avg_chars),
      fmt(row.p95_chars), fmt(row.max_chars), fmt(row.images), pct(row.share),
    ].map((v) => v.padStart(11)).join(""));
  }
  console.log("\nGROUPS");
  for (const [name, g] of Object.entries(r.groups)) {
    console.log(`  ${name.padEnd(10)} tools ${String(g.tools).padStart(3)}  calls ${fmt(g.calls).padStart(7)}  ` +
      `errors ${fmt(g.errors).padStart(5)}  chars ${fmt(g.total_chars).padStart(12)}  imgs ${fmt(g.images).padStart(4)}  share ${pct(g.share)}`);
  }
}

let opts;
try {
  opts = parseArgs(process.argv.slice(2));
} catch (e) {
  console.error(`analyze-usage: ${e.message}`);
  process.exit(2);
}
const report = analyze(opts);
if (opts.json) console.log(JSON.stringify(report, null, 2));
else printReport(report, opts.top);
