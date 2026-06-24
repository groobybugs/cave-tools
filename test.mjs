import { bashTool } from "./dist/tools/bash.js";
import { compressTool } from "./dist/tools/compress.js";
import { editTool } from "./dist/tools/edit.js";
import { applyPatchTool } from "./dist/tools/apply-patch.js";
import { findTool } from "./dist/tools/find.js";
import { grepTool } from "./dist/tools/grep.js";
import { lsTool } from "./dist/tools/ls.js";
import { readTool } from "./dist/tools/read.js";
import { statusTool } from "./dist/tools/status.js";
import { writeTool } from "./dist/tools/write.js";
import { invalidateTool } from "./dist/tools/invalidate.js";
import { websearchTool, parseWebsearchResponse } from "./dist/tools/websearch.js";
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
import {
  addCodebookFile,
  compressWithCodebook,
  getCodebookSize,
} from "./dist/compression/codebook.js";
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
  assert.match(bashSoftFailure.content[0].text, /\[exit: 7\]/);
  assert.match(bashSoftFailure.content[0].text, /err/);
  assert.match(bashSoftFailure.content[0].text, /out/);

  const bashHardFailure = await bashTool.handler({
    command: "sh -c 'exit 7'",
    description: "Test hard failure command",
  });
  assert.equal(bashHardFailure.isError, true);
  assert.match(bashHardFailure.content[0].text, /\[exit: 7\]/);

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
  assert.deepStrictEqual(JSON.parse(compactedJson), JSON.parse(prettyJson), "value identical");
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
  await cleanupArchives(0);
  const smallOutput = "small";
  assert.equal(await archiveIfLarge(smallOutput, "echo small"), null, "small output not archived");

  const bigOutput = "line\n".repeat(10_000);
  const archived = await archiveIfLarge(bigOutput, "seq 10000");
  assert.ok(archived, "big output archived");
  assert.ok(archived.summary.includes("lines archived"), "summary mentions archived lines");

  const expanded = await expandArchive(archived.id);
  assert.equal(expanded, bigOutput, "archive round-trip");
  await cleanupArchives(0);
  assert.equal(await expandArchive(archived.id), null, "cleanup removed archive");
  console.log("Archive tests passed");
  console.log();

  // Codebook tests
  console.log("2h. Testing codebook:");
  addCodebookFile("a.ts", "import { useState } from 'react';\nexport const THEME = 'dark';\n");
  addCodebookFile("b.ts", "import { useState } from 'react';\nexport const THEME = 'dark';\n");
  addCodebookFile("c.ts", "import { useState } from 'react';\nexport const OTHER = 'value';\n");
  assert.ok(getCodebookSize() > 0, "codebook built after 3 files");

  const codebookResult = compressWithCodebook("import { useState } from 'react';\nexport const UNIQUE = 'x';");
  assert.ok(codebookResult.text.includes("[§"), "boilerplate replaced with codebook ref");
  assert.ok(codebookResult.legend.includes("§CODEBOOK"), "legend appended");
  console.log("Codebook tests passed");
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

  console.log("6b. Testing cave__edit improvements:");
  const editImproveDir = mkdtempSync(join(tmpdir(), "cave-edit-improve-"));
  try {
    const crlfFile = join(editImproveDir, "crlf.txt");
    writeFileSync(crlfFile, "line1\r\nline2\r\nline3\r\n", "utf-8");
    const crlfResult = await editTool.handler({
      file_path: crlfFile,
      old_string: "line1\nline2",
      new_string: "changed1\nchanged2",
    });
    assert.equal(crlfResult.isError, undefined, `CRLF fallback should work: ${crlfResult.content[0].text}`);
    const crlfContent = readFileSync(crlfFile, "utf-8");
    assert.ok(crlfContent.includes("changed1\r\nchanged2"), "new_string should be adapted to CRLF");
    assert.ok(crlfContent.includes("\r\nline3\r\n"), "rest of file should keep CRLF");

    const lfFile = join(editImproveDir, "lf.txt");
    writeFileSync(lfFile, "line1\nline2\nline3\n", "utf-8");
    const lfResult = await editTool.handler({
      file_path: lfFile,
      old_string: "line1\r\nline2",
      new_string: "a\r\nb",
    });
    assert.equal(lfResult.isError, undefined, `LF fallback should work: ${lfResult.content[0].text}`);
    const lfContent = readFileSync(lfFile, "utf-8");
    assert.ok(lfContent.includes("a\nb"), "new_string should be adapted to LF");

    const wsFile = join(editImproveDir, "whitespace.txt");
    writeFileSync(wsFile, "  let x = 1;  \n  let y = 2;\n", "utf-8");
    const wsResult = await editTool.handler({
      file_path: wsFile,
      old_string: "  let x = 1;\n  let y = 2;",
      new_string: "  let x = 10;\n  let y = 20;",
    });
    assert.equal(wsResult.isError, undefined, `trailing whitespace tolerance should work: ${wsResult.content[0].text}`);
    const wsContent = readFileSync(wsFile, "utf-8");
    assert.ok(wsContent.includes("let x = 10;"));
    assert.ok(wsContent.includes("let y = 20;"));

    const appliedFile = join(editImproveDir, "applied.txt");
    writeFileSync(appliedFile, "new content here\n", "utf-8");
    const appliedResult = await editTool.handler({
      file_path: appliedFile,
      old_string: "old content",
      new_string: "new content here",
    });
    assert.equal(appliedResult.isError, true);
    assert.match(appliedResult.content[0].text, /already applied/);

    const hintFile = join(editImproveDir, "hint.txt");
    writeFileSync(hintFile, "function myFunction() {\n  return 42;\n}\n", "utf-8");
    const hintResult = await editTool.handler({
      file_path: hintFile,
      old_string: "function myFunction() {",
      new_string: "function myFunction() {",
    });
    assert.equal(hintResult.isError, true);
    assert.match(hintResult.content[0].text, /identical/);

    const hintFile2 = join(editImproveDir, "hint2.txt");
    writeFileSync(hintFile2, "function myFunction() {\n  return 42;\n}\n", "utf-8");
    const hintResult2 = await editTool.handler({
      file_path: hintFile2,
      old_string: "function notHere() {",
      new_string: "function gone() {",
    });
    assert.equal(hintResult2.isError, true);
    assert.match(hintResult2.content[0].text, /Closest match at line 1/);

    const batchFile = join(editImproveDir, "batch.txt");
    writeFileSync(batchFile, "alpha beta gamma delta\n", "utf-8");
    const batchResult = await editTool.handler({
      file_path: batchFile,
      edits: [
        { old_string: "alpha", new_string: "one" },
        { old_string: "gamma", new_string: "three" },
      ],
    });
    assert.equal(batchResult.isError, undefined, `batch edit should work: ${batchResult.content[0].text}`);
    assert.equal(readFileSync(batchFile, "utf-8"), "one beta three delta\n");

    const overlapFile = join(editImproveDir, "overlap.txt");
    writeFileSync(overlapFile, "foo bar baz\n", "utf-8");
    const overlapResult = await editTool.handler({
      file_path: overlapFile,
      edits: [
        { old_string: "foo bar", new_string: "x" },
        { old_string: "bar baz", new_string: "y" },
      ],
    });
    assert.equal(overlapResult.isError, true);
    assert.match(overlapResult.content[0].text, /overlap/);

    const largeFile = join(editImproveDir, "large.txt");
    const largeContent = "pattern\n".repeat(50);
    writeFileSync(largeFile, largeContent, "utf-8");
    const largeResult = await editTool.handler({
      file_path: largeFile,
      old_string: "pattern",
      new_string: "replaced",
    });
    assert.equal(largeResult.isError, true);
    assert.match(largeResult.content[0].text, /not unique/);
  } finally {
    rmSync(editImproveDir, { recursive: true, force: true });
  }
  console.log("cave__edit improvements tests passed");
  console.log();

  console.log("6e. Testing cave__edit fuzzy replacers:");
  const fuzzyDir = mkdtempSync(join(tmpdir(), "cave-edit-fuzzy-"));
  try {
    // LineTrimmed / IndentationFlexible: leading-indent drift.
    const indentFile = join(fuzzyDir, "indent.txt");
    writeFileSync(indentFile, "function f() {\n        return 1;\n}\n", "utf-8");
    const indentResult = await editTool.handler({
      file_path: indentFile,
      old_string: "return 1;", // no indentation supplied
      new_string: "return 2;",
    });
    assert.equal(indentResult.isError, undefined, `indent drift should match: ${indentResult.content[0].text}`);
    assert.ok(readFileSync(indentFile, "utf-8").includes("return 2;"));

    // WhitespaceNormalized: collapsed inner whitespace.
    const wsNormFile = join(fuzzyDir, "wsnorm.txt");
    writeFileSync(wsNormFile, "const   x   =   1;\n", "utf-8");
    const wsNormResult = await editTool.handler({
      file_path: wsNormFile,
      old_string: "const x = 1;",
      new_string: "const x = 2;",
    });
    assert.equal(wsNormResult.isError, undefined, `whitespace-normalized should match: ${wsNormResult.content[0].text}`);
    assert.ok(readFileSync(wsNormFile, "utf-8").includes("const x = 2;"));

    // BlockAnchor: interior line drifted, anchors intact.
    const blockFile = join(fuzzyDir, "block.txt");
    writeFileSync(blockFile, "if (cond) {\n  doSomethingElse();\n}\n", "utf-8");
    const blockResult = await editTool.handler({
      file_path: blockFile,
      old_string: "if (cond) {\n  doSomething();\n}",
      new_string: "if (cond) {\n  done();\n}",
    });
    assert.equal(blockResult.isError, undefined, `block-anchor should match: ${blockResult.content[0].text}`);
    assert.ok(readFileSync(blockFile, "utf-8").includes("done();"));

    // EscapeNormalized: literal \n escape sequences in old_string.
    const escFile = join(fuzzyDir, "esc.txt");
    writeFileSync(escFile, "line one\nline two\n", "utf-8");
    const escResult = await editTool.handler({
      file_path: escFile,
      old_string: "line one\\nline two",
      new_string: "changed",
    });
    assert.equal(escResult.isError, undefined, `escape-normalized should match: ${escResult.content[0].text}`);
    assert.ok(readFileSync(escFile, "utf-8").includes("changed"));
  } finally {
    rmSync(fuzzyDir, { recursive: true, force: true });
  }
  console.log("cave__edit fuzzy replacer tests passed");
  console.log();

  console.log("6c. Testing cave__read force parameter:");
  const readForceDir = mkdtempSync(join(tmpdir(), "cave-read-force-"));
  try {
    const forceFile = join(readForceDir, "force.txt");
    writeFileSync(forceFile, "initial content\n", "utf-8");

    const read1 = await readTool.handler({ file_path: forceFile });
    assert.equal(read1.isError, undefined);
    assert.ok(read1.content[0].text.includes("initial content"));

    const read2 = await readTool.handler({ file_path: forceFile });
    assert.equal(read2.isError, undefined);
    assert.match(read2.content[0].text, /unchanged since last read/);

    const read3 = await readTool.handler({ file_path: forceFile, force: true });
    assert.equal(read3.isError, undefined);
    assert.ok(read3.content[0].text.includes("initial content"), "force should bypass dedup");
  } finally {
    rmSync(readForceDir, { recursive: true, force: true });
  }
  console.log("cave__read force parameter tests passed");
  console.log();

  console.log("6d. Testing cave__read recently-edited bypass:");
  const readRecentDir = mkdtempSync(join(tmpdir(), "cave-read-recent-"));
  try {
    const recentFile = join(readRecentDir, "recent.txt");
    writeFileSync(recentFile, "original content\n", "utf-8");

    const read1 = await readTool.handler({ file_path: recentFile });
    assert.equal(read1.isError, undefined);
    assert.ok(read1.content[0].text.includes("original content"));

    await editTool.handler({
      file_path: recentFile,
      old_string: "original content",
      new_string: "edited content",
    });

    const read2 = await readTool.handler({ file_path: recentFile });
    assert.equal(read2.isError, undefined);
    assert.ok(read2.content[0].text.includes("edited content"), "recently-edited file should bypass dedup");
  } finally {
    rmSync(readRecentDir, { recursive: true, force: true });
  }
  console.log("cave__read recently-edited bypass tests passed");
  console.log();

  console.log("7. Testing cave__write:");
  const writeDir = mkdtempSync(join(tmpdir(), "cave-write-"));
  try {
    // Create a new file via content.
    const newFile = join(writeDir, "new.txt");
    const createResult = await writeTool.handler({
      file_path: newFile,
      content: "hello world",
    });
    assert.equal(createResult.isError, undefined);
    assert.match(createResult.content[0].text, /Wrote 11 chars/);
    assert.equal(readFileSync(newFile, "utf-8"), "hello world");

    // Overwrite an existing file.
    const overwriteResult = await writeTool.handler({
      file_path: newFile,
      content: "replaced",
    });
    assert.equal(overwriteResult.isError, undefined);
    assert.equal(readFileSync(newFile, "utf-8"), "replaced");

    // content: "" now writes an empty file (no longer a cache-only no-op).
    const emptyContentFile = join(writeDir, "empty.txt");
    writeFileSync(emptyContentFile, "to be cleared", "utf-8");
    const emptyContentResult = await writeTool.handler({
      file_path: emptyContentFile,
      content: "",
    });
    assert.equal(emptyContentResult.isError, undefined);
    assert.equal(readFileSync(emptyContentFile, "utf-8"), "");

    // truncate empties a file.
    const truncateFile = join(writeDir, "truncate.txt");
    writeFileSync(truncateFile, "will be emptied", "utf-8");
    const truncateResult = await writeTool.handler({
      file_path: truncateFile,
      truncate: true,
    });
    assert.equal(truncateResult.isError, undefined);
    assert.match(truncateResult.content[0].text, /Truncated/);
    assert.equal(readFileSync(truncateFile, "utf-8"), "");

    // Neither content nor truncate is an error (points at cave__invalidate).
    const noOpResult = await writeTool.handler({ file_path: newFile });
    assert.equal(noOpResult.isError, true);
    assert.match(noOpResult.content[0].text, /cave__invalidate/);

    const nestedFile = join(writeDir, "nested", "created.txt");
    const nestedResult = await writeTool.handler({
      file_path: nestedFile,
      content: "nested",
    });
    assert.equal(nestedResult.isError, undefined);
    assert.equal(readFileSync(nestedFile, "utf-8"), "nested");

    const bomFile = join(writeDir, "bom.txt");
    writeFileSync(bomFile, "\uFEFFbefore", "utf-8");
    const bomResult = await writeTool.handler({
      file_path: bomFile,
      content: "after",
    });
    assert.equal(bomResult.isError, undefined);
    assert.equal(readFileSync(bomFile, "utf-8"), "\uFEFFafter");
  } finally {
    rmSync(writeDir, { recursive: true, force: true });
  }
  console.log("cave__write tests passed");
  console.log();

  console.log("7a. Testing cave__apply_patch:");
  const patchDir = mkdtempSync(join(tmpdir(), "cave-patch-"));
  try {
    const addFile = join(patchDir, "added.txt");
    const patchAdd = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${addFile}\n+one\n+two\n*** End Patch`,
    });
    assert.equal(patchAdd.isError, undefined, patchAdd.content[0].text);
    assert.equal(readFileSync(addFile, "utf-8"), "one\ntwo\n");

    const patchUpdate = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${addFile}\n@@\n-one\n+ONE\n two\n*** End Patch`,
    });
    assert.equal(patchUpdate.isError, undefined, patchUpdate.content[0].text);
    assert.equal(readFileSync(addFile, "utf-8"), "ONE\ntwo\n");

    const patchDelete = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Delete File: ${addFile}\n*** End Patch`,
    });
    assert.equal(patchDelete.isError, undefined, patchDelete.content[0].text);
    assert.throws(() => readFileSync(addFile, "utf-8"));
  } finally {
    rmSync(patchDir, { recursive: true, force: true });
  }
  console.log("cave__apply_patch tests passed");
  console.log();

  console.log("7b. Testing cave__invalidate:");
  const invalidateDir = mkdtempSync(join(tmpdir(), "cave-invalidate-"));
  try {
    const fileA = join(invalidateDir, "a.txt");
    const fileB = join(invalidateDir, "b.txt");
    writeFileSync(fileA, "keep a", "utf-8");
    writeFileSync(fileB, "keep b", "utf-8");

    const multiResult = await invalidateTool.handler({
      file_paths: [fileA, fileB],
    });
    assert.equal(multiResult.isError, undefined);
    assert.match(multiResult.content[0].text, /Invalidated cache for 2/);
    // No disk writes.
    assert.equal(readFileSync(fileA, "utf-8"), "keep a");
    assert.equal(readFileSync(fileB, "utf-8"), "keep b");

    const emptyResult = await invalidateTool.handler({ file_paths: [] });
    assert.equal(emptyResult.isError, true);
    assert.match(emptyResult.content[0].text, /non-empty array/);
  } finally {
    rmSync(invalidateDir, { recursive: true, force: true });
  }
  console.log("cave__invalidate tests passed");
  console.log();

  console.log("7c. Testing cave__websearch:");
  const directPayload = JSON.stringify({
    result: { content: [{ type: "text", text: "direct web result" }] },
  });
  assert.equal(parseWebsearchResponse(directPayload), "direct web result");
  assert.equal(parseWebsearchResponse(`event: message\ndata: ${directPayload}\n\n`), "direct web result");

  const originalFetch = globalThis.fetch;
  try {
    let requestedUrl = "";
    let requestedBody = "";
    globalThis.fetch = async (url, init) => {
      requestedUrl = String(url);
      requestedBody = String(init?.body || "");
      return new Response(directPayload, {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    };
    const webResult = await websearchTool.handler({
      query: "latest cave tools news",
      provider: "exa",
      numResults: 2,
      timeout: 1,
    });
    assert.equal(webResult.isError, undefined, webResult.content[0].text);
    assert.match(webResult.content[0].text, /direct web result/);
    assert.match(requestedUrl, /mcp\.exa\.ai/);
    assert.match(requestedBody, /web_search_exa/);
  } finally {
    globalThis.fetch = originalFetch;
  }
  console.log("cave__websearch tests passed");
  console.log();

  // Test status
  console.log("8. Testing cave__status:");
  const statusResult = await statusTool.handler({});
  console.log(statusResult.content[0].text);
  console.log();

  console.log("All tests passed!");
}

test().catch(console.error);
