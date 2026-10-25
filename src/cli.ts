#!/usr/bin/env node

import { existsSync, writeFileSync } from "fs";
import { join } from "path";
import { isToolName } from "./dispatch.js";
import { redactSecrets } from "./compression/redact.js";
import { fetchRtkGainSummary, renderStatusCli } from "./status-ui.js";
import {
  buildStatuslineSuffix,
  buildStatusSummary,
  writeStatuslineSuffix,
} from "./statusline.js";
import { maybeRunGoImplementation } from "./impl-switch.js";

// Static imports above always evaluate before this hook, so the switch cannot
// literally precede them without splitting the entry file. This is still the
// earliest executable point: no CLI command runs before a selected Go process
// starts. Static module initialization from the imports above still runs;
// lazy server/tool/daemon imports and command dispatch do not. Without the
// switch it returns immediately and the TypeScript CLI below is unchanged.
await maybeRunGoImplementation();

const CAVE_INSTRUCTIONS = `# Cave Tools — token-optimized MCP

Cave Tools provides optimized drop-in replacements for shell + file ops (RTK
rewriting, structured JSON/XML extraction, per-tool line budgets). Same results,
fewer tokens. Prefer file/web/edit replacements over built-ins; use shell routing below.

## Tools
- \`cave__read\` instead of Read — optimized drop-in replacement (dedup cache + line budgets).
- \`cave__grep\`, \`cave__find\`, \`cave__ls\` instead of shell/Glob/Grep.
- Shell: native shell is the default on Claude Code / opencode, with output auto-compressed through the CLI filter (Claude: successful commands only). Use \`cave__bash\` for RTK pre-rewrite or guaranteed failure compression — never prefix \`cave-tools\` or \`rtk\` yourself. Other hosts: use \`cave__bash\` (max 10min), or \`cave-tools bash -- '<cmd>'\` from the shell if MCP is down.
- Long jobs: native background for servers/watchers (uncompressed); \`cave__bash_start\` when a long job's final output should be compressed — poll \`cave__bash_status\` (\`wait\` up to 60s), stop \`cave__bash_stop\`. \`cave-tools jobs\` lists/kills jobs from a terminal.
- \`[cave: full output … at <path>]\`: \`cave__read\` offset/limit or \`cave__grep\` that path instead of re-running. Unfiltered: \`cave-tools raw <cmd>\` / \`CAVE_RAW=1\`. \`CAVE_TOOLS_FILTER_BASH=0\` disables the native filter; \`CAVE_TOOLS_WRAP_BASH=1\` wraps commands before they run (changes which permission/deny rules match).
- \`cave__compress\` to optimize large pasted or tool-produced text down to fewer tokens.
- \`cave__status\` to inspect savings, cache hit rate, reduction % + budgets.
- \`cave__write\` to create/overwrite a single file; \`cave__edit\` for fuzzy string replacement.
- After editing a file outside Cave Tools, call \`cave__invalidate\` to refresh the read dedup cache.

## Rules
- Do not double-wrap: never run \`rtk <cmd>\` inside \`cave__bash\` (it already prepends rtk).
- Pass raw file paths / patterns / commands — do not wrap tools in extra scripts.
- Never \`sleep\`-poll to wait for long tasks.
- Interactive commands (vim, less, ssh) and hook-sensitive stdin: native shell.

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
  // Lazy: the server pulls in the MCP SDK and every tool module.
  import("./index.js").then(({ startServer }) => startServer()).catch((error) => {
    console.error("Fatal error:", error);
    process.exit(1);
  });
} else if (args[0] === "status") {
  (async () => {
  // --emit-statusline: write compact one-liner to ~/.claude/.cave-tools-statusline-suffix
  // for the statusline script to read. No stdout output. Used by SessionStart and
  // the MCP server's in-process refresh to keep the badge fresh without
  // re-rendering on every keystroke.
  if (args.includes("--emit-statusline")) {
    // Only the aggregates the badge needs: no rtk probe, no by-tool table.
    writeStatuslineSuffix(buildStatuslineSuffix());
    process.exit(0);
  }
  const summary = await buildStatusSummary();

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
    const cmd = redactSecrets(job.command).replace(/\s+/g, " ").trim();
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
} else if (args[0] === "daemon") {
  const sub = args[1] ?? "status";
  if (sub === "start") {
    import("./daemon/server.js").then(({ startDaemon }) => startDaemon()).catch((error) => {
      console.error("daemon failed:", error);
      process.exit(1);
    });
  } else if (sub === "stop" || sub === "status") {
    void import("./daemon/control.js").then(async (control) =>
      process.exit(await (sub === "stop" ? control.daemonStop() : control.daemonStatus())),
    );
  } else {
    console.error("Usage: cave-tools daemon [start|stop|status]");
    process.exit(2);
  }
} else if (args[0] === "errors") {
  void import("./runtime/tool-errors-cli.js").then(({ runErrorsCommand }) =>
    runErrorsCommand(args.slice(1)).then((code) => process.exit(code)),
  );
} else if (args[0] === "help" || args[0] === "--help" || args[0] === "-h") {
  void import("./cli-tools.js").then(({ runHelpCommand }) => runHelpCommand(args[1]));
} else if (args[0] === "raw") {
  void import("./cli-tools.js").then(({ runRawCommand }) => runRawCommand(args.slice(1)));
} else if (args[0] === "filter") {
  void import("./cli-tools.js").then(({ runFilterCommand }) => runFilterCommand(args.slice(1)));
} else if (args[0] === "tool") {
  // Explicit form; the only way to reach the status tool, since `status` is the savings report above.
  void import("./cli-tools.js").then(({ runToolCommand }) => runToolCommand(args[1], args.slice(2)));
} else if (isToolName(`cave__${args[0]}`)) {
  void import("./cli-tools.js").then(({ runToolCommand }) => runToolCommand(args[0], args.slice(1)));
} else {
    console.error("Usage: cave-tools [mcp|bench|install-agent|init|status [--emit-statusline|--verbose]|errors [--limit N]|jobs [kill <id> [signal]|log <id> [lines]]|bins|daemon [start|stop|status]|raw <command...>|filter --json [--session <id>]|help [tool]|<tool> [args...]|tool <tool> [args...]]");
  process.exit(2);
}
