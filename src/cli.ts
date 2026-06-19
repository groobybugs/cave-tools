#!/usr/bin/env node

import { startServer } from "./index.js";
import {
  getAllBudgets,
  getStatsSessionsDir,
  pruneDeadSessions,
  getLifetimeStats,
  reductionPercent,
  efficiencyMeter,
  isRtkAvailable,
} from "./compression/utils.js";
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  writeFileSync,
  lstatSync,
  realpathSync,
  statSync,
} from "fs";
import { homedir } from "os";
import { join, basename } from "path";

interface SessionStats {
  pid: number;
  sessionStart: number;
  updatedAt: number;
  cache: {
    hits: number;
    misses: number;
    total: number;
    hitRate: number;
    filesTracked: number;
  };
  savings: {
    totalCalls: number;
    rawChars: number;
    compressedChars: number;
    compressionSavedChars: number;
    dedupSavedChars: number;
    savedChars: number;
    estimatedTokensSaved: number;
    byTool: Record<
      string,
      {
        calls: number;
        rawChars: number;
        compressedChars: number;
        savedChars: number;
      }
    >;
  };
  rtk?: {
    rewrites: number;
    alreadyWrapped: number;
    passthrough: number;
  };
  budgets?: ReturnType<typeof getAllBudgets>;
}

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

interface StatusSummary {
  rtkAvailable: boolean;
  reductionPct: number;
  hitRatePct: number;
  cacheHits: number;
  cacheMisses: number;
  filesTracked: number;
  rtkRewrites: number;
  rtkAlreadyWrapped: number;
  rtkPassthrough: number;
  totalCalls: number;
  rawChars: number;
  compressedChars: number;
  compressionSavedChars: number;
  dedupSavedChars: number;
  savedChars: number;
  tokensSaved: number;
  liveSessions: number;
  endedSessions: number;
  visibleSessions: number;
  lastUpdate: number;
  hasData: boolean;
  budgets: ReturnType<typeof getAllBudgets>;
}

async function buildStatusSummary(): Promise<StatusSummary> {
  await pruneDeadSessions();
  const rtkAvailable = await isRtkAvailable();
  const lifetime = await getLifetimeStats();
  const sessions = readSessionStats();
  const liveSessions = sessions.filter((s) => isProcessAlive(s.pid));
  const visibleSessions = liveSessions.length > 0 ? liveSessions : sessions;
  const latestSession = visibleSessions
    .slice()
    .sort((a, b) => b.updatedAt - a.updatedAt)[0];
  const budgets = latestSession?.budgets || getAllBudgets();

  const totals = visibleSessions.reduce(
    (acc, s) => {
      const compressionSavedChars =
        s.savings.compressionSavedChars ?? s.savings.savedChars ?? 0;
      const dedupSavedChars = s.savings.dedupSavedChars ?? 0;
      acc.filesTracked += s.cache.filesTracked ?? 0;
      acc.hits += s.cache.hits ?? 0;
      acc.misses += s.cache.misses ?? 0;
      acc.rawChars += s.savings.rawChars ?? 0;
      acc.compressedChars += s.savings.compressedChars ?? 0;
      acc.compressionSavedChars += compressionSavedChars;
      acc.dedupSavedChars += dedupSavedChars;
      acc.savedChars += compressionSavedChars + dedupSavedChars;
      acc.calls += s.savings.totalCalls ?? 0;
      acc.rtkRewrites += s.rtk?.rewrites ?? 0;
      acc.rtkAlreadyWrapped += s.rtk?.alreadyWrapped ?? 0;
      acc.rtkPassthrough += s.rtk?.passthrough ?? 0;
      return acc;
    },
    {
      filesTracked: 0,
      hits: lifetime.hits,
      misses: lifetime.misses,
      rawChars: lifetime.rawChars,
      compressedChars: lifetime.compressedChars,
      compressionSavedChars: lifetime.compressionSavedChars,
      dedupSavedChars: lifetime.dedupSavedChars,
      savedChars: lifetime.savedChars,
      calls: lifetime.calls,
      rtkRewrites: lifetime.rtkRewrites,
      rtkAlreadyWrapped: lifetime.rtkAlreadyWrapped,
      rtkPassthrough: lifetime.rtkPassthrough,
    },
  );

  const cacheTotal = totals.hits + totals.misses;
  const hitRate = cacheTotal > 0 ? totals.hits / cacheTotal : 0;
  const compressionPct = reductionPercent(
    totals.rawChars,
    totals.compressionSavedChars,
  );

  const updateTimes = visibleSessions.map((s) => s.updatedAt);
  if (lifetime.updatedAt > 0) updateTimes.push(lifetime.updatedAt);
  const lastUpdate = updateTimes.length > 0 ? Math.max(...updateTimes) : 0;

  return {
    rtkAvailable,
    reductionPct: compressionPct,
    hitRatePct: hitRate * 100,
    cacheHits: totals.hits,
    cacheMisses: totals.misses,
    filesTracked: totals.filesTracked,
    rtkRewrites: totals.rtkRewrites,
    rtkAlreadyWrapped: totals.rtkAlreadyWrapped,
    rtkPassthrough: totals.rtkPassthrough,
    totalCalls: totals.calls,
    rawChars: totals.rawChars,
    compressedChars: totals.compressedChars,
    compressionSavedChars: totals.compressionSavedChars,
    dedupSavedChars: totals.dedupSavedChars,
    savedChars: totals.savedChars,
    tokensSaved: Math.round(totals.savedChars / 4),
    liveSessions: liveSessions.length,
    endedSessions: lifetime.bankedSessions,
    visibleSessions: visibleSessions.length,
    lastUpdate,
    hasData: visibleSessions.length > 0 || lifetime.bankedSessions > 0,
    budgets,
  };
}

// Compact one-liner for the statusline suffix.
// Format: "↓38% • 1.1M tok" (or "↓38% • 18cache" when no tokens yet).
// Pure ASCII fallback for terminals without UTF-8 — controlled by env var.
function formatStatuslineSuffix(s: StatusSummary): string {
  if (!s.hasData) return "";
  const pct = s.reductionPct.toFixed(0);
  const tokens = s.tokensSaved;
  let amount: string;
  if (tokens >= 1_000_000) amount = `${(tokens / 1_000_000).toFixed(1)}M tok`;
  else if (tokens >= 1000) amount = `${(tokens / 1000).toFixed(0)}k tok`;
  else if (tokens > 0) amount = `${tokens} tok`;
  else amount = `${s.cacheHits}cache`;
  const arrow = process.env.CAVE_TOOLS_ASCII === "1" ? "v" : "↓";
  const bullet = process.env.CAVE_TOOLS_ASCII === "1" ? "|" : "•";
  return `${arrow}${pct}% ${bullet} ${amount}`;
}

// Symlink-safe write of the statusline suffix file. Refuses to write through
// a symlink (would let a local attacker clobber an unrelated file). Verifies
// directory ownership when the parent itself is a legitimately symlinked
// ~/.claude (common in shared-config setups).
function writeStatuslineSuffix(suffix: string): void {
  const claudeDir = process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude");
  const suffixPath = join(claudeDir, ".cave-tools-statusline-suffix");
  try {
    mkdirSync(claudeDir, { recursive: true });
    let realDir = claudeDir;
    try {
      const ls = lstatSync(claudeDir);
      if (ls.isSymbolicLink()) {
        realDir = realpathSync(claudeDir);
        const rs = statSync(realDir);
        if (!rs.isDirectory()) return;
        if (typeof process.getuid === "function" && rs.uid !== process.getuid()) return;
      }
    } catch {
      return;
    }
    const realPath = join(realDir, basename(suffixPath));
    try {
      if (lstatSync(realPath).isSymbolicLink()) return;
    } catch (e) {
      const code = (e as NodeJS.ErrnoException).code;
      if (code !== "ENOENT") return;
    }
    writeFileSync(realPath, suffix, { mode: 0o600 });
  } catch {
    // Silent fail — suffix is best-effort. Statusline script handles absence.
  }
}

function readSessionStats(): SessionStats[] {
  const dir = getStatsSessionsDir();
  if (!existsSync(dir)) return [];

  return readdirSync(dir)
    .filter((name) => name.endsWith(".json"))
    .map((name) => {
      try {
        return JSON.parse(readFileSync(join(dir, name), "utf-8")) as SessionStats;
      } catch {
        return null;
      }
    })
    .filter((stats): stats is SessionStats => stats !== null);
}

const CAVE_INSTRUCTIONS = `# Cave Tools — token-optimized MCP

Cave Tools provides optimized drop-in replacements for shell + file ops (RTK
rewriting, structured JSON/XML extraction, per-tool line budgets). Same results,
fewer tokens. Prefer these over built-ins.

## Tools
- \`cave__read\` instead of Read — optimized drop-in replacement (dedup cache + line budgets).
- \`cave__grep\`, \`cave__find\`, \`cave__ls\` instead of shell/Glob/Grep.
- \`cave__bash\` instead of Bash for every command — optimized drop-in replacement (RTK + structured extraction + line budgets).
- \`cave__compress\` to optimize large pasted or tool-produced text down to fewer tokens.
- \`cave__status\` to inspect savings, cache hit rate, reduction % + budgets.
- \`cave__write\` to create/overwrite a single file; \`cave__edit\` for fuzzy string replacement.
- After editing a file outside Cave Tools, call \`cave__invalidate\` to refresh the read dedup cache.

## Rules
- Do not double-wrap: never run \`rtk <cmd>\` inside \`cave__bash\` (it already prepends rtk).
- Pass raw file paths / patterns / commands — do not wrap tools in extra scripts.
- Fall back to built-in Bash only for background processes, stream monitors, or hook-sensitive stdin.

## Savings
Run \`cave-tools status\` (CLI) or \`cave__status\` (MCP) for reduction % and an
rtk-gain-style efficiency meter aggregated across live sessions.
`;

function writeIfMissing(file: string, content: string): "created" | "exists" {
  if (existsSync(file)) return "exists";
  writeFileSync(file, content, "utf-8");
  return "created";
}

const args = process.argv.slice(2);

if (args[0] === "mcp" || args.length === 0) {
  startServer().catch((error) => {
    console.error("Fatal error:", error);
    process.exit(1);
  });
} else if (args[0] === "status") {
  (async () => {
  const summary = await buildStatusSummary();

  // --emit-statusline: write compact one-liner to ~/.claude/.cave-tools-statusline-suffix
  // for the statusline script to read. No stdout output. Used by SessionStart and
  // PostToolUse hooks to keep the badge fresh without re-rendering on every keystroke.
  if (args.includes("--emit-statusline")) {
    writeStatuslineSuffix(formatStatuslineSuffix(summary));
    process.exit(0);
  }

  const lines = [
    "=== Cave Tools Status ===",
    "",
    `RTK Available: ${summary.rtkAvailable ? "Yes" : "No"}`,
    "",
  ];

  if (summary.hasData) {
    const age = summary.lastUpdate > 0
      ? Math.round((Date.now() - summary.lastUpdate) / 1000)
      : 0;
    const ageStr = age < 60 ? `${age}s ago` : `${Math.round(age / 60)}m ago`;
    lines.push(
      "Global Session Stats (cumulative):",
      `  Sessions:      ${summary.visibleSessions + summary.endedSessions} (${summary.liveSessions} live, ${summary.endedSessions} ended)`,
      `  Files tracked: ${summary.filesTracked}`,
      `  Cache hits:    ${summary.cacheHits}`,
      `  Cache misses:  ${summary.cacheMisses}`,
      `  Hit rate:      ${summary.hitRatePct.toFixed(1)}%`,
      `  Last update:   ${ageStr}`,
      "",
      "RTK Rewrites:",
      `  Rewritten:       ${summary.rtkRewrites}`,
      `  Already wrapped: ${summary.rtkAlreadyWrapped}`,
      `  Passthrough:     ${summary.rtkPassthrough}`,
      "",
      "Output trimming:",
      `  Calls:            ${summary.totalCalls}`,
      `  Raw chars:        ${summary.rawChars}`,
      `  Trimmed chars:    ${summary.compressedChars}`,
      `  Saved chars:      ${summary.compressionSavedChars}`,
      `  Reduction:        ${summary.reductionPct.toFixed(1)}%`,
      `  Meter: ${efficiencyMeter(summary.reductionPct)} ${summary.reductionPct.toFixed(1)}%`,
      "",
      "Dedup (avoided re-reads):",
      `  Cache hits:    ${summary.cacheHits}`,
      `  Chars avoided: ${summary.dedupSavedChars} (budget-capped)`,
      "",
      `Total est. tokens saved: ${summary.tokensSaved}`,
      "",
    );
  } else {
    lines.push("Global Session Stats: (no session data yet - start MCP session first)", "");
  }

  lines.push(
    "Budget Configuration:",
    ...Object.entries(summary.budgets).map(
      ([name, budget]) =>
        `  ${name}: max=${budget.maxLines}, head=${budget.headLines}, tail=${budget.tailLines}`,
    ),
  );

  console.log(lines.join("\n"));
  process.exit(0);
  })();
} else if (args[0] === "bench") {
  console.log("Benchmark mode - not yet implemented");
  process.exit(0);
} else if (args[0] === "install-agent" || args[0] === "init") {
  const claude = writeIfMissing(join(process.cwd(), "CLAUDE.md"), CAVE_INSTRUCTIONS);
  const agents = writeIfMissing(join(process.cwd(), "AGENTS.md"), CAVE_INSTRUCTIONS);
  console.log("Cave Tools instructions:");
  console.log(`  CLAUDE.md  ${claude === "created" ? "created" : "already exists (left untouched)"}`);
  console.log(`  AGENTS.md  ${agents === "created" ? "created" : "already exists (left untouched)"}`);
  process.exit(0);
} else {
  console.error(`Unknown command: ${args[0]}`);
  console.error("Usage: cave-tools [mcp|bench|install-agent|init|status [--emit-statusline]]");
  process.exit(1);
}
