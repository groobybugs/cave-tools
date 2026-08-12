#!/usr/bin/env node

import { startServer } from "./index.js";
import {
  getAllBudgets,
  pruneDeadSessions,
  getLifetimeStats,
  listSessionStats,
  reductionPercent,
  isRtkAvailable,
} from "./compression/utils.js";
import {
  existsSync,
  mkdirSync,
  writeFileSync,
  lstatSync,
  realpathSync,
  statSync,
} from "fs";
import { homedir } from "os";
import { join, basename } from "path";
import {
  fetchRtkGainSummary,
  renderStatusCli,
  type ToolBreakdown,
} from "./status-ui.js";

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
  byTool: ToolBreakdown[];
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

  // Aggregate by-tool from all sessions (live + ended) for the breakdown table.
  const byToolMap = new Map<string, ToolBreakdown>();
  for (const s of listSessionStats(true)) {
    const tools = s.savings?.byTool ?? {};
    for (const [name, t] of Object.entries(tools)) {
      const cur = byToolMap.get(name) ?? {
        name,
        calls: 0,
        rawChars: 0,
        savedChars: 0,
      };
      cur.calls += t.calls ?? 0;
      cur.rawChars += t.rawChars ?? 0;
      cur.savedChars += t.savedChars ?? 0;
      byToolMap.set(name, cur);
    }
  }

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
    byTool: [...byToolMap.values()],
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
  return listSessionStats(false);
}

const CAVE_INSTRUCTIONS = `# Cave Tools — token-optimized MCP

Cave Tools provides optimized drop-in replacements for shell + file ops (RTK
rewriting, structured JSON/XML extraction, per-tool line budgets). Same results,
fewer tokens. Prefer these over built-ins.

## Tools
- \`cave__read\` instead of Read — optimized drop-in replacement (dedup cache + line budgets).
- \`cave__grep\`, \`cave__find\`, \`cave__ls\` instead of shell/Glob/Grep.
- \`cave__bash\` instead of Bash for every command — optimized drop-in replacement (RTK + structured extraction + line budgets). Max timeout 10min.
- \`cave__bash_start\` for commands expected to exceed ~2-3min — runs detached, returns a jobId immediately; poll with \`cave__bash_status\` (\`wait\` up to 60s), stop with \`cave__bash_stop\`. \`cave-tools jobs\` lists/kills jobs from a terminal.
- \`cave__compress\` to optimize large pasted or tool-produced text down to fewer tokens.
- \`cave__status\` to inspect savings, cache hit rate, reduction % + budgets.
- \`cave__write\` to create/overwrite a single file; \`cave__edit\` for fuzzy string replacement.
- After editing a file outside Cave Tools, call \`cave__invalidate\` to refresh the read dedup cache.

## Rules
- Do not double-wrap: never run \`rtk <cmd>\` inside \`cave__bash\` (it already prepends rtk).
- Pass raw file paths / patterns / commands — do not wrap tools in extra scripts.
- Never \`sleep\`-poll inside \`cave__bash\` to wait for long tasks: use \`cave__bash_start\` + \`cave__bash_status { wait: 60 }\` instead.
- Fall back to built-in Bash only for stream monitors or hook-sensitive stdin.

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

  const rtkSkipped = process.env.CAVE_TOOLS_STATUS_RTK === "0";
  const rtkGain = rtkSkipped ? null : await fetchRtkGainSummary();
  const verbose = args.includes("--verbose") || args.includes("-v");

  console.log(
    renderStatusCli({
      rtkAvailable: summary.rtkAvailable,
      reductionPct: summary.reductionPct,
      hitRatePct: summary.hitRatePct,
      cacheHits: summary.cacheHits,
      cacheMisses: summary.cacheMisses,
      filesTracked: summary.filesTracked,
      rtkRewrites: summary.rtkRewrites,
      rtkAlreadyWrapped: summary.rtkAlreadyWrapped,
      rtkPassthrough: summary.rtkPassthrough,
      totalCalls: summary.totalCalls,
      rawChars: summary.rawChars,
      compressedChars: summary.compressedChars,
      compressionSavedChars: summary.compressionSavedChars,
      dedupSavedChars: summary.dedupSavedChars,
      savedChars: summary.savedChars,
      tokensSaved: summary.tokensSaved,
      liveSessions: summary.liveSessions,
      endedSessions: summary.endedSessions,
      lastUpdate: summary.lastUpdate,
      hasData: summary.hasData,
      budgets: summary.budgets,
      byTool: summary.byTool,
      rtkGain,
      rtkGainSkipped: rtkSkipped,
      verbose,
    }),
  );
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
} else if (args[0] === "jobs") {
  (async () => {
  const { listJobs } = await import("./storage/db.js");
  const { getJobInfo, stopJob, tailFileBytes } = await import("./runtime/jobs.js");

  const sub = args[1];
  if (sub === "kill") {
    const jobId = args[2];
    if (!jobId) {
      console.error("Usage: cave-tools jobs kill <jobId> [signal]");
      process.exit(1);
    }
    const signal = (args[3] ?? "SIGTERM") as NodeJS.Signals;
    const job = stopJob(jobId, signal);
    if (!job) {
      console.error(`Unknown job: ${jobId}`);
      process.exit(1);
    }
    console.log(
      job.state === "killed"
        ? `${job.jobId} killed (sent ${signal} to process group ${job.pid}).`
        : `${job.jobId} already finished (state: ${job.state}).`,
    );
    process.exit(0);
  }

  if (sub === "log") {
    const jobId = args[2];
    if (!jobId) {
      console.error("Usage: cave-tools jobs log <jobId> [lines]");
      process.exit(1);
    }
    const job = getJobInfo(jobId);
    if (!job) {
      console.error(`Unknown job: ${jobId}`);
      process.exit(1);
    }
    const wanted = Math.max(1, Number(args[3]) || 100);
    const tail = await tailFileBytes(job.logPath, 256 * 1024);
    const lines = tail.split("\n");
    process.stdout.write(lines.length > wanted ? lines.slice(-wanted).join("\n") : tail);
    process.exit(0);
  }

  const jobs = listJobs().map((job) => getJobInfo(job.jobId) ?? job);
  if (jobs.length === 0) {
    console.log("No background jobs recorded.");
    process.exit(0);
  }
  for (const job of jobs) {
    const secs = Math.round(((job.endedAt ?? Date.now()) - job.startedAt) / 1000);
    const cmd = job.command.replace(/\s+/g, " ").trim();
    const short = cmd.length > 80 ? `${cmd.slice(0, 77)}...` : cmd;
    const state =
      job.state === "exited"
        ? `exited(${job.exitCode ?? "?"})`
        : job.state;
    console.log(
      `${job.jobId}  ${state.padEnd(10)} ${String(secs + "s").padEnd(7)} pid=${job.pid}  ${short}`,
    );
    console.log(`  log: ${job.logPath}`);
  }
  process.exit(0);
  })();
} else if (args[0] === "bins") {
  (async () => {
    const { ensureBins, binDir } = await import("./runtime/bins.js");
    const result = await ensureBins({ log: (message) => console.log(message) });
    console.log(`binDir: ${binDir()}`);
    console.log(`rg: ${result.rg ?? "(missing)"}`);
    console.log(`fd: ${result.fd ?? "(missing)"}`);
    process.exit(result.rg ? 0 : 1);
  })();
} else {
  console.error("Usage: cave-tools [mcp|bench|install-agent|init|status [--emit-statusline|--verbose]|jobs [kill <id> [signal]|log <id> [lines]]|bins]");
  process.exit(1);
}
