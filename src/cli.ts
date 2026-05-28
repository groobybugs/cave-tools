#!/usr/bin/env node

import { startServer } from "./index.js";
import {
  getAllBudgets,
  getStatsSessionsDir,
  reductionPercent,
  efficiencyMeter,
  isRtkAvailable,
} from "./compression/utils.js";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "fs";
import { join } from "path";

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

Cave Tools wraps shell + file ops with RTK rewriting, Stone Tablet JSON/XML
compression, and Flint Chipper line budgets. Prefer these over built-ins.

## Tools
- \`cave__read\` instead of Read — dedup cache + line-budget compression.
- \`cave__grep\`, \`cave__find\`, \`cave__ls\` instead of shell/Glob/Grep.
- \`cave__bash\` instead of Bash for every command — RTK + Stone Tablet + Flint Chipper.
- \`cave__compress\` to compress large pasted or tool-produced text.
- \`cave__status\` to inspect savings, cache hit rate, reduction % + budgets.
- After editing a file outside Cave Tools, call \`cave__write\` to invalidate the read dedup cache.

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
  const rtkAvailable = isRtkAvailable();
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
      return acc;
    },
    {
      filesTracked: 0,
      hits: 0,
      misses: 0,
      rawChars: 0,
      compressedChars: 0,
      compressionSavedChars: 0,
      dedupSavedChars: 0,
      savedChars: 0,
      calls: 0,
    },
  );
  const cacheTotal = totals.hits + totals.misses;
  const hitRate = cacheTotal > 0 ? totals.hits / cacheTotal : 0;
  const overallPct = reductionPercent(
    totals.rawChars,
    totals.savedChars,
    totals.dedupSavedChars,
  );

  const lines = [
    "=== Cave Tools Status ===",
    "",
    `RTK Available: ${rtkAvailable ? "Yes" : "No"}`,
    "",
  ];

  if (visibleSessions.length > 0) {
    const newestUpdate = Math.max(...visibleSessions.map((s) => s.updatedAt));
    const age = Math.round((Date.now() - newestUpdate) / 1000);
    const ageStr = age < 60 ? `${age}s ago` : `${Math.round(age / 60)}m ago`;
    lines.push(
      "Global Session Stats:",
      `  Sessions:               ${visibleSessions.length} (${liveSessions.length} live)`,
      `  Files tracked:          ${totals.filesTracked}`,
      `  Cache hits:             ${totals.hits}`,
      `  Cache misses:           ${totals.misses}`,
      `  Hit rate:               ${(hitRate * 100).toFixed(1)}%`,
      `  Compression calls:      ${totals.calls}`,
      `  Raw chars:              ${totals.rawChars}`,
      `  Compressed chars:       ${totals.compressedChars}`,
      `  Compression saved chars: ${totals.compressionSavedChars}`,
      `  Dedup saved chars:      ${totals.dedupSavedChars}`,
      `  Saved chars:            ${totals.savedChars}`,
      `  Estimated tokens saved: ${Math.round(totals.savedChars / 4)}`,
      `  Reduction:              ${overallPct.toFixed(1)}%`,
      `  Efficiency meter: ${efficiencyMeter(overallPct)} ${overallPct.toFixed(1)}%`,
      `  Last update:            ${ageStr}`,
      "",
    );
  } else {
    lines.push("Global Session Stats: (no session data yet - start MCP session first)", "");
  }

  lines.push(
    "Budget Configuration:",
    ...Object.entries(budgets).map(
      ([name, budget]) =>
        `  ${name}: max=${budget.maxLines}, head=${budget.headLines}, tail=${budget.tailLines}`,
    ),
  );

  console.log(lines.join("\n"));
  process.exit(0);
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
  console.error("Usage: cave-tools [mcp|bench|install-agent|init|status]");
  process.exit(1);
}
