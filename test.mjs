import { bashTool } from "./dist/tools/bash.js";
import { bashStartTool } from "./dist/tools/bash-start.js";
import { bashStatusTool } from "./dist/tools/bash-status.js";
import { bashStopTool } from "./dist/tools/bash-stop.js";
import {
  getJobInfo,
  pruneJobs,
  readFileChunk,
  resultPathFor,
  waitForJob,
} from "./dist/runtime/jobs.js";
import { insertJob, deleteJob } from "./dist/storage/db.js";
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
import { webfetchTool } from "./dist/tools/webfetch.js";
import { configureTool } from "./dist/tools/configure.js";
import {
  compactJson,
  compactJsonl,
  collapseRepeatedLines,
  extractStructuredData,
  recordRead,
  recordEdit,
  shouldForceFull,
  getBounceStats,
  getAllBudgets,
  applyBudget,
  truncateChars,
  fitsBudget,
  isSubagentContext,
  resetSubagentContextCache,
  updateFileCache,
  isFileUnchanged,
  getCacheStats,
  buildStats,
  resetCache,
  stripLeadingRtk,
  rewriteCommandWithRtk,
  getRtkStats,
  getRtkTopPassthrough,
  resetRtkAvailabilityCache,
} from "./dist/compression/utils.js";
import {
  collapseRepetition,
  repetitionMarker,
} from "./dist/compression/repetition.js";
import { writeIfUnchanged } from "./dist/runtime/file-mutation.js";
import {
  compressStructuredSemantic,
  isStructuredCompressionEnabled,
  setStructuredCompression,
} from "./dist/compression/structured.js";
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
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync, existsSync, unlinkSync, chmodSync, utimesSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, delimiter as pathDelimiter } from "node:path";
import { createServer } from "node:http";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { redactSecrets } from "./dist/compression/redact.js";
import {
  getSnapshot,
  getSnapshotStats,
  rememberSnapshot,
  resetSnapshots,
  MAX_SNAPSHOT_FILE_BYTES,
  MAX_SNAPSHOT_TOTAL_BYTES,
} from "./dist/compression/snapshots.js";
import { listedTools } from "./dist/index.js";

async function test() {
  console.log("Testing Cave Tools...\n");

  // Codex's default MCP approval mode (`auto`) skips the prompt only for
  // readOnlyHint tools; subagents cannot answer a prompt, so a missing hint
  // makes every call fail there. Mutating tools must stay unhinted.
  console.log("0. Testing tool annotations:");
  for (const tool of [readTool, grepTool, findTool, lsTool, statusTool, bashStatusTool,
    compressTool, invalidateTool, webfetchTool, websearchTool]) {
    assert.equal(tool.annotations?.readOnlyHint, true, `${tool.name} readOnlyHint`);
  }
  for (const tool of [bashTool, bashStartTool, bashStopTool, writeTool, editTool,
    applyPatchTool, configureTool]) {
    assert.notEqual(tool.annotations?.readOnlyHint, true, `${tool.name} must not be readOnly`);
  }
  console.log("tool annotation tests passed\n");

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

  // Semantic structured compression tests
  console.log("2b2. Testing semantic structured compression:");
  assert.ok(isStructuredCompressionEnabled(), "semantic compression defaults to enabled");

  // Build a JSON blob big enough to clear the 50-line gate, with nested
  // arrays, a long string, and many top-level keys.
  const bigValue = "x".repeat(250);
  const manyItems = Array.from({ length: 10 }, (_, i) => ({ idx: i, value: bigValue }));
  const nestedObj = {
    metadata: { name: "demo", version: 1 },
    spec: { replicas: 3, image: "demo:1.0" },
    status: { ready: true, conditions: ["ok", "ok", "ok"] },
    items: manyItems,
    extra1: "filler1",
    extra2: "filler2",
    extra3: "filler3",
    extra4: "filler4",
    extra5: "filler5",
  };
  const bigJson = JSON.stringify(nestedObj, null, 2);
  // Force >50 lines by repeating the structure in an outer array.
  const wrappedJson = JSON.stringify(
    Array.from({ length: 60 }, (_, i) => ({ ...nestedObj, idx: i })),
    null,
    2,
  );

  assert.ok(wrappedJson.split("\n").length > 50, "fixture must clear 50-line gate");

  // No command hint → generic path keeps first 8 top-level keys and
  // exercises string truncation inside the nested array. With a kubectl
  // hint the `items` array would be filtered out before strings could be
  // truncated, so we deliberately use a non-matching command here.
  const compressed = compressStructuredSemantic(wrappedJson, "my-command");
  assert.ok(compressed !== null, "semantic compression returns a result for big JSON");
  assert.ok(compressed.includes("[JSON compressed:"), "JSON annotation footer present");
  assert.ok(compressed.split("\n").length < wrappedJson.split("\n").length, "compressed JSON is shorter");
  assert.ok(compressed.includes("..."), "long arrays elided into '...' summaries");
  assert.ok(compressed.includes("chars)"), "long strings truncated with char count");

  // Hint-driven path: kubectl keeps only metadata/spec/status at the top
  // level and records the retained keys in the footer annotation.
  const kubectlCompressed = compressStructuredSemantic(
    wrappedJson,
    "kubectl get pods",
  );
  assert.ok(kubectlCompressed !== null, "hint path returns a result");
  assert.ok(
    kubectlCompressed.includes("Keys retained: metadata, spec, status"),
    "kubectl hint keys advertised in footer",
  );

  // Non-JSON input → null (no crash, no fallback inside this function).
  const nonStructured = "not even close to JSON or XML\n".repeat(60);
  assert.equal(compressStructuredSemantic(nonStructured, "ls"), null, "text input falls through");

  // Small JSON that doesn't clear the 50-line gate → null.
  const smallJson = JSON.stringify({ a: 1, b: 2, c: [1, 2, 3] }, null, 2);
  assert.equal(compressStructuredSemantic(smallJson, "echo"), null, "small JSON skipped");

  // XML with namespace boilerplate → namespaces stripped. Need >50 lines so the
  // input clears the gate; many sibling <item> elements exercise the
  // repetition-collapse branch and push the compression ratio well below 0.6.
  const xmlLines = [`<?xml version="1.0" encoding="UTF-8"?>`];
  xmlLines.push(
    `<root xmlns="http://example.com/ns" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`,
  );
  for (let i = 0; i < 60; i++) {
    xmlLines.push(`  <item>n${i}</item>`);
  }
  xmlLines.push(`</root>`);
  const longXmlPadded = xmlLines.join("\n");
  const compressedXml = compressStructuredSemantic(longXmlPadded);
  assert.ok(compressedXml !== null, "XML compression returns a result");
  assert.ok(!compressedXml.includes("xmlns"), "namespaces stripped from XML");
  assert.ok(compressedXml.includes("[XML compressed:"), "XML annotation footer present");
  console.log("Semantic structured compression tests passed");

  // Multi-line XML: sibling runs collapse as whole elements (children never
  // leak through as orphans), marker carries an attribute summary, and an
  // error-bearing element mid-run is force-kept.
  const rss = [];
  rss.push(`<?xml version="1.0"?>`);
  rss.push(`<rss version="2.0">`);
  rss.push(`  <channel>`);
  rss.push(`    <title>Feed</title>`);
  for (let i = 1; i <= 30; i++) {
    rss.push(`    <item id="a${i}" status="ok">`);
    rss.push(`      <title>Story ${i}</title>`);
    if (i === 15) {
      rss.push(`      <desc>connection timeout while fetching upstream.</desc>`);
    } else {
      rss.push(`      <desc>Routine summary text for item ${i}.</desc>`);
    }
    rss.push(`    </item>`);
  }
  rss.push(`  </channel>`);
  rss.push(`</rss>`);
  const rssText = rss.join("\n");
  const rssCompressed = compressStructuredSemantic(rssText);
  assert.ok(rssCompressed !== null, "multi-line XML compresses");
  assert.ok(rssCompressed.includes("Story 1"), "head element kept");
  assert.ok(rssCompressed.includes("Story 30"), "tail element kept");
  assert.ok(rssCompressed.includes("connection timeout"), "error element force-kept");
  assert.ok(rssCompressed.includes("elements elided"), "elision marker present");
  assert.ok(rssCompressed.includes("all status=ok"), "marker carries invariant summary");
  assert.ok(!rssCompressed.includes("<title>Story 5</title>"), "elided children do not leak as orphans");
  assert.ok(rssCompressed.length < rssText.length, "output actually smaller");

  // XML idempotence: a second pass must not change the payload.
  const secondPass = compressStructuredSemantic(rssCompressed);
  if (secondPass !== null) {
    assert.equal(secondPass, rssCompressed, "second pass is stable");
  }

  // JSON: error-bearing array items survive the array cap.
  const results = {
    results: [
      { name: "t1", status: "passed" },
      { name: "t2", status: "passed" },
      { name: "t3", status: "passed" },
      { name: "t4", status: "failed", message: "assertion error: timeout" },
      { name: "t5", status: "passed" },
    ],
  };
  const longResults = JSON.stringify(results, null, 2) + "\n// padding ".repeat(60);
  const jsonCompressed = compressStructuredSemantic(longResults);
  if (jsonCompressed !== null) {
    assert.ok(jsonCompressed.includes("t4"), "failed test item survives the array cap");
    assert.ok(jsonCompressed.includes("error-bearing"), "stub notes error keeps");
  }

  // Digit-masked run-length collapse for repeated log spam.
  const spamLines = [];
  for (let i = 1; i <= 30; i++) {
    spamLines.push(`2026-08-21T10:00:00Z INFO GET /api/users 200 12ms req=${i} user=42`);
  }
  spamLines.push(`2026-08-21T10:00:01Z ERROR upstream refused the connection`);
  const spam = spamLines.join("\n");
  const spamCollapsed = collapseRepeatedLines(spam);
  assert.ok(spamCollapsed.includes("similar lines omitted"), "spam run collapses");
  assert.ok(spamCollapsed.includes("req=1"), "first occurrence kept verbatim");
  assert.ok(spamCollapsed.includes("upstream refused"), "distinct line kept");
  assert.ok(spamCollapsed.length < spam.length, "collapsed output smaller");
  assert.equal(collapseRepeatedLines(spamCollapsed), spamCollapsed, "collapse is idempotent");
  assert.equal(collapseRepeatedLines("short\nunique\nlines\n"), "short\nunique\nlines\n", "no-run input untouched");
  // Pure-numeric runs (seq, counters) are the content itself — never collapse.
  const numeric = Array.from({ length: 50 }, (_, k) => String(k + 1)).join("\n");
  assert.equal(collapseRepeatedLines(numeric), numeric, "pure-numeric runs stay intact");
  console.log("Compression improvement tests passed");

  // Toggle off → null even for inputs that would otherwise compress.
  setStructuredCompression(false);
  assert.equal(isStructuredCompressionEnabled(), false, "toggle flips state");
  assert.equal(compressStructuredSemantic(wrappedJson, "kubectl get pods"), null, "toggle off bypasses");
  setStructuredCompression(true);
  assert.ok(isStructuredCompressionEnabled(), "toggle restores state");
  // Suppress unused-binding warning for bigValue/bigJson — built as a compact
  // fixture reference even though the wrappedJson variant is what we assert on.
  void bigValue;
  void bigJson;
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
  await cleanupArchives(-1);
  const smallOutput = "small";
  assert.equal(await archiveIfLarge(smallOutput, "echo small"), null, "small output not archived");

  const bigOutput = "line\n".repeat(10_000);
  const archived = await archiveIfLarge(bigOutput, "seq 10000");
  assert.ok(archived, "big output archived");
  assert.ok(archived.summary.includes("lines archived"), "summary mentions archived lines");

  const expanded = await expandArchive(archived.id);
  assert.equal(expanded, bigOutput, "archive round-trip");
  await cleanupArchives(-1);
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
  const grepDir = mkdtempSync(join(tmpdir(), "cave-grep-"));
  try {
    writeFileSync(join(grepDir, "a.ts"), "export const ALPHA = 1;\nexport const beta = 2;\n", "utf-8");
    writeFileSync(join(grepDir, "b.md"), "# Title\nALPHA reference here\n", "utf-8");

    const grepMatches = await grepTool.handler({
      pattern: "ALPHA",
      path: grepDir,
      limit: 10,
    });
    assert.equal(grepMatches.isError, undefined);
    const gText = grepMatches.content[0].text;
    assert.match(gText, /a\.ts:1: export const ALPHA = 1/);
    assert.match(gText, /b\.md:2: ALPHA reference here/);

    // glob filter restricts to .ts only.
    const grepTsOnly = await grepTool.handler({
      pattern: "ALPHA",
      path: grepDir,
      glob: "*.ts",
      limit: 10,
    });
    assert.match(grepTsOnly.content[0].text, /a\.ts:1:/);
    assert.doesNotMatch(grepTsOnly.content[0].text, /b\.md/);

    // literal mode treats regex metachars as plain text.
    writeFileSync(join(grepDir, "lit.txt"), "price is $5.00 each\n", "utf-8");
    const grepLiteral = await grepTool.handler({
      pattern: "$5.00",
      path: grepDir,
      glob: "lit.txt",
      literal: true,
      limit: 5,
    });
    assert.match(grepLiteral.content[0].text, /lit\.txt:1: price is \$5\.00 each/);
    // Without literal, "$5.00" is a regex that won't match the literal dollar.
    const grepRegex = await grepTool.handler({
      pattern: "$5.00",
      path: grepDir,
      glob: "lit.txt",
      limit: 5,
    });
    assert.equal(grepRegex.content[0].text, "No matches found");

    // ignoreCase matches different casing.
    const grepCI = await grepTool.handler({
      pattern: "alpha",
      path: grepDir,
      glob: "a.ts",
      ignoreCase: true,
      limit: 5,
    });
    assert.match(grepCI.content[0].text, /a\.ts:1: export const ALPHA = 1/);

    // context lines emit `-`-prefixed neighbors.
    const grepCtx = await grepTool.handler({
      pattern: "beta",
      path: grepDir,
      glob: "a.ts",
      context: 1,
      limit: 5,
    });
    assert.match(grepCtx.content[0].text, /a\.ts:2: export const beta = 2/);
    assert.match(grepCtx.content[0].text, /a\.ts-1- export const ALPHA = 1/);

    // missing path surfaces an error.
    const grepMissing = await grepTool.handler({
      pattern: "x",
      path: join(grepDir, "nope"),
      limit: 5,
    });
    assert.equal(grepMissing.isError, true);
    assert.match(grepMissing.content[0].text, /Path not found/);

    // Per-file cap: 30 hits in one file → 10 lines + "+N more" marker.
    const manyHits = Array.from({ length: 30 }, (_, i) => `HIT_TOKEN line ${i + 1}`);
    writeFileSync(join(grepDir, "many.ts"), manyHits.join("\n") + "\n", "utf-8");
    const grepCapped = await grepTool.handler({
      pattern: "HIT_TOKEN",
      path: grepDir,
      glob: "many.ts",
      limit: 100,
    });
    const capText = grepCapped.content[0].text;
    const capHits = capText.split("\n").filter((l) => l.includes("HIT_TOKEN"));
    assert.equal(capHits.length, 10, "per-file cap keeps 10 hit lines");
    assert.match(capText, /\+20 more in many\.ts/);
  } finally {
    rmSync(grepDir, { recursive: true, force: true });
  }
  console.log("cave__grep tests passed");
  console.log();

  console.log("3b. Testing read fast-path, budgets, repetition:");
  {
    const budgets = getAllBudgets();
    assert.equal(budgets.read.maxLines, 200);
    assert.equal(budgets.read.headLines, 100);
    assert.equal(budgets.read.tailLines, 100);
    assert.equal(budgets.ls.maxLines, 60);
    assert.equal(budgets.ls.headLines, 30);
    assert.equal(budgets.ls.tailLines, 30);
    assert.equal(budgets.find.maxLines, 120);

    const fpDir = mkdtempSync(join(tmpdir(), "cave-fastpath-"));
    try {
      // Small read: skip codebook even after it learned the react import.
      const smallBody =
        "import { useState } from 'react';\nexport const FAST_PATH_UNIQUE = 'y';\n";
      const smallFile = join(fpDir, "small.ts");
      writeFileSync(smallFile, smallBody, "utf-8");
      const smallRead = await readTool.handler({ file_path: smallFile });
      assert.equal(smallRead.content[0].text, smallBody, "small-read fast-path is identity");
      assert.doesNotMatch(smallRead.content[0].text, /\[§/, "fast-path skips codebook");
      assert.equal(fitsBudget(smallBody, "read"), true);

      // Large read: applyBudget head/tail shape (limit above read maxLines).
      const largeLines = Array.from(
        { length: 400 },
        (_, i) => `ZZZ-large-read-${String(i + 1).padStart(3, "0")}`,
      );
      const largeFile = join(fpDir, "large.txt");
      writeFileSync(largeFile, largeLines.join("\n"), "utf-8");
      const largeRead = await readTool.handler({ file_path: largeFile, limit: 400 });
      const largeText = largeRead.content[0].text;
      assert.match(largeText, /^ZZZ-large-read-001\n/);
      assert.match(largeText, /ZZZ-large-read-100/);
      assert.match(largeText, /ZZZ-large-read-301/);
      assert.match(largeText, /ZZZ-large-read-400/);
      assert.doesNotMatch(largeText, /ZZZ-large-read-200/);
      assert.match(largeText, /lines truncated/);

      // Truncation hint names offset/limit + force=true (bounce safety).
      const paged = await readTool.handler({ file_path: largeFile, force: true });
      assert.match(paged.content[0].text, /offset=201/);
      assert.match(paged.content[0].text, /limit/);
      assert.match(paged.content[0].text, /force=true/);
    } finally {
      rmSync(fpDir, { recursive: true, force: true });
    }

    const line = "the same log line repeats a lot and must shrink";
    const raw = Array.from({ length: 10 }, () => line).join("\n");
    const once = collapseRepetition(raw);
    const twice = collapseRepetition(once);
    assert.equal(once, twice, "repetition collapse is idempotent");
    assert.match(once, /… \d+ identical lines elided …/);
    assert.equal((once.match(/identical lines elided/g) || []).length, 1);
    assert.equal((twice.match(/identical lines elided/g) || []).length, 1);
    const nested = [repetitionMarker(2), repetitionMarker(2), repetitionMarker(2)].join("\n");
    assert.equal(collapseRepetition(nested), nested, "marker lines never re-collapse");

    const ids = Array.from({ length: 10 }, (_, i) => `GET /x req=${i} ok payload=hello`);
    const idText = ids.join("\n");
    assert.equal(collapseRepetition(idText), idText, "digit-mask off by default");
    const masked = collapseRepetition(idText, { maskDigits: true });
    assert.match(masked, /identical lines elided/, "digit-mask opt-in collapses id runs");
    assert.equal(collapseRepetition(masked, { maskDigits: true }), masked, "masked collapse idempotent");
  }
  console.log("read fast-path / repetition tests passed");
  console.log();

  console.log("4. Testing cave__find:");
  const findDir = mkdtempSync(join(tmpdir(), "cave-find-"));
  try {
    mkdirSync(join(findDir, "sub"), { recursive: true });
    writeFileSync(join(findDir, "top.md"), "x\n", "utf-8");
    writeFileSync(join(findDir, "sub", "deep.ts"), "y\n", "utf-8");
    writeFileSync(join(findDir, "ignore.txt"), "z\n", "utf-8");

    const findMd = await findTool.handler({ pattern: "*.md", path: findDir, limit: 50 });
    assert.equal(findMd.isError, undefined);
    assert.match(findMd.content[0].text, /top\.md/);
    assert.doesNotMatch(findMd.content[0].text, /\.ts/);

    const findRecursive = await findTool.handler({ pattern: "**/*.ts", path: findDir, limit: 50 });
    assert.match(findRecursive.content[0].text, /sub\/deep\.ts/);

    // hidden=false excludes dotfiles (upstream opencode glob behavior).
    writeFileSync(join(findDir, ".secret"), "s\n", "utf-8");
    const findHidden = await findTool.handler({ pattern: "*", path: findDir, limit: 50 });
    assert.match(findHidden.content[0].text, /\.secret/);
    const findNoHidden = await findTool.handler({ pattern: "*", path: findDir, limit: 50, hidden: false });
    assert.doesNotMatch(findNoHidden.content[0].text, /\.secret/);

    // limit truncation notice.
    const findLimited = await findTool.handler({ pattern: "*", path: findDir, limit: 1 });
    assert.match(findLimited.content[0].text, /1 results limit reached/);

    // missing directory.
    const findMissing = await findTool.handler({ pattern: "*", path: join(findDir, "missing"), limit: 5 });
    assert.equal(findMissing.isError, true);
    assert.match(findMissing.content[0].text, /Path not found or not a directory/);
  } finally {
    rmSync(findDir, { recursive: true, force: true });
  }
  console.log("cave__find tests passed");
  console.log();

  console.log("4b. Testing search bins:");
  {
    const { whichUserRg, whichUserFd, binDir, ensureBins } = await import("./dist/runtime/bins.js");
    assert.ok(whichUserRg(), "rg must resolve from PATH");
    assert.ok(whichUserFd(), "fd or fdfind must resolve from PATH");
    const bins = await ensureBins();
    assert.ok(bins.rg, "ensureBins must return rg");
    assert.ok(bins.rg === whichUserRg(), "PATH rg wins over download cache");
    assert.ok(typeof binDir() === "string" && binDir().length > 0);
  }
  console.log("search bins tests passed");
  console.log();

  console.log("5. Testing cave__ls:");
  const lsDir = mkdtempSync(join(tmpdir(), "cave-ls-"));
  try {
    mkdirSync(join(lsDir, "dir"), { recursive: true });
    writeFileSync(join(lsDir, "file-a.txt"), "a\n", "utf-8");
    writeFileSync(join(lsDir, "file-b.md"), "b\n", "utf-8");
    writeFileSync(join(lsDir, ".hidden"), "h\n", "utf-8");

    const lsAll = await lsTool.handler({ path: lsDir, limit: 50 });
    assert.equal(lsAll.isError, undefined);
    const lsText = lsAll.content[0].text;
    // directories sorted first, dotfiles included.
    assert.match(lsText, /^dir\//m);
    assert.match(lsText, /\.hidden/m);
    assert.match(lsText, /file-a\.txt/m);
    assert.ok(lsText.indexOf("dir/") < lsText.indexOf("file-a.txt"), "directories list before files");

    // pagination via offset.
    const lsPage = await lsTool.handler({ path: lsDir, limit: 2, offset: 2 });
    assert.match(lsPage.content[0].text, /Directory listing truncated.*offset=4/);

    // symlink escape guard: a symlink pointing outside the dir is filtered.
    try {
      writeFileSync(join(tmpdir(), "cave-ls-escape.txt"), "outside\n", "utf-8");
      const { symlinkSync } = await import("node:fs");
      symlinkSync(join(tmpdir(), "cave-ls-escape.txt"), join(lsDir, "escape-link"), "file");
    } catch {
      // symlink creation may be unsupported on some platforms; skip if so.
    }
    const lsSymlink = await lsTool.handler({ path: lsDir, limit: 50 });
    assert.doesNotMatch(lsSymlink.content[0].text, /escape-link/, "symlink escaping the dir is filtered");

    // missing directory.
    const lsMissing = await lsTool.handler({ path: join(lsDir, "nope"), limit: 5 });
    assert.equal(lsMissing.isError, true);
    assert.match(lsMissing.content[0].text, /Path not found or not a directory/);
  } finally {
    rmSync(lsDir, { recursive: true, force: true });
    rmSync(join(tmpdir(), "cave-ls-escape.txt"), { force: true });
  }
  console.log("cave__ls tests passed");
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

  console.log("6e2. Testing cave__edit upstream match tiers:");
  const tierDir = mkdtempSync(join(tmpdir(), "cave-edit-tiers-"));
  try {
    // Tier 2: unicode-punctuation normalization (curly quotes/dash → ASCII).
    const uniFile = join(tierDir, "uni.txt");
    writeFileSync(uniFile, "it’s a “test” — done\n", "utf-8");
    const uniResult = await editTool.handler({
      file_path: uniFile,
      old_string: "it's a \"test\" - done",
      new_string: "it's a \"test\" - changed",
    });
    assert.equal(uniResult.isError, undefined, `unicode tier should match: ${uniResult.content[0].text}`);
    assert.ok(readFileSync(uniFile, "utf-8").includes("changed"));

    // Tier 3: trailing-whitespace-tolerant line window.
    const trailFile = join(tierDir, "trail.txt");
    writeFileSync(trailFile, "alpha   \nbeta\t\n", "utf-8");
    const trailResult = await editTool.handler({
      file_path: trailFile,
      old_string: "alpha\nbeta",
      new_string: "alpha\nGAMMA",
    });
    assert.equal(trailResult.isError, undefined, `trimEnd tier should match: ${trailResult.content[0].text}`);
    assert.ok(readFileSync(trailFile, "utf-8").includes("GAMMA"));

    // CAVE_TOOLS_FUZZY_EDIT=0 disables the fuzzy fallback (strict upstream parity).
    const fuzzyOffFile = join(tierDir, "fuzzyoff.txt");
    writeFileSync(fuzzyOffFile, "const   x   =   1;\n", "utf-8");
    process.env.CAVE_TOOLS_FUZZY_EDIT = "0";
    try {
      const offResult = await editTool.handler({
        file_path: fuzzyOffFile,
        old_string: "const x = 1;",
        new_string: "const x = 2;",
      });
      assert.equal(offResult.isError, true);
      assert.match(offResult.content[0].text, /Could not find oldString/);
    } finally {
      delete process.env.CAVE_TOOLS_FUZZY_EDIT;
    }
  } finally {
    rmSync(tierDir, { recursive: true, force: true });
  }
  console.log("cave__edit upstream tier tests passed");
  console.log();

  console.log("6f. Testing cave__edit line-range mode:");
  const rangeDir = mkdtempSync(join(tmpdir(), "cave-edit-range-"));
  try {
    const { applyRangeEdit } = await import("./dist/tools/edit.js");
    const { getFileHash } = await import("./dist/compression/utils.js");

    // Pure helper
    const h1 = applyRangeEdit("a\nb\nc\n", 2, 2, "B");
    assert.equal(h1.ok, true);
    assert.equal(h1.content, "a\nB\nc\n");

    const hIns = applyRangeEdit("a\nb\n", 2, 1, "x");
    assert.equal(hIns.ok, true);
    assert.equal(hIns.content, "a\nx\nb\n");

    const hDel = applyRangeEdit("a\nb\nc\n", 2, 3, "");
    assert.equal(hDel.ok, true);
    assert.equal(hDel.content, "a\n");

    const hBad = applyRangeEdit("a\nb\n", 5, 5, "z");
    assert.equal(hBad.ok, false);

    // CRLF
    const hCrlf = applyRangeEdit("a\r\nb\r\nc\r\n", 2, 2, "B");
    assert.equal(hCrlf.ok, true);
    assert.equal(hCrlf.content, "a\r\nB\r\nc\r\n");

    // Handler: middle replace
    const midFile = join(rangeDir, "mid.txt");
    writeFileSync(midFile, "one\ntwo\nthree\nfour\n", "utf-8");
    const mid = await editTool.handler({
      file_path: midFile,
      start_line: 2,
      end_line: 3,
      content: "TWO\nTHREE",
    });
    assert.equal(mid.isError, undefined, mid.content[0].text);
    assert.equal(readFileSync(midFile, "utf-8"), "one\nTWO\nTHREE\nfour\n");

    // Insert before line 2
    const insFile = join(rangeDir, "ins.txt");
    writeFileSync(insFile, "a\nb\n", "utf-8");
    const ins = await editTool.handler({
      file_path: insFile,
      start_line: 2,
      end_line: 1,
      content: "x",
    });
    assert.equal(ins.isError, undefined, ins.content[0].text);
    assert.equal(readFileSync(insFile, "utf-8"), "a\nx\nb\n");

    // Insert before trailing empty line of "a\nb\n" → lines [a,b,""]
    const appFile = join(rangeDir, "app.txt");
    writeFileSync(appFile, "a\nb\n", "utf-8");
    const app = await editTool.handler({
      file_path: appFile,
      start_line: 3,
      end_line: 2,
      content: "c",
    });
    assert.equal(app.isError, undefined, app.content[0].text);
    assert.equal(readFileSync(appFile, "utf-8"), "a\nb\nc\n");

    // Bounds fail
    const bound = await editTool.handler({
      file_path: midFile,
      start_line: 99,
      end_line: 99,
      content: "x",
    });
    assert.equal(bound.isError, true);
    assert.match(bound.content[0].text, /out of range/);

    // Mutual exclusion
    const both = await editTool.handler({
      file_path: midFile,
      old_string: "one",
      new_string: "ONE",
      start_line: 1,
      end_line: 1,
      content: "ONE",
    });
    assert.equal(both.isError, true);
    assert.match(both.content[0].text, /either/);

    // expected_hash mismatch
    const hashFile = join(rangeDir, "hash.txt");
    writeFileSync(hashFile, "hello\n", "utf-8");
    const badHash = await editTool.handler({
      file_path: hashFile,
      start_line: 1,
      end_line: 1,
      content: "bye",
      expected_hash: "deadbeefdeadbeef",
    });
    assert.equal(badHash.isError, true);
    assert.match(badHash.content[0].text, /expected_hash mismatch/);

    // expected_hash match (prefix)
    const realHash = await getFileHash(hashFile);
    assert.ok(realHash);
    const goodHash = await editTool.handler({
      file_path: hashFile,
      start_line: 1,
      end_line: 1,
      content: "bye",
      expected_hash: realHash.slice(0, 12),
    });
    assert.equal(goodHash.isError, undefined, goodHash.content[0].text);
    assert.equal(readFileSync(hashFile, "utf-8"), "bye\n");

    // Batch ranges high→low
    const batchFile = join(rangeDir, "batch.txt");
    writeFileSync(batchFile, "1\n2\n3\n4\n5\n", "utf-8");
    const batch = await editTool.handler({
      file_path: batchFile,
      edits: [
        { start_line: 1, end_line: 1, content: "A" },
        { start_line: 4, end_line: 5, content: "D\nE" },
      ],
    });
    assert.equal(batch.isError, undefined, batch.content[0].text);
    assert.equal(readFileSync(batchFile, "utf-8"), "A\n2\n3\nD\nE\n");

    // Read footer with line_numbers
    const metaFile = join(rangeDir, "meta.txt");
    writeFileSync(metaFile, "alpha\nbeta\n", "utf-8");
    const metaRead = await readTool.handler({
      file_path: metaFile,
      line_numbers: true,
      force: true,
    });
    assert.equal(metaRead.isError, undefined);
    assert.match(metaRead.content[0].text, /00001:[0-9a-f]{2}\| alpha/);
    assert.match(metaRead.content[0].text, /--- cave_edit_meta ---/);
    assert.match(metaRead.content[0].text, /file_hash: [0-9a-f]{16}/);
    assert.match(metaRead.content[0].text, /range_checksum: \d+-\d+:[0-9a-f]{8}/);

    // v2: range checksum match/mismatch + move + delete flag + multi-file
    const { rangeChecksum, applyMoveLines, lineTag } = await import("./dist/tools/line-range.js");
    assert.equal(lineTag("alpha").length, 2);
    const lines = ["a", "b", "c"];
    const cs = rangeChecksum(lines, 1, 2);
    assert.equal(cs.length, 8);

    const csFile = join(rangeDir, "cs.txt");
    writeFileSync(csFile, "a\nb\nc\n", "utf-8");
    const csVal = rangeChecksum(["a", "b", "c", ""], 2, 2);
    const csOk = await editTool.handler({
      file_path: csFile,
      start_line: 2,
      end_line: 2,
      content: "B",
      expected_range_checksum: csVal,
    });
    assert.equal(csOk.isError, undefined, csOk.content[0].text);
    assert.equal(readFileSync(csFile, "utf-8"), "a\nB\nc\n");

    writeFileSync(csFile, "a\nb\nc\n", "utf-8");
    const csBad = await editTool.handler({
      file_path: csFile,
      start_line: 2,
      end_line: 2,
      content: "B",
      expected_range_checksum: "ffffffff",
    });
    assert.equal(csBad.isError, true);
    assert.match(csBad.content[0].text, /expected_range_checksum mismatch/);
    assert.match(csBad.content[0].text, /window:/);

    // delete:true
    const delFile = join(rangeDir, "del.txt");
    writeFileSync(delFile, "1\n2\n3\n", "utf-8");
    const del = await editTool.handler({
      file_path: delFile,
      start_line: 2,
      end_line: 2,
      delete: true,
    });
    assert.equal(del.isError, undefined, del.content[0].text);
    assert.equal(readFileSync(delFile, "utf-8"), "1\n3\n");

    // move
    const mv = applyMoveLines("a\nb\nc\nd\n", 2, 3, 5);
    assert.equal(mv.ok, true);
    assert.equal(mv.content, "a\nd\nb\nc\n");
    const mvFile = join(rangeDir, "mv.txt");
    writeFileSync(mvFile, "a\nb\nc\nd\n", "utf-8");
    const mvEdit = await editTool.handler({
      file_path: mvFile,
      start_line: 2,
      end_line: 3,
      insert_before: 5,
    });
    assert.equal(mvEdit.isError, undefined, mvEdit.content[0].text);
    assert.equal(readFileSync(mvFile, "utf-8"), "a\nd\nb\nc\n");

    // multi-file batch
    const mf1 = join(rangeDir, "mf1.txt");
    const mf2 = join(rangeDir, "mf2.txt");
    writeFileSync(mf1, "x\n", "utf-8");
    writeFileSync(mf2, "y\n", "utf-8");
    const multi = await editTool.handler({
      edits: [
        { file_path: mf1, start_line: 1, end_line: 1, content: "X" },
        { file_path: mf2, old_string: "y", new_string: "Y" },
      ],
    });
    assert.equal(multi.isError, undefined, multi.content[0].text);
    assert.equal(readFileSync(mf1, "utf-8"), "X\n");
    assert.equal(readFileSync(mf2, "utf-8"), "Y\n");

    // strict requires proof
    const prevMode = process.env.CAVE_TOOLS_MODE;
    process.env.CAVE_TOOLS_MODE = "strict";
    try {
      writeFileSync(csFile, "a\nb\n", "utf-8");
      const strictFail = await editTool.handler({
        file_path: csFile,
        start_line: 1,
        end_line: 1,
        content: "A",
      });
      assert.equal(strictFail.isError, true);
      assert.match(strictFail.content[0].text, /STRICT/);
      const strictOk = await editTool.handler({
        file_path: csFile,
        start_line: 1,
        end_line: 1,
        content: "A",
        expected_range_checksum: rangeChecksum(["a", "b", ""], 1, 1),
      });
      assert.equal(strictOk.isError, undefined, strictOk.content[0].text);
    } finally {
      if (prevMode === undefined) delete process.env.CAVE_TOOLS_MODE;
      else process.env.CAVE_TOOLS_MODE = prevMode;
    }
  } finally {
    rmSync(rangeDir, { recursive: true, force: true });
  }
  console.log("cave__edit line-range tests passed");
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
    assert.match(read2.content[0].text, /unchanged since read #\d+/);

    const read3 = await readTool.handler({ file_path: forceFile, force: true });
    assert.equal(read3.isError, undefined);
    assert.ok(read3.content[0].text.includes("initial content"), "force should bypass dedup");
  } finally {
    rmSync(readForceDir, { recursive: true, force: true });
  }
  console.log("cave__read force parameter tests passed");
  console.log();

  console.log("6c2. Testing cave__read per-session dedup (multi-session isolation):");
  const readSessionDir = mkdtempSync(join(tmpdir(), "cave-read-session-"));
  try {
    const sharedFile = join(readSessionDir, "shared.txt");
    writeFileSync(sharedFile, "shared content\n", "utf-8");

    // Session A first read → delivers content.
    const aRead1 = await readTool.handler({ file_path: sharedFile, __sessionId: "sessionA" });
    assert.equal(aRead1.isError, undefined);
    assert.ok(aRead1.content[0].text.includes("shared content"), "session A first read delivers content");

    // Session A re-read (same session, unchanged) → stub (dedup within session).
    const aRead2 = await readTool.handler({ file_path: sharedFile, __sessionId: "sessionA" });
    assert.equal(aRead2.isError, undefined);
    assert.match(aRead2.content[0].text, /unchanged since read #\d+/, "session A re-read dedups");

    // Session B first read (different session, same file) → MUST deliver fresh
    // content, NOT a stub. This is the multi-session fix: subagents sharing the
    // MCP process no longer inherit the parent's "unchanged" stub.
    const bRead1 = await readTool.handler({ file_path: sharedFile, __sessionId: "sessionB" });
    assert.equal(bRead1.isError, undefined);
    assert.ok(bRead1.content[0].text.includes("shared content"), "session B gets fresh content, not parent's stub");

    // Session B re-read → stub (its own dedup now active).
    const bRead2 = await readTool.handler({ file_path: sharedFile, __sessionId: "sessionB" });
    assert.match(bRead2.content[0].text, /unchanged since read #\d+/, "session B re-read dedups");

    // No __sessionId (legacy client) → falls back to default session, still dedups.
    const legacyRead1 = await readTool.handler({ file_path: sharedFile });
    assert.ok(legacyRead1.content[0].text.includes("shared content"), "legacy client gets content on first read");
  } finally {
    rmSync(readSessionDir, { recursive: true, force: true });
  }
  console.log("cave__read per-session dedup tests passed");
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
    assert.match(patchAdd.content[0].text, /Success\. Updated the following files:/);
    assert.match(patchAdd.content[0].text, new RegExp(`A .*added\\.txt`));

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

    // add/update/delete in one patch, plus nested-dir add.
    const combinedDir = mkdtempSync(join(patchDir, "combined-"));
    const nested = join(combinedDir, "nested", "new.txt");
    const modify = join(combinedDir, "modify.txt");
    const del = join(combinedDir, "delete.txt");
    writeFileSync(modify, "line1\nline2\n");
    writeFileSync(del, "obsolete\n");
    const combined = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${nested}\n+created\n*** Delete File: ${del}\n*** Update File: ${modify}\n@@\n-line2\n+changed\n*** End Patch`,
    });
    assert.equal(combined.isError, undefined, combined.content[0].text);
    assert.equal(readFileSync(nested, "utf-8"), "created\n");
    assert.equal(readFileSync(modify, "utf-8"), "line1\nchanged\n");
    assert.throws(() => readFileSync(del, "utf-8"));
    assert.match(combined.content[0].text, /A .*nested\/new\.txt/);
    assert.match(combined.content[0].text, /D .*delete\.txt/);
    assert.match(combined.content[0].text, /M .*modify\.txt/);

    // multiple hunks in one file.
    const multi = join(combinedDir, "multi.txt");
    writeFileSync(multi, "line1\nline2\nline3\nline4\n");
    const multiPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${multi}\n@@\n-line2\n+changed2\n@@\n-line4\n+changed4\n*** End Patch`,
    });
    assert.equal(multiPatch.isError, undefined, multiPatch.content[0].text);
    assert.equal(readFileSync(multi, "utf-8"), "line1\nchanged2\nline3\nchanged4\n");

    // insert-only hunk.
    const insertOnly = join(combinedDir, "insert_only.txt");
    writeFileSync(insertOnly, "alpha\nomega\n");
    const insertPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${insertOnly}\n@@\n alpha\n+beta\n omega\n*** End Patch`,
    });
    assert.equal(insertPatch.isError, undefined, insertPatch.content[0].text);
    assert.equal(readFileSync(insertOnly, "utf-8"), "alpha\nbeta\nomega\n");

    // appends trailing newline on update.
    const noNewline = join(combinedDir, "no_newline.txt");
    writeFileSync(noNewline, "no newline at end");
    const appendPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${noNewline}\n@@\n-no newline at end\n+first line\n+second line\n*** End Patch`,
    });
    assert.equal(appendPatch.isError, undefined, appendPatch.content[0].text);
    const appended = readFileSync(noNewline, "utf-8");
    assert.ok(appended.endsWith("\n"));
    assert.equal(appended, "first line\nsecond line\n");

    // move to nested dir.
    const moveDir = mkdtempSync(join(patchDir, "move-"));
    const moveSrc = join(moveDir, "old", "name.txt");
    mkdirSync(join(moveDir, "old"), { recursive: true });
    writeFileSync(moveSrc, "old content\n");
    const movePatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${moveSrc}\n*** Move to: ${join(moveDir, "renamed", "dir", "name.txt")}\n@@\n-old content\n+new content\n*** End Patch`,
    });
    assert.equal(movePatch.isError, undefined, movePatch.content[0].text);
    assert.throws(() => readFileSync(moveSrc, "utf-8"));
    assert.equal(readFileSync(join(moveDir, "renamed", "dir", "name.txt"), "utf-8"), "new content\n");
    assert.match(movePatch.content[0].text, /M .*renamed.*name\.txt|M .*name\.txt/);

    // move overwrites existing destination.
    const moveOverwriteSrc = join(moveDir, "old2", "name.txt");
    const moveOverwriteDst = join(moveDir, "renamed2", "dir", "name.txt");
    mkdirSync(join(moveDir, "old2"), { recursive: true });
    mkdirSync(join(moveDir, "renamed2", "dir"), { recursive: true });
    writeFileSync(moveOverwriteSrc, "from\n");
    writeFileSync(moveOverwriteDst, "existing\n");
    const moveOverwritePatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${moveOverwriteSrc}\n*** Move to: ${moveOverwriteDst}\n@@\n-from\n+new\n*** End Patch`,
    });
    assert.equal(moveOverwritePatch.isError, undefined, moveOverwritePatch.content[0].text);
    assert.throws(() => readFileSync(moveOverwriteSrc, "utf-8"));
    assert.equal(readFileSync(moveOverwriteDst, "utf-8"), "new\n");

    // add overwrites existing file (opencode parity).
    const dupFile = join(combinedDir, "duplicate.txt");
    writeFileSync(dupFile, "old content\n");
    const dupPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${dupFile}\n+new content\n*** End Patch`,
    });
    assert.equal(dupPatch.isError, undefined, dupPatch.content[0].text);
    assert.equal(readFileSync(dupFile, "utf-8"), "new content\n");

    // update missing target rejects with verification message.
    const missingUpdate = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${join(combinedDir, "missing.txt")}\n@@\n-nope\n+better\n*** End Patch`,
    });
    assert.equal(missingUpdate.isError, true);
    assert.match(missingUpdate.content[0].text, /apply_patch verification failed: Failed to read file to update/);

    // delete missing target rejects.
    const missingDelete = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Delete File: ${join(combinedDir, "missing.txt")}\n*** End Patch`,
    });
    assert.equal(missingDelete.isError, true);
    assert.match(missingDelete.content[0].text, /apply_patch verification failed: Failed to read file for deletion/);

    // invalid hunk header rejects.
    const invalidHdr = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Frobnicate File: foo\n*** End Patch`,
    });
    assert.equal(invalidHdr.isError, true);
    assert.match(invalidHdr.content[0].text, /apply_patch verification failed/);

    // empty patch rejects.
    const emptyPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** End Patch`,
    });
    assert.equal(emptyPatch.isError, true);
    assert.match(emptyPatch.content[0].text, /patch rejected: empty patch/);

    // verification failure leaves no side effects (atomicity).
    const atomicDir = mkdtempSync(join(patchDir, "atomic-"));
    const atomicPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${join(atomicDir, "created.txt")}\n+hello\n*** Update File: ${join(atomicDir, "missing.txt")}\n@@\n-old\n+new\n*** End Patch`,
    });
    assert.equal(atomicPatch.isError, true);
    assert.throws(() => readFileSync(join(atomicDir, "created.txt"), "utf-8"));

    // missing context rejects, target unchanged.
    const ctxDir = mkdtempSync(join(patchDir, "ctx-"));
    const ctxFile = join(ctxDir, "modify.txt");
    writeFileSync(ctxFile, "line1\nline2\n");
    const ctxPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${ctxFile}\n@@\n-missing\n+changed\n*** End Patch`,
    });
    assert.equal(ctxPatch.isError, true);
    assert.match(ctxPatch.content[0].text, /apply_patch verification failed/);
    assert.equal(readFileSync(ctxFile, "utf-8"), "line1\nline2\n");

    // end-of-file anchor.
    const eofFile = join(ctxDir, "tail.txt");
    writeFileSync(eofFile, "alpha\nlast\n");
    const eofPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${eofFile}\n@@\n-last\n+end\n*** End of File\n*** End Patch`,
    });
    assert.equal(eofPatch.isError, undefined, eofPatch.content[0].text);
    assert.equal(readFileSync(eofFile, "utf-8"), "alpha\nend\n");

    // @@ context disambiguation.
    const ctxAmbFile = join(ctxDir, "multi_ctx.txt");
    writeFileSync(ctxAmbFile, "fn a\nx=10\ny=2\nfn b\nx=10\ny=20\n");
    const ctxAmbPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${ctxAmbFile}\n@@ fn b\n-x=10\n+x=11\n*** End Patch`,
    });
    assert.equal(ctxAmbPatch.isError, undefined, ctxAmbPatch.content[0].text);
    assert.equal(readFileSync(ctxAmbFile, "utf-8"), "fn a\nx=10\ny=2\nfn b\nx=11\ny=20\n");

    // trailing-whitespace match.
    const wsFile = join(ctxDir, "trailing_ws.txt");
    writeFileSync(wsFile, "line1  \nline2\nline3   \n");
    const wsPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${wsFile}\n@@\n-line2\n+changed\n*** End Patch`,
    });
    assert.equal(wsPatch.isError, undefined, wsPatch.content[0].text);
    assert.equal(readFileSync(wsFile, "utf-8"), "line1  \nchanged\nline3   \n");

    // leading-whitespace match.
    const leadWsFile = join(ctxDir, "leading_ws.txt");
    writeFileSync(leadWsFile, "  line1\nline2\n  line3\n");
    const leadWsPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${leadWsFile}\n@@\n-line2\n+changed\n*** End Patch`,
    });
    assert.equal(leadWsPatch.isError, undefined, leadWsPatch.content[0].text);
    assert.equal(readFileSync(leadWsFile, "utf-8"), "  line1\nchanged\n  line3\n");

    // Unicode punctuation match.
    const uniFile = join(ctxDir, "unicode.txt");
    writeFileSync(uniFile, `He said \u201Chello\u201D\nsome\u2014dash\nend\n`);
    const uniPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${uniFile}\n@@\n-He said "hello"\n+He said "hi"\n*** End Patch`,
    });
    assert.equal(uniPatch.isError, undefined, uniPatch.content[0].text);
    assert.equal(readFileSync(uniFile, "utf-8"), `He said "hi"\nsome\u2014dash\nend\n`);

    // BOM preservation on update.
    const bomFile = join(ctxDir, "bom.cs");
    writeFileSync(bomFile, "\uFEFFusing System;\n\nclass Test {}\n");
    const bomPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${bomFile}\n@@\n class Test {}\n+class Next {}\n*** End Patch`,
    });
    assert.equal(bomPatch.isError, undefined, bomPatch.content[0].text);
    const bomResult = readFileSync(bomFile, "utf-8");
    assert.equal(bomResult.charCodeAt(0), 0xfeff);
    assert.equal(bomResult.slice(1), "using System;\n\nclass Test {}\nclass Next {}\n");

    // BOM preservation on move.
    const bomMoveSrc = join(ctxDir, "bom_move_src.cs");
    const bomMoveDst = join(ctxDir, "bom_move_dst.cs");
    writeFileSync(bomMoveSrc, "\uFEFFhello\n");
    const bomMovePatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${bomMoveSrc}\n*** Move to: ${bomMoveDst}\n@@\n-hello\n+world\n*** End Patch`,
    });
    assert.equal(bomMovePatch.isError, undefined, bomMovePatch.content[0].text);
    assert.throws(() => readFileSync(bomMoveSrc, "utf-8"));
    const movedBom = readFileSync(bomMoveDst, "utf-8");
    assert.equal(movedBom.charCodeAt(0), 0xfeff);
    assert.equal(movedBom.slice(1), "world\n");

    // Cave extension: non-@@ update shape still works.
    const legacyFile = join(ctxDir, "legacy.txt");
    writeFileSync(legacyFile, "alpha\nbeta\n");
    const legacyPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${legacyFile}\n-alpha\n+ALPHA\n beta\n*** End Patch`,
    });
    assert.equal(legacyPatch.isError, undefined, legacyPatch.content[0].text);
    assert.equal(readFileSync(legacyFile, "utf-8"), "ALPHA\nbeta\n");

    // Delete of a directory is rejected before writes; earlier add must not
    // be applied.
    const dirDeleteDir = mkdtempSync(join(patchDir, "dir-delete-"));
    const dirToDelete = join(dirDeleteDir, "dir");
    mkdirSync(dirToDelete, { recursive: true });
    const dirDeletePatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${join(dirDeleteDir, "new.txt")}\n+before\n*** Delete File: ${dirToDelete}\n*** End Patch`,
    });
    assert.equal(dirDeletePatch.isError, true);
    assert.match(dirDeletePatch.content[0].text, /Cannot delete directory with Delete File/);
    // Add hunk must NOT have been applied (verification phase rejected the patch).
    assert.throws(() => readFileSync(join(dirDeleteDir, "new.txt"), "utf-8"));

    // Duplicate *** Update File: sections for the same path coalesce — final
    // state combines both hunks instead of failing writeIfUnchanged mid-apply.
    const dupUpdatesDir = mkdtempSync(join(patchDir, "dup-updates-"));
    const dupUpdatesFile = join(dupUpdatesDir, "dup.txt");
    writeFileSync(dupUpdatesFile, "a\nb\nc\n");
    const dupUpdatePatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${dupUpdatesFile}\n@@\n-a\n+A\n*** Update File: ${dupUpdatesFile}\n@@\n-c\n+C\n*** End Patch`,
    });
    assert.equal(dupUpdatePatch.isError, undefined, dupUpdatePatch.content[0].text);
    assert.equal(readFileSync(dupUpdatesFile, "utf-8"), "A\nb\nC\n");

    // Move to same canonical path as source is rejected.
    const selfMoveDir = mkdtempSync(join(patchDir, "self-move-"));
    const selfMoveSrc = join(selfMoveDir, "self.txt");
    writeFileSync(selfMoveSrc, "stay\n");
    const selfMovePatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${selfMoveSrc}\n*** Move to: ${selfMoveSrc}\n@@\n-stay\n+changed\n*** End Patch`,
    });
    assert.equal(selfMovePatch.isError, true);
    assert.match(selfMovePatch.content[0].text, /Move source and destination resolve to same path/);
    assert.equal(readFileSync(selfMoveSrc, "utf-8"), "stay\n");

    // Heredoc-wrapped patch text is unwrapped before parsing.
    const heredocDir = mkdtempSync(join(patchDir, "heredoc-"));
    const heredocFile = join(heredocDir, "heredoc_test.txt");
    const heredocPatch = await applyPatchTool.handler({
      patchText: `cat <<'EOF'\n*** Begin Patch\n*** Add File: ${heredocFile}\n+heredoc content\n*** End Patch\nEOF`,
    });
    assert.equal(heredocPatch.isError, undefined, heredocPatch.content[0].text);
    assert.equal(readFileSync(heredocFile, "utf-8"), "heredoc content\n");

    const heredocNoCat = join(heredocDir, "heredoc_no_cat.txt");
    const heredocPatch2 = await applyPatchTool.handler({
      patchText: `<<EOF\n*** Begin Patch\n*** Add File: ${heredocNoCat}\n+no cat prefix\n*** End Patch\nEOF`,
    });
    assert.equal(heredocPatch2.isError, undefined, heredocPatch2.content[0].text);
    assert.equal(readFileSync(heredocNoCat, "utf-8"), "no cat prefix\n");

    // NBSP normalization: file has U+00A0, patch has normal space — should match.
    const nbspDir = mkdtempSync(join(patchDir, "nbsp-"));
    const nbspFile = join(nbspDir, "nbsp.txt");
    writeFileSync(nbspFile, "line1\u00A0end\nline2\n");
    const nbspPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${nbspFile}\n@@\n-line1 end\n+line1 changed\n*** End Patch`,
    });
    assert.equal(nbspPatch.isError, undefined, nbspPatch.content[0].text);
    assert.equal(readFileSync(nbspFile, "utf-8"), "line1 changed\nline2\n");

    // Move + Add to same destination rejected; both writes do not occur.
    const moveAddDir = mkdtempSync(join(patchDir, "move-add-"));
    const moveAddSrc = join(moveAddDir, "src.txt");
    const moveAddDst = join(moveAddDir, "dst.txt");
    writeFileSync(moveAddSrc, "src\n");
    const moveAddPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${moveAddSrc}\n*** Move to: ${moveAddDst}\n@@\n-src\n+from-src\n*** Add File: ${moveAddDst}\n+from-add\n*** End Patch`,
    });
    assert.equal(moveAddPatch.isError, true);
    assert.match(moveAddPatch.content[0].text, /already added|already a move destination|move destination is also added/);
    // Source and destination both untouched.
    assert.equal(readFileSync(moveAddSrc, "utf-8"), "src\n");
    assert.throws(() => readFileSync(moveAddDst, "utf-8"));

    // Move + Update destination rejected.
    const moveUpdDir = mkdtempSync(join(patchDir, "move-upd-"));
    const moveUpdSrc = join(moveUpdDir, "src.txt");
    const moveUpdDst = join(moveUpdDir, "dst.txt");
    writeFileSync(moveUpdSrc, "src\n");
    writeFileSync(moveUpdDst, "existing\n");
    const moveUpdPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${moveUpdSrc}\n*** Move to: ${moveUpdDst}\n@@\n-src\n+from-src\n*** Update File: ${moveUpdDst}\n@@\n-existing\n+changed\n*** End Patch`,
    });
    assert.equal(moveUpdPatch.isError, true);
    assert.match(moveUpdPatch.content[0].text, /move destination is also updated|updates a path that is also a move destination/);

    // Two moves to same destination rejected.
    const moveTwoDir = mkdtempSync(join(patchDir, "move-two-"));
    const moveTwoA = join(moveTwoDir, "a.txt");
    const moveTwoB = join(moveTwoDir, "b.txt");
    const moveTwoDst = join(moveTwoDir, "shared.txt");
    writeFileSync(moveTwoA, "A\n");
    writeFileSync(moveTwoB, "B\n");
    const moveTwoPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${moveTwoA}\n*** Move to: ${moveTwoDst}\n@@\n-A\n+from-A\n*** Update File: ${moveTwoB}\n*** Move to: ${moveTwoDst}\n@@\n-B\n+from-B\n*** End Patch`,
    });
    assert.equal(moveTwoPatch.isError, true);
    assert.match(moveTwoPatch.content[0].text, /moves multiple sources to same destination/);

    // Move destination then delete same destination rejected.
    const moveDelDir = mkdtempSync(join(patchDir, "move-del-"));
    const moveDelSrc = join(moveDelDir, "src.txt");
    const moveDelDst = join(moveDelDir, "dst.txt");
    writeFileSync(moveDelSrc, "src\n");
    writeFileSync(moveDelDst, "dst\n");
    const moveDelPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${moveDelSrc}\n*** Move to: ${moveDelDst}\n@@\n-src\n+from-src\n*** Delete File: ${moveDelDst}\n*** End Patch`,
    });
    assert.equal(moveDelPatch.isError, true);
    assert.match(moveDelPatch.content[0].text, /deletes a path that is also a move destination/);
    assert.equal(readFileSync(moveDelSrc, "utf-8"), "src\n");
    assert.equal(readFileSync(moveDelDst, "utf-8"), "dst\n");

    // Move swap rejected before writes.
    const swapDir = mkdtempSync(join(patchDir, "move-swap-"));
    const swapA = join(swapDir, "a.txt");
    const swapB = join(swapDir, "b.txt");
    writeFileSync(swapA, "A\n");
    writeFileSync(swapB, "B\n");
    const swapPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Update File: ${swapA}\n*** Move to: ${swapB}\n@@\n-A\n+from-A\n*** Update File: ${swapB}\n*** Move to: ${swapA}\n@@\n-B\n+from-B\n*** End Patch`,
    });
    assert.equal(swapPatch.isError, true);
    assert.match(swapPatch.content[0].text, /move destination|move source/);
    assert.equal(readFileSync(swapA, "utf-8"), "A\n");
    assert.equal(readFileSync(swapB, "utf-8"), "B\n");

    // Add over directory rejected before earlier add writes.
    const addDirDir = mkdtempSync(join(patchDir, "add-dir-"));
    const addDirTarget = join(addDirDir, "dir-target");
    mkdirSync(addDirTarget, { recursive: true });
    const addDirPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${join(addDirDir, "created.txt")}\n+created\n*** Add File: ${addDirTarget}\n+bad\n*** End Patch`,
    });
    assert.equal(addDirPatch.isError, true);
    assert.match(addDirPatch.content[0].text, /Cannot write file over directory/);
    assert.throws(() => readFileSync(join(addDirDir, "created.txt"), "utf-8"));

    // Move over directory rejected before earlier add writes.
    const moveDirTargetRoot = mkdtempSync(join(patchDir, "move-dir-target-"));
    const moveDirSource = join(moveDirTargetRoot, "src.txt");
    const moveDirTarget = join(moveDirTargetRoot, "dir-target");
    mkdirSync(moveDirTarget, { recursive: true });
    writeFileSync(moveDirSource, "src\n");
    const moveDirPatch = await applyPatchTool.handler({
      patchText: `*** Begin Patch\n*** Add File: ${join(moveDirTargetRoot, "created.txt")}\n+created\n*** Update File: ${moveDirSource}\n*** Move to: ${moveDirTarget}\n@@\n-src\n+from-src\n*** End Patch`,
    });
    assert.equal(moveDirPatch.isError, true);
    assert.match(moveDirPatch.content[0].text, /Cannot write file over directory/);
    assert.throws(() => readFileSync(join(moveDirTargetRoot, "created.txt"), "utf-8"));
    assert.equal(readFileSync(moveDirSource, "utf-8"), "src\n");
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
  const statusText = statusResult.content[0].text;
  assert.equal(statusResult.isError, undefined);
  assert.match(statusText, /=== Cave Tools Status ===/);
  assert.match(statusText, /RTK Available:/);
  assert.match(statusText, /Cache Stats:/);
  assert.match(statusText, /Output trimming:/);
  assert.match(statusText, /Budget Configuration:/);
  assert.match(statusText, /bash: max=/);
  console.log("cave__status tests passed");
  console.log();

  // Test configure
  console.log("9. Testing cave__configure:");
  {
    // set_budget applies and is observable via getAllBudgets + applyBudget.
    const setRes = await configureTool.handler({
      action: "set_budget",
      tool_name: "grep",
      max_lines: 5,
      head_lines: 2,
      tail_lines: 2,
    });
    assert.equal(setRes.isError, undefined);
    assert.match(setRes.content[0].text, /Set budget for grep: max=5, head=2, tail=2/);
    assert.equal(getAllBudgets().grep.maxLines, 5);
    const trimmed = applyBudget("a\nb\nc\nd\ne\nf\ng\n", "grep");
    assert.ok(trimmed.includes("lines truncated"), "budget tightening truncates long output");

    // invalid tool name still sets an entry (configure is permissive); verify
    // it does not throw and reports the name back.
    const badTool = await configureTool.handler({
      action: "set_budget",
      tool_name: "no-such-tool",
      max_lines: 10,
    });
    assert.equal(badTool.isError, undefined);
    assert.match(badTool.content[0].text, /no-such-tool/);
    // set_budget without max_chars keeps the tool's char cap.
    assert.equal(getAllBudgets().grep.maxChars, 6000);
    // restore the default budget for grep so later tests aren't affected.
    await configureTool.handler({
      action: "set_budget",
      tool_name: "grep",
      max_lines: 60,
      head_lines: 30,
      tail_lines: 30,
    });

    // reset_cache clears the dedup cache.
    const cacheRes = await configureTool.handler({ action: "reset_cache" });
    assert.equal(cacheRes.isError, undefined);
    assert.match(cacheRes.content[0].text, /Cache reset/);

    // reset_stats zeroes counters.
    const statsRes = await configureTool.handler({ action: "reset_stats" });
    assert.equal(statsRes.isError, undefined);
    assert.match(statsRes.content[0].text, /Statistics reset/);

    // unknown action returns an error-text result.
    const unknownRes = await configureTool.handler({ action: "frobnicate" });
    assert.equal(unknownRes.isError, true);
    assert.match(unknownRes.content[0].text, /Unknown action: frobnicate/);
  }
  console.log("cave__configure tests passed");
  console.log();

  // Test bash edge cases
  console.log("10. Testing cave__bash edge cases:");
  {
    // negative timeout rejected.
    const negTimeout = await bashTool.handler({
      command: "echo hi",
      description: "negative timeout",
      timeout: -1,
    });
    assert.equal(negTimeout.isError, true);
    assert.match(negTimeout.content[0].text, /Invalid timeout value: -1/);

    // oversize timeout rejected.
    const bigTimeout = await bashTool.handler({
      command: "echo hi",
      description: "oversize timeout",
      timeout: 10 * 60 * 1000 + 1,
    });
    assert.equal(bigTimeout.isError, true);
    assert.match(bigTimeout.content[0].text, /must be <= 600000ms/);

    // timeout 0 disables the kill timer (matches opencode shell).
    const noTimeout = await bashTool.handler({
      command: "echo no-timeout-ok",
      description: "zero timeout disables timer",
      timeout: 0,
    });
    assert.equal(noTimeout.isError, undefined, noTimeout.content[0].text);
    assert.match(noTimeout.content[0].text, /no-timeout-ok/);

    // classification + truncation: verbatim policy (`cat`) keeps head+tail when
    // output exceeds 500 lines; compressible policy (`seq`) applies budget trim.
    const verbatimDir = mkdtempSync(join(tmpdir(), "cave-bash-verbatim-"));
    try {
      const longFile = join(verbatimDir, "long.txt");
      writeFileSync(longFile, Array.from({ length: 600 }, (_, i) => `row ${i + 1}`).join("\n") + "\n", "utf-8");
      const verbatim = await bashTool.handler({
        command: `cat ${longFile}`,
        description: "verbatim policy long output",
        allowFailure: true,
      });
      assert.equal(verbatim.isError, undefined, verbatim.content[0].text);
      assert.match(verbatim.content[0].text, /lines truncated/);

      // secret redaction on/off toggles within the same bash call.
      const redactOn = await bashTool.handler({
        command: "echo 'Authorization: Bearer sk-aaaa-bbbb-cccc-dddd-eeee'",
        description: "redaction on",
      });
      assert.match(redactOn.content[0].text, /\[REDACTED:Bearer token\]/);
      const redactOff = await bashTool.handler({
        command: "echo 'Authorization: Bearer sk-aaaa-bbbb-cccc-dddd-eeee'",
        description: "redaction off",
        redact_secrets: false,
      });
      assert.match(redactOff.content[0].text, /sk-aaaa-bbbb-cccc-dddd-eeee/);
    } finally {
      rmSync(verbatimDir, { recursive: true, force: true });
    }

    // compressible (`seq`) gets budgeted when output exceeds the bash budget.
    const compressible = await bashTool.handler({
      command: "seq 1 200",
      description: "compressible long output",
      allowFailure: true,
    });
    assert.equal(compressible.isError, undefined, compressible.content[0].text);
    assert.match(compressible.content[0].text, /lines truncated/);

    // allowFailure suppresses isError on non-zero exit.
    const soft = await bashTool.handler({
      command: "sh -c 'exit 3'",
      description: "soft fail",
      allowFailure: true,
    });
    assert.equal(soft.isError, undefined);
    assert.match(soft.content[0].text, /\[exit: 3\]/);

    // Trimmed output (2000 identical lines collapse to a few) is archived
    // behind a handle pointing at the full text on disk.
    const big = await bashTool.handler({
      command: "yes 'abcdefghijklmnopqrstuvwxyz0123456789-abcdefghij' | head -n 2000",
      description: "archive-sized output",
      allowFailure: true,
    });
    assert.equal(big.isError, undefined, big.content[0].text);
    const bigHandle = /\[cave: full output \((\d+) lines\) at (\S+) — cave__read offset\/limit or cave__grep\]/.exec(big.content[0].text);
    assert.ok(bigHandle, big.content[0].text);
    const bigArchived = readFileSync(bigHandle[2], "utf8");
    assert.equal(bigArchived.split("\n").length, Number(bigHandle[1]), "handle line count");
    const bigLines = bigArchived.trimEnd().split("\n");
    assert.ok(bigLines.length >= 1999, "archive keeps every captured line");
    assert.ok(bigLines.every((l) => l === "abcdefghijklmnopqrstuvwxyz0123456789-abcdefghij"));
    assert.ok(bigArchived.length > big.content[0].text.length, "archive holds more than was shown");
  }
  console.log("cave__bash edge case tests passed");
  console.log();

  // Test read edge cases
  console.log("11. Testing cave__read edge cases:");
  const readEdgeDir = mkdtempSync(join(tmpdir(), "cave-read-edge-"));
  try {
    // binary rejection via NUL byte.
    const binFile = join(readEdgeDir, "bin.dat");
    writeFileSync(binFile, Buffer.from([0x00, 0x01, 0x02, 0x03]));
    const binResult = await readTool.handler({ file_path: binFile });
    assert.equal(binResult.isError, true);
    assert.match(binResult.content[0].text, /Cannot read binary file:.*bin\.dat/);

    // binary rejection via known extension.
    const zipFile = join(readEdgeDir, "blob.zip");
    writeFileSync(zipFile, "PK\x03\x04");
    const zipResult = await readTool.handler({ file_path: zipFile });
    assert.equal(zipResult.isError, true);

    // image magic byte → image content block.
    const pngFile = join(readEdgeDir, "pixel.png");
    const pngBytes = Buffer.from([
      0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
      0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
    ]);
    writeFileSync(pngFile, pngBytes);
    const pngResult = await readTool.handler({ file_path: pngFile });
    assert.equal(pngResult.isError, undefined);
    assert.equal(pngResult.content[0].type, "image");
    assert.equal(pngResult.content[0].mimeType, "image/png");

    // offset/limit slicing.
    const linesFile = join(readEdgeDir, "lines.txt");
    writeFileSync(linesFile, Array.from({ length: 50 }, (_, i) => `L${i + 1}`).join("\n") + "\n", "utf-8");
    const slice = await readTool.handler({ file_path: linesFile, offset: 10, limit: 3 });
    assert.match(slice.content[0].text, /L10/);
    assert.match(slice.content[0].text, /L12/);
    assert.doesNotMatch(slice.content[0].text, /L13/);

    // missing file surfaces a "did you mean" style hint.
    const missing = await readTool.handler({ file_path: join(readEdgeDir, "linnes.txt") });
    assert.equal(missing.isError, true);
    assert.match(missing.content[0].text, /File not found/);

    // signatures mode on a TS file emits only signatures.
    const tsFile = join(readEdgeDir, "mod.ts");
    writeFileSync(
      tsFile,
      "export function add(a: number, b: number): number {\n  return a + b;\n}\n\nexport class Box {\n  constructor(public v: number) {}\n}\n",
      "utf-8",
    );
    const sigs = await readTool.handler({ file_path: tsFile, mode: "signatures" });
    assert.match(sigs.content[0].text, /export fn add/);
    assert.doesNotMatch(sigs.content[0].text, /return a \+ b/);

    // aggressive mode strips comments.
    const jsFile = join(readEdgeDir, "code.js");
    writeFileSync(jsFile, "// header comment\nconst x = 1;\n\n\n\nconst y = 2;\n", "utf-8");
    const agg = await readTool.handler({ file_path: jsFile, mode: "aggressive" });
    assert.doesNotMatch(agg.content[0].text, /header comment/);
    assert.match(agg.content[0].text, /const x = 1/);
  } finally {
    rmSync(readEdgeDir, { recursive: true, force: true });
  }
  console.log("cave__read edge case tests passed");
  console.log();

  // Test webfetch
  console.log("12. Testing cave__webfetch:");
  {
    // Missing URL.
    const noUrl = await webfetchTool.handler({ format: "markdown" });
    assert.equal(noUrl.isError, true);
    assert.match(noUrl.content[0].text, /url is required/);

    // Non-http(s) rejected.
    const badScheme = await webfetchTool.handler({ url: "ftp://example.com" });
    assert.equal(badScheme.isError, true);
    assert.match(badScheme.content[0].text, /URL must start with http:\/\/ or https:\/\//);

    // Spin up a local HTTP server for fixture responses.
    const server = createServer((req, res) => {
      const url = req.url || "/";
      if (url === "/text") {
        res.setHeader("content-type", "text/plain");
        res.end("hello plain text");
      } else if (url === "/html") {
        res.setHeader("content-type", "text/html; charset=utf-8");
        res.end(
          "<!DOCTYPE html><html><head><script>var x=1;</script><style>body{}</style></head>" +
            "<body><h1>Title</h1><p>Hello <a href=\"/x\">link</a> world</p><p>Second paragraph.</p></body></html>",
        );
      } else if (url === "/meta") {
        // Real-world page with void elements (meta/link) in <head> — must not
        // suppress body content. Also exercises <pre><code> nesting.
        res.setHeader("content-type", "text/html; charset=utf-8");
        res.end(
          "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><link rel=\"stylesheet\" href=\"/x.css\">" +
            "<title>Test</title></head><body><h1>Body Title</h1><p>Body text survives.</p>" +
            "<p>alpha<br>beta</p><hr>" +
            "<pre><code>code line 1\nline 2</code></pre></body></html>",
        );
      } else if (url === "/img") {
        res.setHeader("content-type", "image/png");
        res.end(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
      } else if (url === "/bin") {
        res.setHeader("content-type", "application/octet-stream");
        res.end(Buffer.from([0x00, 0x01, 0x02, 0x03]));
      } else if (url === "/big") {
        res.setHeader("content-type", "text/plain");
        res.end("x".repeat(6 * 1024 * 1024));
      } else {
        res.statusCode = 404;
        res.end("not found");
      }
    });
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const port = server.address().port;
    const base = `http://127.0.0.1:${port}`;
    try {
      // text format returns body as-is for text/plain.
      const textRes = await webfetchTool.handler({ url: `${base}/text`, format: "text" });
      assert.equal(textRes.isError, undefined, textRes.content[0].text);
      assert.match(textRes.content[0].text, /hello plain text/);

      // markdown format converts HTML: heading emitted, script/style stripped, link rendered.
      const mdRes = await webfetchTool.handler({ url: `${base}/html`, format: "markdown" });
      assert.equal(mdRes.isError, undefined, mdRes.content[0].text);
      const md = mdRes.content[0].text;
      assert.match(md, /# Title/);
      assert.match(md, /\[link\]\(\/x\)/);
      assert.doesNotMatch(md, /var x=1/);
      assert.doesNotMatch(md, /body\{\}/);

      // text format on HTML strips tags and skips script/style.
      const htmlText = await webfetchTool.handler({ url: `${base}/html`, format: "text" });
      assert.match(htmlText.content[0].text, /Title/);
      assert.match(htmlText.content[0].text, /Hello/);
      assert.doesNotMatch(htmlText.content[0].text, /<script>|<style>|var x=1/);

      // html format returns raw HTML.
      const rawHtml = await webfetchTool.handler({ url: `${base}/html`, format: "html" });
      assert.match(rawHtml.content[0].text, /<h1>Title<\/h1>/);

      // images are rejected (matching upstream opencode webfetch).
      const imgRes = await webfetchTool.handler({ url: `${base}/img` });
      assert.equal(imgRes.isError, true);
      assert.match(imgRes.content[0].text, /Unsupported fetched image content type: image\/png/);

      // 5MB cap rejects oversized responses.
      const bigRes = await webfetchTool.handler({ url: `${base}/big`, timeout: 10 });
      assert.equal(bigRes.isError, true);
      assert.match(bigRes.content[0].text, /Response too large \(exceeds 5MB limit\)/);

      // timeout validation: negative timeout falls back to default (no crash).
      const negTimeout = await webfetchTool.handler({ url: `${base}/text`, timeout: -5 });
      assert.equal(negTimeout.isError, undefined);
      assert.match(negTimeout.content[0].text, /hello plain text/);

      // Void elements (meta/link) in <head> must not suppress body content.
      const metaMd = await webfetchTool.handler({ url: `${base}/meta`, format: "markdown" });
      assert.equal(metaMd.isError, undefined, metaMd.content[0].text);
      assert.match(metaMd.content[0].text, /Body Title/);
      assert.match(metaMd.content[0].text, /Body text survives\./);
      // <pre><code> → fenced block, no stray inline backticks from <code>.
      assert.match(metaMd.content[0].text, /```/);
      assert.match(metaMd.content[0].text, /code line 1/);
      // <br> renders a line break and <hr> renders a rule (void-tag skip must
      // not swallow their handlers).
      assert.match(metaMd.content[0].text, /alpha\nbeta/);
      assert.match(metaMd.content[0].text, /^---$/m);

      const metaText = await webfetchTool.handler({ url: `${base}/meta`, format: "text" });
      assert.equal(metaText.isError, undefined, metaText.content[0].text);
      assert.match(metaText.content[0].text, /Body text survives\./);
      assert.doesNotMatch(metaText.content[0].text, /utf-8|x\.css/);

      // Non-2xx status is an error, not success content.
      const binRes = await webfetchTool.handler({ url: `${base}/bin`, timeout: 10 });
      assert.equal(binRes.isError, true);
      assert.match(binRes.content[0].text, /Unsupported fetched file content type/);

      const notFound = await webfetchTool.handler({ url: `${base}/nonexistent`, timeout: 10 });
      assert.equal(notFound.isError, true);
      assert.match(notFound.content[0].text, /Request failed with status 404/);
    } finally {
      await new Promise((resolve) => server.close(resolve));
    }
  }
  console.log("cave__webfetch tests passed");
  console.log();

  // Test CLI status UI renderer
  console.log("12. Testing status-ui renderer:");
  {
    const {
      formatCompact,
      renderStatusCli,
      coloredMeter,
    } = await import("./dist/status-ui.js");

    assert.equal(formatCompact(87525037), "87.5M");
    assert.equal(formatCompact(1200), "1.2k");
    assert.equal(formatCompact(42), "42");
    assert.equal(formatCompact(15000), "15k");

    const plain = renderStatusCli({
      rtkAvailable: true,
      reductionPct: 48.2,
      hitRatePct: 82.1,
      cacheHits: 410,
      cacheMisses: 90,
      filesTracked: 12,
      rtkRewrites: 12,
      rtkAlreadyWrapped: 3,
      rtkPassthrough: 40,
      totalCalls: 128,
      rawChars: 4_800_000,
      compressedChars: 2_400_000,
      compressionSavedChars: 2_400_000,
      dedupSavedChars: 900_000,
      savedChars: 3_300_000,
      tokensSaved: 825_000,
      liveSessions: 2,
      endedSessions: 5,
      lastUpdate: Date.now() - 12_000,
      hasData: true,
      budgets: {
        bash: { maxLines: 200, headLines: 80, tailLines: 40 },
        read: { maxLines: 200, headLines: 80, tailLines: 40 },
      },
      byTool: [
        { name: "bash", calls: 40, rawChars: 1_000_000, savedChars: 620_000 },
        { name: "read", calls: 55, rawChars: 800_000, savedChars: 300_000 },
      ],
      rtkGain: {
        totalCommands: 10362,
        totalInput: 99_500_000,
        totalOutput: 12_000_000,
        totalSaved: 87_500_000,
        avgSavingsPct: 87.9,
        totalTimeMs: 30_000_000,
        avgTimeMs: 2900,
      },
      rtkGainSkipped: false,
      verbose: false,
    });

    // Force plain-mode assertions via env is hard mid-test; check substance.
    assert.match(plain, /cave-tools/);
    assert.match(plain, /by tool/);
    assert.match(plain, /rtk gain/);
    assert.match(plain, /bash/);
    assert.match(plain, /health/);
    assert.match(plain, /budgets/);
    assert.match(plain, /tokens|825k|825\.0k|825000/i);
    assert.match(plain, /87\.9%/); // rtk gain pct

    const empty = renderStatusCli({
      rtkAvailable: false,
      reductionPct: 0,
      hitRatePct: 0,
      cacheHits: 0,
      cacheMisses: 0,
      filesTracked: 0,
      rtkRewrites: 0,
      rtkAlreadyWrapped: 0,
      rtkPassthrough: 0,
      totalCalls: 0,
      rawChars: 0,
      compressedChars: 0,
      compressionSavedChars: 0,
      dedupSavedChars: 0,
      savedChars: 0,
      tokensSaved: 0,
      liveSessions: 0,
      endedSessions: 0,
      lastUpdate: 0,
      hasData: false,
      budgets: { bash: { maxLines: 200, headLines: 80, tailLines: 40 } },
      byTool: [],
      rtkGain: null,
      rtkGainSkipped: false,
      verbose: false,
    });
    assert.match(empty, /No session data yet/);
    assert.match(empty, /offline|rtk/i);

    const meter = coloredMeter(50, 10, false);
    assert.equal(meter.length, 10);

    const verbose = renderStatusCli({
      rtkAvailable: true,
      reductionPct: 10,
      hitRatePct: 50,
      cacheHits: 1,
      cacheMisses: 1,
      filesTracked: 1,
      rtkRewrites: 0,
      rtkAlreadyWrapped: 0,
      rtkPassthrough: 0,
      totalCalls: 1,
      rawChars: 1000,
      compressedChars: 900,
      compressionSavedChars: 100,
      dedupSavedChars: 0,
      savedChars: 100,
      tokensSaved: 25,
      liveSessions: 1,
      endedSessions: 0,
      lastUpdate: Date.now(),
      hasData: true,
      budgets: { bash: { maxLines: 10, headLines: 4, tailLines: 4 } },
      byTool: [],
      rtkGain: null,
      rtkGainSkipped: true,
      verbose: true,
    });
    assert.match(verbose, /detail|raw chars/i);
    assert.match(verbose, /CAVE_TOOLS_STATUS_RTK=0|skipped/i);
  }
  console.log("status-ui tests passed");
  console.log();

  // Background jobs (cave__bash_start/status/stop)
  {
    console.log("Testing background jobs:");
    const session = "test-jobs";
    const createdLogs = [];
    const createdIds = [];

    // 1. Fast command completes; status reports exit code and log content.
    const start1 = await bashStartTool.handler({
      command: "echo hello-bg",
      description: "fast echo job",
      __sessionId: session,
    });
    assert.match(start1.content[0].text, /jobId: (\S+)/);
    const job1 = start1.content[0].text.match(/jobId: (\S+)/)[1];
    createdIds.push(job1);
    createdLogs.push(start1.content[0].text.match(/logPath: (\S+)/)[1]);

    const status1 = await bashStatusTool.handler({ jobId: job1, wait: 30, __sessionId: session });
    assert.match(status1.content[0].text, /exited \(code 0\)/);
    assert.match(status1.content[0].text, /hello-bg/);
    assert.equal(status1.isError, undefined);

    // Status-only reads no log bytes. Cursor mode returns each byte range once.
    const statusOnly = await bashStatusTool.handler({
      jobId: job1,
      tail_lines: 0,
      __sessionId: session,
    });
    assert.match(statusOnly.content[0].text, /tail_lines=0 read no log bytes/);
    assert.match(statusOnly.content[0].text, /next_cursor: \d+/);
    const cursorFirst = await bashStatusTool.handler({
      jobId: job1,
      cursor: 0,
      tail_lines: 100,
      __sessionId: session,
    });
    const nextCursor = Number(cursorFirst.content[0].text.match(/next_cursor: (\d+)/)[1]);
    const firstBody = cursorFirst.content[0].text.slice(cursorFirst.content[0].text.indexOf("\n\n") + 2);
    assert.match(firstBody, /hello-bg/);
    const cursorSecond = await bashStatusTool.handler({
      jobId: job1,
      cursor: nextCursor,
      tail_lines: 100,
      __sessionId: session,
    });
    const secondBody = cursorSecond.content[0].text.slice(cursorSecond.content[0].text.indexOf("\n\n") + 2);
    assert.match(secondBody, /\(no new log output\)/);
    assert.doesNotMatch(secondBody, /hello-bg/);

    // 2. wait returns as soon as the command finishes, not after full timeout.
    const start2 = await bashStartTool.handler({
      command: "sleep 2; echo done-wait",
      description: "two second sleep job",
      __sessionId: session,
    });
    const job2 = start2.content[0].text.match(/jobId: (\S+)/)[1];
    createdIds.push(job2);
    createdLogs.push(start2.content[0].text.match(/logPath: (\S+)/)[1]);
    const t0 = Date.now();
    const status2 = await bashStatusTool.handler({ jobId: job2, wait: 60, __sessionId: session });
    assert.ok(Date.now() - t0 < 30000, "wait must return right after job exit");
    assert.match(status2.content[0].text, /exited \(code 0\)/);
    assert.match(status2.content[0].text, /done-wait/);

    // 3. Long command: running state, then stop -> killed.
    const start3 = await bashStartTool.handler({
      command: "sleep 300",
      description: "long sleep job",
      __sessionId: session,
    });
    const job3 = start3.content[0].text.match(/jobId: (\S+)/)[1];
    createdIds.push(job3);
    createdLogs.push(start3.content[0].text.match(/logPath: (\S+)/)[1]);
    const status3 = await bashStatusTool.handler({ jobId: job3, __sessionId: session });
    assert.match(status3.content[0].text, /running/);
    const stop3 = await bashStopTool.handler({ jobId: job3 });
    assert.match(stop3.content[0].text, /killed/);
    const status3b = await bashStatusTool.handler({ jobId: job3, __sessionId: session });
    assert.match(status3b.content[0].text, /killed/);

    // 4. Unknown job errors.
    const statusUnknown = await bashStatusTool.handler({ jobId: "j-nope", __sessionId: session });
    assert.equal(statusUnknown.isError, true);
    const stopUnknown = await bashStopTool.handler({ jobId: "j-nope" });
    assert.equal(stopUnknown.isError, true);

    // 5. No-jobId status lists only the calling session's jobs.
    const list = await bashStatusTool.handler({ __sessionId: session });
    assert.match(list.content[0].text, new RegExp(job1));
    const listOther = await bashStatusTool.handler({ __sessionId: "other-session" });
    assert.match(listOther.content[0].text, /No background jobs/);

    // 6. Restart recovery: row marked running with a dead pid.
    //    With exit marker -> recovered exit code; without marker -> lost.
    const deadPid = 4194303;
    const mkJob = (id, withMarker) => {
      const logPath = join(mkdtempSync(join(tmpdir(), "cave-job-")), `${id}.log`);
      writeFileSync(logPath, `fake log\n${withMarker ? "[cave-job exit=3]\n" : ""}`);
      insertJob({
        jobId: id,
        pid: deadPid,
        sessionId: session,
        command: "fake",
        workdir: null,
        startedAt: Date.now() - 1000,
        endedAt: null,
        exitCode: null,
        signal: null,
        state: "running",
        logPath,
      });
      createdIds.push(id);
      createdLogs.push(logPath);
      return id;
    };
    const recId = mkJob("j-test-recover", true);
    const recovered = getJobInfo(recId);
    assert.equal(recovered.state, "exited");
    assert.equal(recovered.exitCode, 3);
    const lostId = mkJob("j-test-lost", false);
    const lost = getJobInfo(lostId);
    assert.equal(lost.state, "lost");

    // 7. Non-zero exit surfaces isError on status.
    const start7 = await bashStartTool.handler({
      command: "exit 7",
      description: "failing job",
      __sessionId: session,
    });
    const job7 = start7.content[0].text.match(/jobId: (\S+)/)[1];
    createdIds.push(job7);
    createdLogs.push(start7.content[0].text.match(/logPath: (\S+)/)[1]);
    const status7 = await bashStatusTool.handler({ jobId: job7, wait: 30, __sessionId: session });
    assert.match(status7.content[0].text, /exited \(code 7\)/);
    assert.equal(status7.isError, true);

    // 8. Metadata and log headers are redacted together.
    const secret = "0123456789abcdef0123456789abcdef";
    const start8 = await bashStartTool.handler({
      command: `printf safe-output # token=${secret}`,
      description: "redaction metadata job",
      __sessionId: session,
    });
    const job8 = start8.content[0].text.match(/jobId: (\S+)/)[1];
    createdIds.push(job8);
    createdLogs.push(start8.content[0].text.match(/logPath: (\S+)/)[1]);
    const status8 = await bashStatusTool.handler({ jobId: job8, wait: 30, __sessionId: session });
    assert.doesNotMatch(status8.content[0].text, new RegExp(secret));
    assert.match(status8.content[0].text, /REDACTED/);

    // 9. Byte chunks never split a UTF-8 sequence and resume without duplicates.
    const utfLog = join(mkdtempSync(join(tmpdir(), "cave-job-utf-")), "utf.log");
    writeFileSync(utfLog, `${"a".repeat(65535)}€Z`, "utf-8");
    createdLogs.push(utfLog);
    const utfFirst = await readFileChunk(utfLog, 0, 65536);
    assert.equal(utfFirst.text, "a".repeat(65535));
    assert.equal(utfFirst.nextCursor, 65535);
    const utfSecond = await readFileChunk(utfLog, utfFirst.nextCursor, 65536);
    assert.equal(utfSecond.text, "€Z");

    // 10. A launcher process can die while its detached worker later records
    // structured completion; adopted wait polls and returns before its timeout.
    const jobsModule = new URL("./dist/runtime/jobs.js", import.meta.url).href;
    const restartScript = `
      const { startJob } = await import(${JSON.stringify(jobsModule)});
      const job = await startJob({
        command: "sleep 1; printf durable-restart; exit 4",
        sessionId: ${JSON.stringify(session)}
      });
      process.stdout.write(JSON.stringify(job));
      process.exit(0);
    `;
    const restartJob = JSON.parse(execFileSync(
      process.execPath,
      ["--input-type=module", "-e", restartScript],
      { encoding: "utf-8", env: process.env },
    ));
    createdIds.push(restartJob.jobId);
    createdLogs.push(restartJob.logPath);
    const restartWaitAt = Date.now();
    const restarted = await waitForJob(restartJob.jobId, 10_000);
    assert.ok(Date.now() - restartWaitAt < 5_000, "adopted wait returns soon after completion");
    assert.equal(restarted.state, "exited");
    assert.equal(restarted.exitCode, 4);
    assert.match(readFileSync(restartJob.logPath, "utf-8"), /durable-restart/);
    assert.ok(existsSync(resultPathFor(restartJob.jobId)), "worker wrote durable result sidecar");

    // 11. Pruning never removes an old log still referenced by a running row.
    const activeId = "j-test-active-prune";
    const activeLog = join(mkdtempSync(join(tmpdir(), "cave-job-")), `${activeId}.log`);
    writeFileSync(activeLog, "active\n");
    const old = new Date(Date.now() - 48 * 60 * 60 * 1000);
    utimesSync(activeLog, old, old);
    insertJob({
      jobId: activeId,
      pid: process.pid,
      sessionId: session,
      command: "active",
      workdir: null,
      startedAt: old.getTime(),
      endedAt: null,
      exitCode: null,
      signal: null,
      state: "running",
      logPath: activeLog,
    });
    createdIds.push(activeId);
    createdLogs.push(activeLog);
    pruneJobs();
    assert.ok(existsSync(activeLog), "active job log survives orphan pruning");

    // Cleanup: remove test rows and logs.
    for (const id of createdIds) {
      try { unlinkSync(resultPathFor(id)); } catch { /* best effort */ }
      try { deleteJob(id); } catch { /* best effort */ }
    }
    for (const log of createdLogs) {
      try { unlinkSync(log); } catch { /* best effort */ }
    }
    console.log("background jobs tests passed");
    console.log();
  }

  console.log("13. Testing filesTracked + subagent context + writeIfUnchanged:");
  {
    const savedEnv = {
      CAVE_TOOLS_FORCE_FRESH: process.env.CAVE_TOOLS_FORCE_FRESH,
      CURSOR_TASK_ID: process.env.CURSOR_TASK_ID,
      CLINE_TASK_ID: process.env.CLINE_TASK_ID,
      CLAUDE_CODE_SUBAGENT: process.env.CLAUDE_CODE_SUBAGENT,
      ANTHROPIC_SUBAGENT: process.env.ANTHROPIC_SUBAGENT,
      FOO_TASK_ID: process.env.FOO_TASK_ID,
      CLAUDE_CODE_ENTRYPOINT: process.env.CLAUDE_CODE_ENTRYPOINT,
      MY_SUBAGENT_ID: process.env.MY_SUBAGENT_ID,
    };
    const applyEnv = (key, value) => {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    };
    const restoreEnv = () => {
      for (const [key, value] of Object.entries(savedEnv)) applyEnv(key, value);
      resetSubagentContextCache();
    };
    const clearSubagentEnv = () => {
      for (const key of Object.keys(savedEnv)) delete process.env[key];
      resetSubagentContextCache();
    };

    try {
      clearSubagentEnv();
      assert.equal(isSubagentContext(), false, "no signals => not subagent");

      process.env.CAVE_TOOLS_FORCE_FRESH = "1";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "FORCE_FRESH=1");

      process.env.CAVE_TOOLS_FORCE_FRESH = "true";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "FORCE_FRESH=true");

      process.env.CAVE_TOOLS_FORCE_FRESH = "yes";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), false, "FORCE_FRESH=yes ignored");

      delete process.env.CAVE_TOOLS_FORCE_FRESH;
      process.env.CLINE_TASK_ID = "abc";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "CLINE_TASK_ID");

      delete process.env.CLINE_TASK_ID;
      process.env.CLAUDE_CODE_SUBAGENT = "1";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "CLAUDE_CODE_SUBAGENT");

      delete process.env.CLAUDE_CODE_SUBAGENT;
      process.env.ANTHROPIC_SUBAGENT = "yes";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "ANTHROPIC_SUBAGENT");

      delete process.env.ANTHROPIC_SUBAGENT;
      process.env.CURSOR_TASK_ID = "cursor-task";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "CURSOR_TASK_ID");

      delete process.env.CURSOR_TASK_ID;
      process.env.FOO_TASK_ID = "t";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), true, "TASK_ID-style FOO_TASK_ID");

      delete process.env.FOO_TASK_ID;
      process.env.CLAUDE_CODE_ENTRYPOINT = "cli";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), false, "unknown CLAUDE_CODE_* ignored");

      delete process.env.CLAUDE_CODE_ENTRYPOINT;
      process.env.MY_SUBAGENT_ID = "x";
      resetSubagentContextCache();
      assert.equal(isSubagentContext(), false, "unknown *SUBAGENT* ignored");
      delete process.env.MY_SUBAGENT_ID;
      resetSubagentContextCache();

      resetCache();
      const dir = mkdtempSync(join(tmpdir(), "cave-cache-"));
      const sample = join(dir, "a.txt");
      writeFileSync(sample, "hello");
      await updateFileCache(sample, "sess");
      assert.equal(await isFileUnchanged(sample, "sess"), true, "same session stubs");
      process.env.CAVE_TOOLS_FORCE_FRESH = "1";
      resetSubagentContextCache();
      assert.equal(await isFileUnchanged(sample, "sess"), false, "subagent never stubs");
      delete process.env.CAVE_TOOLS_FORCE_FRESH;
      resetSubagentContextCache();

      resetCache();
      const files = [0, 1, 2].map((i) => {
        const p = join(dir, `f${i}.txt`);
        writeFileSync(p, `content-${i}`);
        return p;
      });
      for (const sid of ["s1", "s2"]) {
        for (const p of files) await updateFileCache(p, sid);
      }
      assert.equal(getCacheStats().filesTracked, 6, "getCacheStats filesTracked");
      assert.equal(buildStats().cache.filesTracked, 6, "buildStats filesTracked");

      // Dedup is representation-aware: exact repeats stub, but a new range,
      // mode, or numbered view must be delivered once before it can stub.
      resetCache();
      const ranged = join(dir, "ranges.ts");
      writeFileSync(
        ranged,
        "export function alpha() {\n  return 1;\n}\nexport function beta() {\n  return 2;\n}\n",
      );
      const rangeArgs = {
        file_path: ranged,
        offset: 1,
        limit: 2,
        mode: "full",
        __sessionId: "range-session",
      };
      const rangeFirst = await readTool.handler(rangeArgs);
      assert.match(rangeFirst.content[0].text, /alpha/);
      const rangeRepeat = await readTool.handler(rangeArgs);
      assert.match(rangeRepeat.content[0].text, /file unchanged/);
      const rangeNext = await readTool.handler({ ...rangeArgs, offset: 3 });
      assert.doesNotMatch(rangeNext.content[0].text, /file unchanged/);
      assert.match(rangeNext.content[0].text, /}/);
      const signaturesFirst = await readTool.handler({
        ...rangeArgs,
        mode: "signatures",
      });
      assert.doesNotMatch(signaturesFirst.content[0].text, /file unchanged/);
      assert.match(signaturesFirst.content[0].text, /alpha/);
      const signaturesRepeat = await readTool.handler({
        ...rangeArgs,
        mode: "signatures",
      });
      assert.match(signaturesRepeat.content[0].text, /file unchanged/);
      const numbered = await readTool.handler({ ...rangeArgs, line_numbers: true });
      assert.doesNotMatch(numbered.content[0].text, /file unchanged/);
      assert.match(numbered.content[0].text, /cave_edit_meta/);

      const changed = join(dir, "changed.txt");
      writeFileSync(changed, "old");
      const expected = readFileSync(changed);
      writeFileSync(changed, "new");
      await assert.rejects(
        () => writeIfUnchanged(changed, expected, "newer"),
        /File changed after it was read.*cave__read.*cave__invalidate/,
      );

      rmSync(dir, { recursive: true, force: true });
      resetCache();
    } finally {
      restoreEnv();
      resetCache();
    }
    console.log("filesTracked + subagent context tests passed");
    console.log();
  }

  console.log("14. Testing RTK strip / timeout / counters:");
  {
    assert.deepEqual(stripLeadingRtk("rtk git status"), { command: "git status", stripped: true });
    assert.deepEqual(stripLeadingRtk("FOO=1 rtk git status"), { command: "FOO=1 git status", stripped: true });
    assert.deepEqual(stripLeadingRtk("FOO=1 BAR=2 rtk git status"), {
      command: "FOO=1 BAR=2 git status",
      stripped: true,
    });
    assert.deepEqual(stripLeadingRtk('"rtk git status"'), { command: "git status", stripped: true });
    assert.deepEqual(stripLeadingRtk("'rtk git status'"), { command: "git status", stripped: true });
    assert.deepEqual(stripLeadingRtk("rtk rtk git status"), { command: "rtk git status", stripped: true });
    assert.equal(stripLeadingRtk("git status").stripped, false);
    assert.equal(stripLeadingRtk("rtk").stripped, false);
    assert.deepEqual(stripLeadingRtk("  rtk   git status  "), { command: "git status", stripped: true });

    const fakeDir = mkdtempSync(join(tmpdir(), "cave-rtk-fake-"));
    const fakeRtk = join(fakeDir, "rtk");
    writeFileSync(fakeRtk, `#!/usr/bin/env node
const args = process.argv.slice(2);
if (args[0] === "--version") {
  process.stdout.write("rtk-fake 0\\n");
  process.exit(0);
}
if (args[0] === "rewrite") {
  const cmd = args.slice(1).join(" ");
  const sleepMs = Number(process.env.FAKE_RTK_SLEEP_MS || "0");
  if (sleepMs > 0) {
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, sleepMs);
  }
  if (process.env.FAKE_RTK_EMPTY === "1") process.exit(1);
  if (process.env.FAKE_RTK_IDENTICAL === "1") {
    process.stdout.write(cmd);
    process.exit(0);
  }
  process.stdout.write(cmd ? ("rtk " + cmd) : "");
  process.exit(0);
}
process.exit(1);
`);
    chmodSync(fakeRtk, 0o755);

    const prevPath = process.env.PATH;
    const prevEmpty = process.env.FAKE_RTK_EMPTY;
    const prevIdent = process.env.FAKE_RTK_IDENTICAL;
    const prevSleep = process.env.FAKE_RTK_SLEEP_MS;
    process.env.PATH = fakeDir + pathDelimiter + (prevPath || "");
    resetRtkAvailabilityCache();

    const rtkDelta = (before, after) => ({
      rewrites: after.rewrites - before.rewrites,
      alreadyWrapped: after.alreadyWrapped - before.alreadyWrapped,
      passthrough: after.passthrough - before.passthrough,
    });
    const assertOnce = (d, field, msg) => {
      const sum = d.rewrites + d.alreadyWrapped + d.passthrough;
      assert.equal(sum, 1, `${msg}: expected exactly one counter, got ${JSON.stringify(d)}`);
      assert.equal(d[field], 1, `${msg}: expected ${field}=1`);
    };

    try {
      let before = getRtkStats();
      const wrapped = await rewriteCommandWithRtk("rtk echo doublewrap");
      assert.equal(wrapped, "rtk echo doublewrap", "double-wrap stripped once");
      assert.doesNotMatch(wrapped, /rtk rtk/);
      assertOnce(rtkDelta(before, getRtkStats()), "alreadyWrapped", "strip wrap");

      before = getRtkStats();
      const rewritten = await rewriteCommandWithRtk("echo doublewrap");
      assert.equal(rewritten, "rtk echo doublewrap", "plain command rewritten once");
      assertOnce(rtkDelta(before, getRtkStats()), "rewrites", "plain rewrite");

      process.env.FAKE_RTK_IDENTICAL = "1";
      before = getRtkStats();
      const ident = await rewriteCommandWithRtk("echo same");
      assert.equal(ident, "echo same");
      assertOnce(rtkDelta(before, getRtkStats()), "alreadyWrapped", "identical stdout");
      delete process.env.FAKE_RTK_IDENTICAL;

      process.env.FAKE_RTK_EMPTY = "1";
      before = getRtkStats();
      const emptyOut = await rewriteCommandWithRtk("weirdcmd --flag");
      assert.equal(emptyOut, "weirdcmd --flag", "empty stdout returns original");
      assertOnce(rtkDelta(before, getRtkStats()), "passthrough", "empty stdout");
      const top = getRtkTopPassthrough();
      assert.ok(top.some((e) => e.prefix === "weirdcmd --flag"), "passthrough prefix tracked");
      delete process.env.FAKE_RTK_EMPTY;

      process.env.FAKE_RTK_SLEEP_MS = "5000";
      before = getRtkStats();
      const timed = await rewriteCommandWithRtk("sleepcmd now");
      assert.equal(timed, "sleepcmd now", "timeout fallback returns original");
      assertOnce(rtkDelta(before, getRtkStats()), "passthrough", "timeout");
      delete process.env.FAKE_RTK_SLEEP_MS;

      process.env.PATH = prevPath;
      resetRtkAvailabilityCache();
      const bashWrap = await bashTool.handler({
        command: "rtk echo CAVE_RTK_DOUBLEWRAP",
        description: "strip one rtk wrap then run",
      });
      assert.match(bashWrap.content[0].text, /CAVE_RTK_DOUBLEWRAP/);
      assert.notEqual(bashWrap.isError, true);
    } finally {
      process.env.PATH = prevPath;
      if (prevEmpty === undefined) delete process.env.FAKE_RTK_EMPTY;
      else process.env.FAKE_RTK_EMPTY = prevEmpty;
      if (prevIdent === undefined) delete process.env.FAKE_RTK_IDENTICAL;
      else process.env.FAKE_RTK_IDENTICAL = prevIdent;
      if (prevSleep === undefined) delete process.env.FAKE_RTK_SLEEP_MS;
      else process.env.FAKE_RTK_SLEEP_MS = prevSleep;
      resetRtkAvailabilityCache();
      rmSync(fakeDir, { recursive: true, force: true });
    }
    console.log("RTK strip / timeout / counter tests passed");
    console.log();
  }

  {
    console.log("Testing tool profile filtering:");
    const hidden = ["cave__configure", "cave__compress", "cave__status"];
    const full = listedTools("full").map((t) => t.name);
    assert.equal(full.length, 17, "full lists all tools");
    for (const name of hidden) assert.ok(full.includes(name), `full lists ${name}`);
    for (const profile of [undefined, "", "standard", "bogus"]) {
      const names = listedTools(profile).map((t) => t.name);
      assert.equal(names.length, 14, `profile ${profile} lists 14 tools`);
      for (const name of hidden) assert.ok(!names.includes(name), `profile ${profile} hides ${name}`);
    }
    assert.equal(listedTools(" FULL ").length, 17, "profile is trimmed + case-insensitive");
    console.log("tool profile tests passed\n");
  }

  {
    console.log("Testing compact ruleset extraction:");
    const require = createRequire(import.meta.url);
    const config = require("./claude/hooks/cave-tools-config.js");
    const skill = readFileSync("./claude/skills/cave-tools/SKILL.md", "utf8");
    for (const mode of ["hint", "enforce", "strict"]) {
      const compact = config.extractCompactRuleset(skill, mode);
      assert.ok(compact, `${mode}: compact block found`);
      assert.equal(compact, config.fallbackRuleset(mode), `${mode}: fallback in sync with SKILL.md`);
      assert.equal(config.buildRuleset(mode), compact, `${mode}: buildRuleset uses compact block`);
      assert.ok(Buffer.byteLength(compact, "utf8") <= 900, `${mode}: compact ruleset <= 900 bytes`);
      assert.match(compact, new RegExp(`\\*\\*${mode}\\*\\*`), `${mode}: keeps active mode row`);
      for (const other of ["hint", "enforce", "strict"].filter((m) => m !== mode)) {
        assert.doesNotMatch(compact, new RegExp(`\\*\\*${other}\\*\\*`), `${mode}: drops ${other} row`);
      }
      assert.doesNotMatch(compact, /cave-tools-compact-(begin|end)/, `${mode}: markers stripped`);
    }
    assert.equal(config.extractCompactRuleset("# no markers here", "enforce"), null);
    assert.equal(
      config.extractCompactRuleset("<!-- cave-tools-compact-begin -->\nunterminated", "enforce"),
      null,
    );
    console.log("compact ruleset tests passed\n");
  }

  {
    console.log("Testing per-turn reminder by mode:");
    const configDir = mkdtempSync(join(tmpdir(), "cave-reminder-"));
    try {
      const runTracker = (mode) => {
        writeFileSync(join(configDir, ".cave-tools-active"), mode);
        return execFileSync(process.execPath, ["./claude/hooks/cave-tools-mode-tracker.js"], {
          input: JSON.stringify({ prompt: "hello" }),
          encoding: "utf8",
          env: { ...process.env, CLAUDE_CONFIG_DIR: configDir },
        });
      };
      const hint = JSON.parse(runTracker("hint"));
      assert.equal(hint.hookSpecificOutput.hookEventName, "UserPromptSubmit");
      assert.match(hint.hookSpecificOutput.additionalContext, /CAVE-TOOLS MODE ACTIVE \(hint\)/);
      assert.equal(runTracker("enforce"), "", "enforce: no reminder");
      assert.equal(runTracker("strict"), "", "strict: no reminder");
    } finally {
      rmSync(configDir, { recursive: true, force: true });
    }
    console.log("per-turn reminder tests passed\n");
  }

  {
    console.log("Testing usage analyzer:");
    const configDir = mkdtempSync(join(tmpdir(), "cave-usage-"));
    try {
      const projDir = join(configDir, "projects", "-work-alpha");
      mkdirSync(join(projDir, "s1", "subagents"), { recursive: true });
      const usage1 = {
        input_tokens: 10, cache_creation_input_tokens: 100, cache_read_input_tokens: 1000, output_tokens: 5,
        cache_creation: { ephemeral_1h_input_tokens: 60, ephemeral_5m_input_tokens: 40 },
      };
      const base = { sessionId: "S1", cwd: "/work/alpha" };
      const lines = [
        { ...base, type: "assistant", timestamp: "2026-10-01T00:00:00Z",
          message: { id: "msg_1", usage: usage1, content: [{ type: "tool_use", id: "tu1", name: "mcp__cave-tools__cave__read", input: {} }] } },
        // Same API response split across a second line: usage must count once.
        { ...base, type: "assistant", timestamp: "2026-10-01T00:00:01Z",
          message: { id: "msg_1", usage: usage1, content: [{ type: "tool_use", id: "tu2", name: "Bash", input: {} }] } },
        { ...base, type: "user", timestamp: "2026-10-01T00:00:02Z",
          message: { content: [{ type: "tool_result", tool_use_id: "tu1", content: "hello" }] } },
        { ...base, type: "user", timestamp: "2026-10-01T00:00:03Z",
          message: { content: [{ type: "tool_result", tool_use_id: "tu2", is_error: true,
            content: [{ type: "text", text: "abc" }, { type: "image", source: {} }] }] } },
        "{not json",
        { ...base, type: "assistant", timestamp: "2026-10-01T00:01:00Z",
          message: { id: "msg_2", usage: { input_tokens: 20, cache_creation_input_tokens: 0, cache_read_input_tokens: 2000, output_tokens: 7,
            cache_creation: { ephemeral_1h_input_tokens: 0, ephemeral_5m_input_tokens: 0 } }, content: [{ type: "text", text: "done" }] } },
      ];
      writeFileSync(join(projDir, "s1.jsonl"),
        lines.map((l) => (typeof l === "string" ? l : JSON.stringify(l))).join("\n") + "\n");
      const sub = { sessionId: "S2", cwd: "/work/beta" };
      writeFileSync(join(projDir, "s1", "subagents", "agent-a.jsonl"), [
        { ...sub, type: "assistant", timestamp: "2026-10-03T00:00:00Z",
          message: { id: "msg_3", usage: { input_tokens: 1, output_tokens: 1 },
            content: [{ type: "tool_use", id: "tu3", name: "mcp__other__thing", input: {} }] } },
        { ...sub, type: "user", timestamp: "2026-10-03T00:00:01Z",
          message: { content: [{ type: "tool_result", tool_use_id: "tu3", content: "xy" }] } },
      ].map((l) => JSON.stringify(l)).join("\n") + "\n");

      const runUsage = (...args) => JSON.parse(execFileSync(process.execPath,
        ["./scripts/analyze-usage.mjs", "--json", ...args],
        { encoding: "utf8", env: { ...process.env, CLAUDE_CONFIG_DIR: configDir } }));

      const all = runUsage();
      assert.equal(all.files, 2, "nested subagent transcript found");
      assert.equal(all.malformed_lines, 1);
      assert.equal(all.totals.requests, 3, "split message.id counted once");
      assert.equal(all.totals.input, 31);
      assert.equal(all.totals.cache_creation, 100);
      assert.equal(all.totals.cache_creation_1h, 60);
      assert.equal(all.totals.cache_creation_5m, 40);
      assert.equal(all.totals.cache_read, 3000);
      assert.equal(all.totals.output, 13);
      assert.equal(all.totals.weighted_input, 31 + 1.25 * 40 + 2 * 60 + 0.1 * 3000);
      assert.equal(all.sessions.find((s) => s.sessionId === "S1").firstRequestContext, 1110);
      assert.equal(all.fixed_overhead.median_first_request_context, (1110 + 1) / 2);
      assert.equal(all.tool_result_chars, 10);

      const tool = (r, name) => r.tools.find((t) => t.name === name);
      const read = tool(all, "cave__read");
      assert.deepEqual([read.group, read.calls, read.results, read.errors, read.total_chars, read.max_chars],
        ["cave", 1, 1, 0, 5, 5], "MCP name normalized + string content");
      const bash = tool(all, "Bash");
      assert.deepEqual([bash.group, bash.calls, bash.errors, bash.total_chars, bash.images],
        ["built-in", 1, 1, 3, 1], "array content + image + error");
      assert.equal(bash.share, 0.3);
      assert.equal(tool(all, "mcp__other__thing").group, "other-mcp");
      assert.deepEqual([all.groups.cave.calls, all.groups["built-in"].errors, all.groups["other-mcp"].total_chars], [1, 1, 2]);

      const alpha = runUsage("--project", "alpha");
      assert.equal(alpha.totals.requests, 2, "--project filters by cwd");
      assert.equal(tool(alpha, "mcp__other__thing"), undefined);
      const early = runUsage("--until", "2026-10-02");
      assert.equal(early.sessions.length, 1, "--until excludes later sessions");
      const late = runUsage("--since", "2026-10-02");
      assert.deepEqual(late.sessions.map((s) => s.sessionId), ["S2"], "--since excludes earlier sessions");
    } finally {
      rmSync(configDir, { recursive: true, force: true });
    }
    console.log("usage analyzer tests passed\n");
  }

  {
    console.log("Testing recoverable truncation handles:");
    const handleRe = /\[cave: full output \((\d+) lines\) at (\S+) — cave__read offset\/limit or cave__grep\]/;
    const prevDataDir = process.env.CAVE_TOOLS_DATA_DIR;
    const dataDir = mkdtempSync(join(tmpdir(), "cave-handles-"));
    process.env.CAVE_TOOLS_DATA_DIR = dataDir;
    const secret = "sk-live-handle123secret456";
    const noisy = `seq 1 1500; echo 'Authorization: Bearer ${secret}'`;
    const listFiles = (dir) => readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
      e.isDirectory() ? listFiles(join(dir, e.name)) : [join(dir, e.name)]);
    let server;
    try {
      // Nothing dropped: no handle.
      const small = await bashTool.handler({ command: "echo hi", description: "small" });
      assert.doesNotMatch(small.content[0].text, /\[cave: full output/);

      // Dropped + redaction on: handle present, archive is the redacted full output.
      const red = await bashTool.handler({ command: noisy, description: "noisy redacted" });
      const redHandle = handleRe.exec(red.content[0].text);
      assert.ok(redHandle, red.content[0].text.slice(-400));
      const redArchive = readFileSync(redHandle[2], "utf8");
      assert.ok(redArchive.length > red.content[0].text.length);
      assert.equal(redArchive.split("\n").length, Number(redHandle[1]));
      assert.match(redArchive, /\[REDACTED:Bearer token\]/);
      for (const file of listFiles(dataDir)) {
        assert.ok(!readFileSync(file).includes(secret), `secret on disk in ${file}`);
      }

      // Redaction off: archive equals the raw output; redacting it gives the redacted archive.
      const raw = await bashTool.handler({ command: noisy, description: "noisy raw", redact_secrets: false });
      const rawArchive = readFileSync(handleRe.exec(raw.content[0].text)[2], "utf8");
      assert.ok(rawArchive.includes(secret));
      assert.equal(redactSecrets(rawArchive), redArchive, "archive = full redacted output");
      assert.ok(rawArchive.includes("\n750\n"), "archive keeps dropped lines");

      // The handle path works with cave__read and cave__grep.
      const viaRead = await readTool.handler({ file_path: redHandle[2], offset: 1480, limit: 20, __sessionId: "handle-read" });
      assert.match(viaRead.content[0].text, /^1489$/m);
      const viaGrep = await grepTool.handler({ pattern: "^1499$", path: redHandle[2] });
      assert.match(viaGrep.content[0].text, /1499/);

      // grep: per-file cap drops lines; archive keeps all of them.
      const grepDir = mkdtempSync(join(tmpdir(), "cave-handles-grep-"));
      writeFileSync(join(grepDir, "many.txt"),
        Array.from({ length: 60 }, (_, i) => `needle number ${i} with some padding text`).join("\n"));
      const grepped = await grepTool.handler({ pattern: "needle", path: grepDir });
      const grepHandle = handleRe.exec(grepped.content[0].text);
      assert.ok(grepHandle, grepped.content[0].text.slice(-300));
      const grepArchive = readFileSync(grepHandle[2], "utf8");
      for (let i = 0; i < 60; i++) assert.match(grepArchive, new RegExp(`needle number ${i} `));
      rmSync(grepDir, { recursive: true, force: true });

      // webfetch: trimmed body archived, redacted on disk.
      server = createServer((_req, res) => {
        res.writeHead(200, { "content-type": "text/plain" });
        res.end(`Authorization: Bearer ${secret}\n` + Array.from({ length: 3000 }, (_, i) => `row ${i}`).join("\n"));
      });
      await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
      const fetched = await webfetchTool.handler({ url: `http://127.0.0.1:${server.address().port}/`, format: "text" });
      const fetchHandle = handleRe.exec(fetched.content[0].text);
      assert.ok(fetchHandle, fetched.content[0].text.slice(-300));
      const fetchArchive = readFileSync(fetchHandle[2], "utf8");
      assert.match(fetchArchive, /row 2999/);
      assert.ok(!fetchArchive.includes(secret), "webfetch archive redacted");
    } finally {
      server?.close();
      if (prevDataDir === undefined) delete process.env.CAVE_TOOLS_DATA_DIR;
      else process.env.CAVE_TOOLS_DATA_DIR = prevDataDir;
      rmSync(dataDir, { recursive: true, force: true });
    }
    console.log("handle tests passed\n");
  }

  {
    console.log("Testing cave__read re-read diffs:");
    assert.equal(isSubagentContext(), false, "diff tests need a non-subagent context");
    const dir = mkdtempSync(join(tmpdir(), "cave-diff-"));
    const file = join(dir, "notes.txt");
    const base = Array.from({ length: 100 }, (_, i) => `line ${i} of the original notes file`);
    const read = (args) => readTool.handler({ file_path: file, ...args });
    const text = (r) => r.content[0].text;
    const isDiff = (r) => text(r).startsWith("[cave: changed since last read in this session — unified diff; no prior copy? re-read with force=true]");
    try {
      writeFileSync(file, base.join("\n"));
      const first = await read({ __sessionId: "diff-a" });
      assert.ok(!isDiff(first), "first read is full");
      assert.equal(text(first), base.join("\n"));

      // Small change: diff.
      const v2 = [...base];
      v2[50] = "line 50 CHANGED";
      writeFileSync(file, v2.join("\n"));
      const small = await read({ __sessionId: "diff-a" });
      assert.ok(isDiff(small), text(small).slice(0, 200));
      assert.match(text(small), /^-line 50 of the original notes file$/m);
      assert.match(text(small), /^\+line 50 CHANGED$/m);
      assert.match(text(small), /^@@ -48,7 \+48,7 @@$/m);
      assert.ok(text(small).length < 0.4 * v2.join("\n").length);
      // Unchanged since the diff: stub.
      assert.match(text(await read({ __sessionId: "diff-a" })), /^<file unchanged/);

      // Other session, first read anywhere: full.
      const other = await read({ __sessionId: "diff-b" });
      assert.ok(!isDiff(other));
      assert.equal(text(other), v2.join("\n"));

      // Small change but line_numbers / force: full.
      const v3 = [...v2];
      v3[10] = "line 10 CHANGED";
      writeFileSync(file, v3.join("\n"));
      const numbered = await read({ __sessionId: "diff-b", line_numbers: true });
      assert.ok(!isDiff(numbered));
      assert.match(text(numbered), /cave_edit_meta/);
      const forced = await read({ __sessionId: "diff-a", force: true });
      assert.ok(!isDiff(forced));
      assert.equal(text(forced), v3.join("\n"));

      // Large change: full content.
      const v4 = base.map((l) => l.toUpperCase());
      writeFileSync(file, v4.join("\n"));
      const large = await read({ __sessionId: "diff-a" });
      assert.ok(!isDiff(large));
      assert.equal(text(large), v4.join("\n"));

      // Re-read right after cave__edit (recently-edited bypass) still diffs.
      await editTool.handler({ file_path: file, old_string: "LINE 70 OF THE ORIGINAL NOTES FILE", new_string: "edited seventy" });
      const afterEdit = await read({ __sessionId: "diff-a" });
      assert.ok(isDiff(afterEdit), text(afterEdit).slice(0, 200));
      assert.match(text(afterEdit), /^\+edited seventy$/m);

      // Files over the snapshot cap are never diffed.
      const big = join(dir, "big.txt");
      const bigLines = Array.from({ length: 150 }, (_, i) => `${i}`.padEnd(1900, "x"));
      writeFileSync(big, bigLines.join("\n"));
      assert.ok(statSync(big).size > MAX_SNAPSHOT_FILE_BYTES);
      await readTool.handler({ file_path: big, limit: 1000, __sessionId: "diff-a" });
      bigLines[3] = "changed";
      writeFileSync(big, bigLines.join("\n"));
      const bigAgain = await readTool.handler({ file_path: big, limit: 1000, __sessionId: "diff-a" });
      assert.ok(!isDiff(bigAgain));
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }

    // LRU total cap.
    resetSnapshots();
    const chunk = "y".repeat(200 * 1024);
    const count = Math.ceil(MAX_SNAPSHOT_TOTAL_BYTES / chunk.length) + 5;
    for (let i = 0; i < count; i++) rememberSnapshot("cap", `/f${i}`, `h${i}`, chunk);
    const stats = getSnapshotStats();
    assert.ok(stats.bytes <= MAX_SNAPSHOT_TOTAL_BYTES, `snapshot bytes ${stats.bytes}`);
    assert.ok(stats.files < count);
    assert.equal(getSnapshot("cap", "/f0"), undefined, "oldest evicted");
    assert.ok(getSnapshot("cap", `/f${count - 1}`), "newest kept");
    rememberSnapshot("cap", "/huge", "h", "z".repeat(MAX_SNAPSHOT_FILE_BYTES + 1));
    assert.equal(getSnapshot("cap", "/huge"), undefined, "per-file cap");
    resetSnapshots();
    assert.deepEqual(getSnapshotStats(), { files: 0, bytes: 0 });
    console.log("re-read diff tests passed\n");
  }

  {
    console.log("Testing char-cap budgets:");
    const b = getAllBudgets();
    assert.deepEqual(b.bash, { maxLines: 50, headLines: 15, tailLines: 35, maxChars: 6000 });
    assert.deepEqual(b.grep, { maxLines: 60, headLines: 30, tailLines: 30, maxChars: 6000 });
    assert.deepEqual(b.web, { maxLines: 120, headLines: 60, tailLines: 60, maxChars: 8000 });
    assert.equal(b.read.maxChars, undefined, "read has no char cap");

    // Long lines under the line budget: char cap trims at line boundaries.
    const longLines = Array.from({ length: 40 }, (_, i) => `L${String(i).padStart(2, "0")} ` + "x".repeat(396));
    const capped = applyBudget(longLines.join("\n"), "bash");
    assert.ok(capped.length <= 6000, `capped ${capped.length}`);
    const marker = /^\.\.\. \((\d+) chars truncated\) \.\.\.$/m.exec(capped);
    assert.ok(marker, "char-cap marker present");
    const kept = capped.split("\n").filter((l) => !l.startsWith("..."));
    assert.ok(kept.every((l) => longLines.includes(l)), "only whole lines kept");
    // Head/tail split follows bash's 15/35 line ratio.
    const headKept = capped.slice(0, marker.index).split("\n").filter(Boolean).length;
    const tailKept = capped.slice(marker.index + marker[0].length).split("\n").filter(Boolean).length;
    assert.ok(headKept >= 3 && headKept <= 5, `head ${headKept}`);
    assert.ok(tailKept >= 9 && tailKept <= 11, `tail ${tailKept}`);
    assert.equal(kept[0], longLines[0]);
    assert.equal(kept.at(-1), longLines.at(-1));
    // Dropped = original minus kept head and tail text (boundary newlines count as dropped).
    assert.equal(Number(marker[1]), longLines.join("\n").length - (kept.join("\n").length - 1),
      "marker counts dropped chars");

    // A single giant line is cut inside the line, head + tail kept.
    const giant = "A".repeat(1000) + "B".repeat(20000) + "C".repeat(1000);
    const giantCapped = applyBudget(giant, "bash");
    assert.ok(giantCapped.length <= 6000, `giant ${giantCapped.length}`);
    const giantParts = /^(A+B*)\n\.\.\. \((\d+) chars truncated\) \.\.\.\n(B*C+)$/.exec(giantCapped);
    assert.ok(giantParts, "giant line cut into head + marker + tail");
    // Head/tail of the cut line follow the 15/35 ratio too.
    assert.ok(giantParts[1].length < giantParts[3].length, "tail share larger than head share");
    assert.equal(giantParts[1].length + giantParts[3].length + Number(giantParts[2]), giant.length);

    // Line under the cap but over the head share is not cut inside.
    const mid = ["h".repeat(3000), ...Array.from({ length: 10 }, (_, i) => `t${i} ` + "y".repeat(600))].join("\n");
    assert.ok(!truncateChars(mid, 6000, 15, 35).includes("h".repeat(10)), "no partial line cut");

    // Web budget is separate from bash.
    const webText = Array.from({ length: 100 }, (_, i) => `web row ${i}`).join("\n");
    assert.equal(applyBudget(webText, "web"), webText, "100 lines fit web budget");
    assert.match(applyBudget(webText, "bash"), /lines truncated/, "100 lines exceed bash budget");

    // A char-cap-only trim still produces a handle.
    const prevDataDir = process.env.CAVE_TOOLS_DATA_DIR;
    const dataDir = mkdtempSync(join(tmpdir(), "cave-charcap-"));
    process.env.CAVE_TOOLS_DATA_DIR = dataDir;
    try {
      const wide = await bashTool.handler({
        command: `node -e 'for (let i = 0; i < 30; i++) console.log("row" + i + " " + "z".repeat(500))'`,
        description: "wide lines",
      });
      const out = wide.content[0].text;
      assert.match(out, /chars truncated/);
      assert.doesNotMatch(out, /lines truncated/);
      const handle = /full output \(\d+ lines\) at (\S+) —/.exec(out);
      assert.ok(handle, out.slice(-300));
      assert.match(readFileSync(handle[1], "utf8"), /^row29 z{500}$/m);
    } finally {
      if (prevDataDir === undefined) delete process.env.CAVE_TOOLS_DATA_DIR;
      else process.env.CAVE_TOOLS_DATA_DIR = prevDataDir;
      rmSync(dataDir, { recursive: true, force: true });
    }

    // configure: max_chars set, kept when omitted, 0 disables.
    await configureTool.handler({ action: "set_budget", tool_name: "web", max_lines: 120, head_lines: 60, tail_lines: 60, max_chars: 100 });
    assert.equal(getAllBudgets().web.maxChars, 100);
    await configureTool.handler({ action: "set_budget", tool_name: "web", max_lines: 120, head_lines: 60, tail_lines: 60, max_chars: 0 });
    assert.equal(getAllBudgets().web.maxChars, undefined);
    await configureTool.handler({ action: "set_budget", tool_name: "web", max_lines: 120, head_lines: 60, tail_lines: 60, max_chars: 8000 });
    assert.deepEqual(getAllBudgets().web, { maxLines: 120, headLines: 60, tailLines: 60, maxChars: 8000 });
    console.log("char-cap budget tests passed\n");
  }

  {
    console.log("Testing CLI tool commands:");
    const { parseToolArgs, UsageError, formatResult } = await import("./dist/cli-tools.js");
    const { loadTool, callTool, TOOL_NAMES } = await import("./dist/dispatch.js");
    assert.equal(TOOL_NAMES.length, 17);
    const noStdin = async () => { throw new Error("stdin not expected"); };
    const parse = async (tool, argv, stdin = noStdin) => parseToolArgs(await loadTool(`cave__${tool}`), argv, stdin);

    // Positionals fill required, then optional, in schema order; coercion by type.
    let p = await parse("read", ["/x/file.ts", "--line-numbers", "--limit", "20", "--force=false"]);
    assert.deepEqual(p.args, { file_path: "/x/file.ts", line_numbers: true, limit: 20, force: false });
    p = await parse("grep", ["needle", "src", "--ignoreCase", "--context=2"]);
    assert.deepEqual(p.args, { pattern: "needle", path: "src", ignoreCase: true, context: 2 });
    p = await parse("ls", ["."]);
    assert.deepEqual(p.args, { path: "." });
    p = await parse("edit", ["/f", "old", "new", "--replace_all", "true"]);
    assert.deepEqual(p.args, { file_path: "/f", old_string: "old", new_string: "new", replace_all: true });
    p = await parse("invalidate", ["/a", "/b"]);
    assert.deepEqual(p.args, { file_paths: ["/a", "/b"] });
    p = await parse("invalidate", ["--file_paths", '["/a"]']);
    assert.deepEqual(p.args, { file_paths: ["/a"] });
    p = await parse("edit", ["--edits", '[{"start_line":1}]', "--file-path", "/f"]);
    assert.deepEqual(p.args, { edits: [{ start_line: 1 }], file_path: "/f" });
    // bash: everything after the first positional is the command; description defaulted.
    p = await parse("bash", ["--timeout", "5000", "git", "log", "--oneline", "-n", "3"]);
    assert.deepEqual(p.args, { timeout: 5000, command: "git log --oneline -n 3", description: "cave-tools CLI" });
    p = await parse("read", ["/f", "--session", "abc"]);
    assert.equal(p.sessionId, "abc");
    // stdin "-".
    p = await parse("write", ["/f", "-"], async () => "from stdin");
    assert.deepEqual(p.args, { file_path: "/f", content: "from stdin" });
    p = await parse("compress", ["--text", "-"], async () => "piped");
    assert.equal(p.args.text, "piped");
    // Usage errors.
    for (const [tool, argv, msg] of [
      ["read", [], /missing required: file_path/],
      ["read", ["/f", "--bogus", "1"], /unknown flag --bogus/],
      ["read", ["/f", "--limit", "abc"], /expects a number/],
      ["read", ["/f", "--force=maybe"], /expects true or false/],
      ["read", ["/f", "--limit"], /needs a value/],
      ["status", ["extra"], /unexpected argument/],
      ["edit", ["--edits", "nope"], /JSON array/],
    ]) {
      await assert.rejects(parse(tool, argv), (e) => e instanceof UsageError && msg.test(e.message), `${tool} ${argv}`);
    }
    await assert.rejects(parse("edit", ["/f", "-", "-"], async () => "x"), /only one value/);
    assert.equal(formatResult({ content: [{ type: "image", data: Buffer.from("abcd").toString("base64"), mimeType: "image/png" }] }),
      "[image image/png 4 B]");

    // End to end through dist/cli.js.
    const dir = mkdtempSync(join(tmpdir(), "cave-cli-"));
    const cli = (args, input) => spawnSync(process.execPath, [join(process.cwd(), "dist", "cli.js"), ...args],
      { cwd: dir, input, encoding: "utf8", env: { ...process.env, CAVE_TOOLS_DATA_DIR: join(dir, "data") } });
    try {
      let r = cli(["write", join(dir, "a.txt"), "-"], "alpha\nbeta\n");
      assert.equal(r.status, 0, r.stderr);
      assert.equal(readFileSync(join(dir, "a.txt"), "utf8"), "alpha\nbeta\n");
      r = cli(["read", "a.txt"]);
      assert.equal(r.status, 0, r.stderr);
      assert.match(r.stdout, /^alpha\nbeta/);
      r = cli(["bash", "echo", "cli-bash-ok"]);
      assert.equal(r.status, 0, r.stderr);
      assert.match(r.stdout, /cli-bash-ok/);
      r = cli(["read", "missing.txt"]);
      assert.equal(r.status, 1, "tool error exits 1");
      assert.match(r.stderr, /File not found/);
      assert.equal(r.stdout, "");
      r = cli(["read", "--bogus"]);
      assert.equal(r.status, 2, "bad flag exits 2");
      assert.match(r.stderr, /usage: cave-tools read <file_path>/);
      r = cli(["no-such-tool"]);
      assert.equal(r.status, 2, "unknown tool exits 2");
      r = cli(["help", "grep"]);
      assert.equal(r.status, 0);
      assert.match(r.stdout, /usage: cave-tools grep <pattern>/);
      assert.match(r.stdout, /--limit\s+number — Max matches \(default 100\)/);
      r = cli(["grep", "--help"]);
      assert.match(r.stdout, /usage: cave-tools grep/);
      r = cli(["help"]);
      assert.match(r.stdout, /apply_patch/);
      assert.match(r.stdout, /cave-tools tool status/);
      r = cli(["tool", "status"]);
      assert.equal(r.status, 0, r.stderr);
      assert.match(r.stdout, /Cave Tools Status/);

      // Shared dispatch: MCP tools/call and CLI return the same text.
      const target = join(dir, "same.txt");
      writeFileSync(target, "same text\nfor both paths\n");
      const viaCli = cli(["read", target]).stdout;
      const viaMcp = await new Promise((resolve, reject) => {
        const child = spawn(process.execPath, [join(process.cwd(), "dist", "cli.js"), "mcp"],
          { cwd: dir, stdio: ["pipe", "pipe", "ignore"], env: { ...process.env, CAVE_TOOLS_DATA_DIR: join(dir, "data") } });
        let buf = "";
        const timer = setTimeout(() => { child.kill(); reject(new Error("mcp timeout")); }, 15000);
        child.stdout.on("data", (chunk) => {
          buf += chunk;
          for (const line of buf.split("\n").slice(0, -1)) {
            const msg = JSON.parse(line);
            if (msg.id === 1) {
              child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
              child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/call",
                params: { name: "cave__read", arguments: { file_path: target } } }) + "\n");
            } else if (msg.id === 2) {
              clearTimeout(timer);
              child.kill();
              resolve(msg.result.content[0].text);
            }
          }
          buf = buf.slice(buf.lastIndexOf("\n") + 1);
        });
        child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize",
          params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "t", version: "0" } } }) + "\n");
      });
      // The CLI adds a trailing newline only when the text lacks one.
      assert.equal(viaCli, viaMcp.endsWith("\n") ? viaMcp : `${viaMcp}\n`, "CLI and MCP share dispatch output");
      const direct = await callTool("cave__read", { file_path: target }, { sessionId: "dispatch-direct" });
      assert.equal(direct.content[0].text, viaMcp);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
    console.log("CLI tool command tests passed\n");
  }

  console.log("All tests passed!");
}

test().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
