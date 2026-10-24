// Generates overflow_cases.json from the Node in PATH and the built TypeScript
// CLI (dist/cli.js), so the Go CLI flag tests compare against real
// `cave-tools <tool>` behaviour for JSON numbers outside float64 range
// instead of hand-written literals.
//
// Run from this directory after `pnpm build`:
//   node gen_overflow_cases.mjs
//
// V8's JSON.parse accepts 1e400 as Infinity; Go's encoding/json rejected it,
// which changed CLI array/object flag values. Every case records the exact
// stdout, stderr and exit code for the same argv (and optional stdin).
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../../../../", import.meta.url));
const cli = join(repoRoot, "dist", "cli.js");
const missing = "/nonexistent-cave-tools-parity/x";

const cases = [
  // Array flag items: TS parses [1e400] to [Infinity] and the edit handler
  // rejects it by shape; Go used to fail the flag parse instead.
  { name: "edits-array-number-overflow", argv: ["edit", "--edits", "[1e400]"] },
  {
    name: "edits-start-line-overflow",
    argv: [
      "edit",
      "--edits",
      `[{"file_path":"${missing}","start_line":1e400,"end_line":2,"content":"z"}]`,
    ],
  },
  {
    name: "edits-end-line-overflow",
    argv: [
      "edit",
      "--edits",
      `[{"file_path":"${missing}","start_line":1,"end_line":1e400,"content":"z"}]`,
    ],
  },
  // A "-" value goes through the same coercion after stdin is read.
  { name: "edits-stdin-number-overflow", argv: ["edit", "--edits", "-"], stdin: "[1e400]" },
  // String-item arrays keep Infinity's String() form: both end up counting
  // one path, so this only guards the coercion path.
  { name: "invalidate-file-paths-overflow", argv: ["invalidate", "--file_paths", "[1e400]"] },
  // Number flags reject non-finite values with the same message.
  { name: "read-offset-overflow", argv: ["read", "--offset", "1e400", missing] },
  { name: "read-limit-overflow", argv: ["read", "--limit", "1e400", missing] },
  { name: "bash-timeout-overflow", argv: ["bash", "--timeout", "1e400", "--command", "true"] },
  // Normal numbers must keep their existing behaviour.
  { name: "read-offset-normal", argv: ["read", "--offset", "2", missing] },
  {
    name: "edits-start-line-normal",
    argv: [
      "edit",
      "--edits",
      `[{"file_path":"${missing}","start_line":2,"end_line":3,"content":"z"}]`,
    ],
  },
];

const home = mkdtempSync(join(tmpdir(), "cave-overflow-fixture-"));
const env = {
  PATH: process.env.PATH,
  HOME: home,
  TMPDIR: join(home, "tmp"),
  CAVE_TOOLS_DATA_DIR: join(home, "data"),
};
try {
  const recorded = cases.map((testCase) => {
    const result = spawnSync(process.execPath, [cli, ...testCase.argv], {
      cwd: repoRoot,
      env,
      input: testCase.stdin,
      maxBuffer: 16 * 1024 * 1024,
    });
    if (result.error) throw result.error;
    const record = {
      name: testCase.name,
      argv: testCase.argv,
      exitCode: result.status,
      stdout: result.stdout.toString("utf8"),
      stderr: result.stderr.toString("utf8"),
    };
    if (testCase.stdin !== undefined) record.stdin = testCase.stdin;
    return record;
  });

  const nodeVersion = execFileSync("node", ["--version"], { encoding: "utf8" }).trim();
  const fixture = {
    nodeVersion,
    template: "node dist/cli.js <tool> <argv...>",
    cases: recorded,
  };
  writeFileSync(new URL("./overflow_cases.json", import.meta.url), JSON.stringify(fixture, null, 1) + "\n");
  console.log(`wrote ${recorded.length} overflow cases for ${nodeVersion}`);
} finally {
  rmSync(home, { recursive: true, force: true });
}
