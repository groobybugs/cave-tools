#!/usr/bin/env node
// Sequential test runner for the full suite and the dedicated regression set.
//
// `pnpm test` builds once, then runs test.mjs plus every dedicated script.
// `pnpm test:reliability` builds once, then runs only the dedicated scripts
// (all of which import dist/). This runner never builds and never repeats a
// build per child.
//
// Every child gets its own CAVE_TOOLS_DATA_DIR and CLAUDE_CONFIG_DIR under a
// single private mkdtemp root (`/tmp/opencode` on Linux, system temp
// elsewhere). That keeps SQLite state, logs, jobs, hooks, and the read registry
// away from a developer's live cache and ~/.claude. Only this runner's root is
// removed in the finally block.
//
// Usage:
//   node scripts/run-tests.mjs
//   node scripts/run-tests.mjs --reliability-only
//   node scripts/run-tests.mjs --list [--reliability-only]
//   node scripts/run-tests.mjs --help

import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const DEDICATED_SUITE = [
  { name: "edit-reliability", script: "scripts/test-edit-reliability.mjs" },
  { name: "read-registry", script: "scripts/test-read-registry.mjs" },
  { name: "tool-errors", script: "scripts/test-tool-errors.mjs" },
  { name: "edit-v2", script: "scripts/test-edit-v2.mjs" },
  { name: "line-range-reliability", script: "scripts/test-line-range-reliability.mjs" },
  { name: "edit-handler-v2", script: "scripts/test-edit-handler-v2.mjs" },
  { name: "file-mutation-locks", script: "scripts/test-file-mutation-locks.mjs" },
];

const FULL_SUITE = [{ name: "core", script: "test.mjs" }, ...DEDICATED_SUITE];

function usage() {
  console.log(`Usage: node scripts/run-tests.mjs [--reliability-only] [--list] [--help]

  (no args)            run test.mjs + every dedicated regression script
  --reliability-only   run only the dedicated regression script set
  --list               print the selected scripts without running them
  --help, -h           show this help

Build once before running (pnpm test / pnpm test:reliability do this):
  pnpm build
  node scripts/run-tests.mjs`);
}

function parseArgs(argv) {
  let reliabilityOnly = false;
  let list = false;
  for (const arg of argv) {
    if (arg === "--reliability-only") reliabilityOnly = true;
    else if (arg === "--list") list = true;
    else if (arg === "--help" || arg === "-h") {
      usage();
      process.exit(0);
    } else {
      console.error(`[tests] unknown argument: ${arg}`);
      usage();
      process.exit(2);
    }
  }
  return { reliabilityOnly, list };
}

function createFixtureRoot() {
  const preferred = process.platform === "linux" ? join("/tmp", "opencode") : tmpdir();
  try {
    mkdirSync(preferred, { recursive: true, mode: 0o700 });
    return mkdtempSync(join(preferred, "cave-tools-tests-"));
  } catch {
    const fallback = tmpdir();
    mkdirSync(fallback, { recursive: true, mode: 0o700 });
    return mkdtempSync(join(fallback, "cave-tools-tests-"));
  }
}

function runSuite(suite) {
  const root = createFixtureRoot();
  let failureCode = null;
  try {
    suite.forEach((test, index) => {
      if (failureCode !== null) return;
      const scriptPath = join(ROOT, test.script);
      if (!existsSync(scriptPath)) {
        console.error(`[tests] missing dedicated test script: ${test.script}`);
        failureCode = 1;
        return;
      }

      // Unique per child so a test that ignores one var cannot contaminate
      // the next child's registry, SQLite store, logs, or jobs.
      const childRoot = join(root, `${String(index).padStart(2, "0")}-${test.name}`);
      const dataDir = join(childRoot, "data");
      const claudeDir = join(childRoot, "claude");
      mkdirSync(dataDir, { recursive: true, mode: 0o700 });
      mkdirSync(claudeDir, { recursive: true, mode: 0o700 });

      console.log(`\n[tests] ${test.name}: ${test.script}`);
      const result = spawnSync(process.execPath, [scriptPath], {
        cwd: ROOT,
        stdio: "inherit",
        env: {
          ...process.env,
          CAVE_TOOLS_DATA_DIR: dataDir,
          CLAUDE_CONFIG_DIR: claudeDir,
          CAVE_TOOLS_DAEMON: "0",
        },
      });

      if (result.error) {
        console.error(`[tests] ${test.name} failed to start: ${result.error.message}`);
        failureCode = 1;
      } else if (result.signal) {
        console.error(`[tests] ${test.name} was terminated by ${result.signal}`);
        failureCode = 1;
      } else if (result.status !== 0) {
        console.error(`[tests] ${test.name} failed with exit code ${result.status}`);
        failureCode = result.status ?? 1;
      }
    });
  } finally {
    try {
      rmSync(root, { recursive: true, force: true });
    } catch (error) {
      console.error(`[tests] failed to remove fixture root ${root}: ${error.message}`);
      if (failureCode === null) failureCode = 1;
    }
  }
  return failureCode;
}

const { reliabilityOnly, list } = parseArgs(process.argv.slice(2));
const suite = reliabilityOnly ? DEDICATED_SUITE : FULL_SUITE;

if (list) {
  for (const test of suite) console.log(`${test.name}\t${test.script}`);
  process.exit(0);
}

const failureCode = runSuite(suite);
if (failureCode !== null) {
  console.error(`\n[tests] suite failed (exit ${failureCode})`);
  process.exit(failureCode || 1);
}
console.log(`\n[tests] ${suite.length} suites passed`);
