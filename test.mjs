import { bashTool } from "./dist/tools/bash.js";
import { compressTool } from "./dist/tools/compress.js";
import { editTool } from "./dist/tools/edit.js";
import { findTool } from "./dist/tools/find.js";
import { grepTool } from "./dist/tools/grep.js";
import { lsTool } from "./dist/tools/ls.js";
import { readTool } from "./dist/tools/read.js";
import { statusTool } from "./dist/tools/status.js";
import { writeTool } from "./dist/tools/write.js";
import {
  compactJson,
  compactJsonl,
  extractStructuredData,
  recordRead,
  recordEdit,
  shouldForceFull,
  getBounceStats,
} from "./dist/compression/utils.js";
import { classifyCommand } from "./dist/compression/classify.js";
import {
  archiveIfLarge,
  expandArchive,
  cleanupArchives,
} from "./dist/compression/archive.js";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

async function test() {
  console.log("Testing Cave Tools...\n");

  // Test bash
  console.log("1. Testing cave__bash:");
  const bashResult = await bashTool.handler({
    command: "echo 'Hello from Cave Tools!'",
    description: "Test echo command",
  });
  console.log(bashResult.content[0].text);
  console.log();

  const bashSoftFailure = await bashTool.handler({
    command: "sh -c 'echo out; echo err >&2; exit 7'",
    description: "Test soft failure command",
    allowFailure: true,
  });
  assert.equal(bashSoftFailure.isError, undefined);
  assert.match(bashSoftFailure.content[0].text, /Exit code: 7/);
  assert.match(bashSoftFailure.content[0].text, /stderr:\nerr/);
  assert.match(bashSoftFailure.content[0].text, /stdout:\nout/);

  const bashHardFailure = await bashTool.handler({
    command: "sh -c 'exit 7'",
    description: "Test hard failure command",
  });
  assert.equal(bashHardFailure.isError, true);
  assert.match(bashHardFailure.content[0].text, /Exit code: 7/);

  // Secret redaction tests
  console.log("1b. Testing secret redaction:");
  const tokenOutput = await bashTool.handler({
    command: "echo 'Authorization: Bearer sk-live-abc123xyz789secret'",
    description: "Test bearer token redaction",
  });
  assert.match(tokenOutput.content[0].text, /\[REDACTED:Bearer token\]/);
  assert.doesNotMatch(tokenOutput.content[0].text, /sk-live-abc123xyz789secret/);

  const apiKeyOutput = await bashTool.handler({
    command: "echo 'api_key=AKIAIOSFODNN7EXAMPLE'",
    description: "Test API key redaction",
  });
  assert.match(apiKeyOutput.content[0].text, /\[REDACTED:AWS key\]/);

  const noRedactOutput = await bashTool.handler({
    command: "echo 'api_key=AKIAIOSFODNN7EXAMPLE'",
    description: "Test redaction opt-out",
    redact_secrets: false,
  });
  assert.match(noRedactOutput.content[0].text, /AKIAIOSFODNN7EXAMPLE/);
  console.log("Secret redaction tests passed");
  console.log();

  // Test compress
  console.log("2. Testing cave__compress:");
  const compressResult = await compressTool.handler({
    text: "Line 1\n\n\n\nLine 2\n\n\nLine 3",
    tool_name: "bash",
  });
  console.log(compressResult.content[0].text);
  console.log();

  // JSON compaction tests
  console.log("2b. Testing JSON compaction:");
  const prettyJson = '{\n  "name": "cave-tools",\n  "version": 3,\n  "tags": ["a", "b"]\n}';
  const compactedJson = compactJson(prettyJson);
  assert.ok(compactedJson, "pretty JSON should compact");
  assert.equal(JSON.parse(compactedJson), JSON.parse(prettyJson), "value identical");
  assert.ok(!compactedJson.includes("\n"), "no newlines in compacted JSON");

  const jsonlInput = '{ "a": 1 }\n{ "b": 2 }\n\n{ "c": 3 }';
  const compactedJsonl = compactJsonl(jsonlInput);
  assert.equal(compactedJsonl, '{"a":1}\n{"b":2}\n{"c":3}', "JSONL compacted");

  const alreadyMinified = '{"a":1,"b":[2,3]}';
  assert.equal(compactJson(alreadyMinified), null, "minified JSON no-op");

  const stringWithSpaces = '{\n  "msg": "hello   world\\n\\ttab"\n}';
  const compactedString = compactJson(stringWithSpaces);
  assert.ok(compactedString?.includes("hello   world"), "inner spaces preserved");
  assert.ok(compactedString?.includes("\\n\\ttab"), "escapes preserved");

  assert.equal(extractStructuredData("not json"), "not json", "non-JSON passthrough");
  console.log("JSON compaction tests passed");
  console.log();

  // Bounce tracking tests
  console.log("2c. Testing bounce tracking:");
  recordRead("src/a.rs", true, 50);
  recordRead("src/a.rs", false, 500);
  let bounceStats = getBounceStats();
  assert.equal(bounceStats.totalBounces, 1, "detect compressed -> full bounce");
  assert.equal(bounceStats.totalWastedChars, 50, "wasted chars from compressed read");

  recordRead("src/b.rs", true, 30);
  recordRead("src/b.rs", false, 400);
  assert.ok(shouldForceFull("src/c.rs"), "high bounce rate extension forces full");

  recordEdit("src/edited.rs");
  assert.ok(shouldForceFull("src/edited.rs"), "recent edit forces full");
  console.log("Bounce tracking tests passed");
  console.log();

  // Command classification tests
  console.log("2d. Testing command classification:");
  assert.equal(classifyCommand("npm run dev"), "passthrough", "dev server passthrough");
  assert.equal(classifyCommand("gh auth login"), "passthrough", "auth passthrough");
  assert.equal(classifyCommand("curl https://api.example.com"), "verbatim", "curl verbatim");
  assert.equal(classifyCommand("gh api repos/owner/repo/issues"), "verbatim", "gh api verbatim");
  assert.equal(classifyCommand("cargo test"), "compressible", "test compressible");
  assert.equal(classifyCommand("git status"), "compressible", "git status compressible");
  console.log("Command classification tests passed");
  console.log();

  // Signatures read mode tests
  console.log("2e. Testing signatures read mode:");
  const sigsDir = mkdtempSync(join(tmpdir(), "cave-sigs-"));
  try {
    const tsFile = join(sigsDir, "sample.ts");
    writeFileSync(
      tsFile,
      "export async function loadUser(id: string): Promise<User> {\n  return db.get(id);\n}\n\nclass UserService {\n  constructor() {}\n}\n\nexport interface User {\n  id: string;\n}\n",
      "utf-8",
    );

    const sigResult = await readTool.handler({
      file_path: tsFile,
      mode: "signatures",
    });
    const sigText = sigResult.content[0].text;
    assert.match(sigText, /export async fn loadUser\(id: string\) → Promise<User>/);
    assert.match(sigText, /class UserService/);
    assert.match(sigText, /export interface User/);
    assert.doesNotMatch(sigText, /return db.get/);
  } finally {
    rmSync(sigsDir, { recursive: true, force: true });
  }
  console.log("Signatures read mode tests passed");
  console.log();

  // Aggressive read mode tests
  console.log("2f. Testing aggressive read mode:");
  const aggDir = mkdtempSync(join(tmpdir(), "cave-agg-"));
  try {
    const pyFile = join(aggDir, "sample.py");
    writeFileSync(
      pyFile,
      "#!/usr/bin/env python3\n# This is a comment\n\ndef hello():\n    # inline comment\n    return 'world'\n\n\nclass Foo:\n    pass\n",
      "utf-8",
    );

    const aggResult = await readTool.handler({
      file_path: pyFile,
      mode: "aggressive",
    });
    const aggText = aggResult.content[0].text;
    assert.ok(aggText.includes("#!/usr/bin/env python3"), "shebang preserved");
    assert.doesNotMatch(aggText, /# This is a comment/);
    assert.ok(aggText.includes("def hello():"));
    assert.ok(aggText.includes("return 'world'"));
  } finally {
    rmSync(aggDir, { recursive: true, force: true });
  }
  console.log("Aggressive read mode tests passed");
  console.log();

  // Archive tests
  console.log("2g. Testing archive:");
  cleanupArchives(0);
  const smallOutput = "small";
  assert.equal(archiveIfLarge(smallOutput, "echo small"), null, "small output not archived");

  const bigOutput = "line\n".repeat(10_000);
  const archived = archiveIfLarge(bigOutput, "seq 10000");
  assert.ok(archived, "big output archived");
  assert.ok(archived.summary.includes("lines archived"), "summary mentions archived lines");

  const expanded = expandArchive(archived.id);
  assert.equal(expanded, bigOutput, "archive round-trip");
  cleanupArchives(0);
  assert.equal(expandArchive(archived.id), null, "cleanup removed archive");
  console.log("Archive tests passed");
  console.log();

  console.log("3. Testing cave__grep:");
  const grepResult = await grepTool.handler({
    pattern: "Cave Tools",
    path: ".",
    glob: "README.md",
    limit: 5,
  });
  console.log(grepResult.content[0].text);
  console.log();

  console.log("4. Testing cave__find:");
  const findResult = await findTool.handler({
    pattern: "*.md",
    path: ".",
    limit: 5,
  });
  console.log(findResult.content[0].text);
  console.log();

  console.log("5. Testing cave__ls:");
  const lsResult = await lsTool.handler({ path: ".", limit: 10 });
  console.log(lsResult.content[0].text);
  console.log();

  console.log("6. Testing cave__edit:");
  const editDir = mkdtempSync(join(tmpdir(), "cave-edit-"));
  try {
    const filePath = join(editDir, "sample.txt");
    writeFileSync(filePath, "alpha beta beta\n", "utf-8");

    const uniqueResult = await editTool.handler({
      file_path: filePath,
      old_string: "alpha",
      new_string: "omega",
    });
    assert.equal(uniqueResult.isError, undefined);
    assert.equal(readFileSync(filePath, "utf-8"), "omega beta beta\n");

    const nonUniqueResult = await editTool.handler({
      file_path: filePath,
      old_string: "beta",
      new_string: "gamma",
    });
    assert.equal(nonUniqueResult.isError, true);

    const replaceAllResult = await editTool.handler({
      file_path: filePath,
      old_string: "beta",
      new_string: "gamma",
      replace_all: true,
    });
    assert.equal(replaceAllResult.isError, undefined);
    assert.equal(readFileSync(filePath, "utf-8"), "omega gamma gamma\n");

    const emptyResult = await editTool.handler({
      file_path: filePath,
      old_string: "",
      new_string: "x",
      replace_all: true,
    });
    assert.equal(emptyResult.isError, true);
    assert.match(emptyResult.content[0].text, /old_string cannot be empty/);
  } finally {
    rmSync(editDir, { recursive: true, force: true });
  }
  console.log("cave__edit tests passed");
  console.log();

  console.log("7. Testing cave__write:");
  const writeDir = mkdtempSync(join(tmpdir(), "cave-write-"));
  try {
    const filePath = join(writeDir, "sample.txt");
    writeFileSync(filePath, "keep me", "utf-8");

    const invalidateResult = await writeTool.handler({
      file_paths: [filePath],
    });
    assert.equal(invalidateResult.isError, undefined);
    assert.match(invalidateResult.content[0].text, /Invalidated cache/);
    assert.equal(readFileSync(filePath, "utf-8"), "keep me");

    const emptyWriteResult = await writeTool.handler({
      file_paths: [filePath],
      content: "",
    });
    assert.equal(emptyWriteResult.isError, undefined);
    assert.equal(readFileSync(filePath, "utf-8"), "");
  } finally {
    rmSync(writeDir, { recursive: true, force: true });
  }
  console.log("cave__write tests passed");
  console.log();

  // Test status
  console.log("8. Testing cave__status:");
  const statusResult = await statusTool.handler({});
  console.log(statusResult.content[0].text);
  console.log();

  console.log("All tests passed!");
}

test().catch(console.error);
