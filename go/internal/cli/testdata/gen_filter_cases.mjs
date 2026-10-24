// Generates filter_cases.json from the Node in PATH and the built TypeScript
// CLI (dist/cli.js), so the Go filter tests compare against real
// `cave-tools filter --json` behaviour instead of hand-written literals.
//
// Run from this directory after `pnpm build`:
//   node gen_filter_cases.mjs
//
// Every case feeds raw stdin bytes (base64 in the fixture) and records the
// exact stdout, stderr and exit code. Invalid UTF-8 and out-of-range JSON
// numbers must round-trip through the same Node decoding rules as stdin.
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { StringDecoder } from "node:string_decoder";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../../../../", import.meta.url));
const cli = join(repoRoot, "dist", "cli.js");

const hex = (value) => Buffer.from(value, "hex");
const text = (value) => Buffer.from(value, "utf8");

const cases = [
  // Invalid UTF-8 on stdin. Node's StringDecoder emits one U+FFFD per maximal
  // invalid subpart; strings.ToValidUTF8 collapsed runs instead.
  { name: "utf8-two-invalid-leads", input: hex("fffe7b7d") },
  { name: "utf8-surrogate-sequence", input: hex("eda080") },
  { name: "utf8-overlong-lead", input: hex("c0af") },
  { name: "utf8-invalid-lead-text", input: hex("61ff62") },
  { name: "utf8-truncated-sequence-eof", input: hex("e282") },
  { name: "utf8-invalid-inside-valid-json", input: hex("7b22636f6d6d616e64223a2263222c227374646f7574223a22fffe227d") },
  { name: "utf8-truncated-inside-json-string", input: hex("7b22636f6d6d616e64223a2263222c227374646f7574223a22e282") },

  // V8 JSON.parse accepts numbers outside float64 range (±Infinity, 400-digit
  // integers) where encoding/json errors when decoding into float64.
  { name: "number-overflow-extra-field", input: text('{"command":"c","stdout":"s","foo":1e400}') },
  { name: "number-overflow-exit-code", input: text('{"command":"c","stdout":"s","exitCode":1e400}') },
  { name: "number-overflow-exit-code-neg", input: text('{"command":"c","stdout":"s","exitCode":-1e400}') },
  { name: "number-overflow-exit-code-1e309", input: text('{"command":"c","stdout":"s","exitCode":1e309}') },
  { name: "number-overflow-nested", input: text('{"command":"c","stdout":"s","x":{"y":-1e400}}') },
  { name: "number-overflow-bigint", input: text(`{"command":"c","stdout":"s","n":${"9".repeat(400)}}`) },
  { name: "number-overflow-array", input: text("[1e400]") },
  { name: "number-underflow", input: text('{"command":"c","stdout":"s","exitCode":1e-400}') },
  { name: "number-underflow-neg", input: text('{"command":"c","stdout":"s","exitCode":-1e-400}') },
  { name: "number-negative-zero", input: text('{"command":"c","stdout":"s","exitCode":-0}') },
  { name: "number-fractional-exit-code", input: text('{"command":"c","stdout":"s","exitCode":1.5}') },
  { name: "number-negative-exit-code", input: text('{"command":"c","stdout":"s","exitCode":-5}') },
  { name: "number-huge-exit-code", input: text('{"command":"c","stdout":"s","exitCode":9007199254740993}') },
  { name: "number-null-exit-code", input: text('{"command":"c","stdout":"s","exitCode":null}') },
  { name: "number-string-exit-code", input: text('{"command":"c","stdout":"s","exitCode":"1"}') },
  { name: "number-mixed-nested", input: text('{"command":"c","stdout":"s","a":[1e400,{"b":1e-400}]}') },

  // JSON.parse strictness around trailing data must stay identical.
  { name: "trailing-garbage", input: text('{"command":"c","stdout":"s"} x') },
  { name: "trailing-second-value", input: text('{"command":"c","stdout":"s"}{"a":1}') },
  { name: "trailing-array-garbage", input: text("[]x") },
  { name: "trailing-whitespace-ok", input: text('{"command":"c","stdout":"s"}   \n') },
  { name: "trailing-number", input: text("1 2") },

  // Shape errors and malformed input (V8 message parity).
  { name: "json-empty", input: text("") },
  { name: "json-whitespace", input: text(" \n\t") },
  { name: "json-null", input: text("null") },
  { name: "json-true", input: text("true") },
  { name: "json-array", input: text("[]") },
  { name: "json-string", input: text('"x"') },
  { name: "json-missing-fields", input: text('{"stdout":"x"}') },
  { name: "json-bom", input: text("\uFEFF{}") },
  { name: "json-not-json", input: text("not json") },
  { name: "json-array-separator", input: text("[1 2]") },
  { name: "json-trailing-comma", input: text('{"a":1,}') },
  { name: "json-leading-zero", input: text("01") },
  { name: "json-multiline", input: text('{\n  "a": 1,\n  "b": 2,\n}') },
  { name: "json-nan", input: text("NaN") },
];

const home = mkdtempSync(join(tmpdir(), "cave-filter-fixture-"));
const env = {
  PATH: process.env.PATH,
  HOME: home,
  TMPDIR: join(home, "tmp"),
  CAVE_TOOLS_DATA_DIR: join(home, "data"),
};
try {
  const recorded = cases.map((testCase) => {
    const result = spawnSync(process.execPath, [cli, "filter", "--json"], {
      cwd: repoRoot,
      env,
      input: testCase.input,
      maxBuffer: 16 * 1024 * 1024,
    });
    if (result.error) throw result.error;
    return {
      name: testCase.name,
      inputBase64: testCase.input.toString("base64"),
      stdinBytes: testCase.input.toString("hex"),
      exitCode: result.status,
      stdout: result.stdout.toString("utf8"),
      stderr: result.stderr.toString("utf8"),
    };
  });

  const nodeVersion = execFileSync("node", ["--version"], { encoding: "utf8" }).trim();
  const fixture = {
    nodeVersion,
    template: "node dist/cli.js filter --json",
    cases: recorded,
  };
  writeFileSync(
    new URL("./filter_cases.json", import.meta.url),
    JSON.stringify(fixture, null, 1) + "\n",
  );

  // Stdin decoding: Node's setEncoding("utf8") goes through StringDecoder,
  // which buffers incomplete sequences across chunks. Record whole-buffer and
  // every chunk split so the Go decoder can prove chunk boundaries do not
  // matter (and that EOF-truncated sequences render as Node prints them).
  const decoderInputs = [
    hex(""),
    hex("fffe7b7d"),
    hex("eda080"),
    hex("c0af"),
    hex("61ff62"),
    hex("e282"),
    hex("e282ac"),
    hex("f09f9880"),
    hex("f09f98"),
    hex("f4908080"),
    hex("f5808080"),
    hex("e080af"),
    hex("e028a1"),
    hex("8080"),
    hex("ff"),
    Buffer.concat([
      Buffer.from("héllo € 😀", "utf8"),
      hex("ed"),
      Buffer.from("tail", "utf8"),
      hex("f09f"),
    ]),
  ];

  const decodeChunks = (bytes, cuts) => {
    const decoder = new StringDecoder("utf8");
    let output = "";
    let start = 0;
    for (const end of cuts) {
      output += decoder.write(bytes.subarray(start, end));
      start = end;
    }
    output += decoder.write(bytes.subarray(start));
    output += decoder.end();
    return output;
  };

  const decoderCases = decoderInputs.map((bytes) => {
    const cuts = [];
    for (let position = 0; position <= bytes.length; position++) cuts.push(position);
    return {
      stdinHex: bytes.toString("hex"),
      whole: bytes.toString("utf8"),
      splits: cuts.map((position) => ({
        cut: position,
        decoded: decodeChunks(bytes, [position]),
      })),
      byteChunks: decodeChunks(
        bytes,
        Array.from({ length: bytes.length }, (_, index) => index),
      ),
    };
  });

  writeFileSync(
    new URL("./decoder_cases.json", import.meta.url),
    JSON.stringify({ nodeVersion, template: "StringDecoder('utf8') chunk splits", cases: decoderCases }, null, 1) + "\n",
  );
  console.log(`wrote ${recorded.length} filter cases and ${decoderCases.length} decoder cases for ${nodeVersion}`);
} finally {
  rmSync(home, { recursive: true, force: true });
}
