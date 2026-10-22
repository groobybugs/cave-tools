// Focused regression tests for cave__edit / cave__read reliability fixes.
//
// Imports the built output (dist/), so run `pnpm build` first. Against a stale
// pre-fix dist the new_string, BOM/CRLF hash, replace_all, and atomicity cases
// fail by design; the script reports the failing assertion.
//
// Fixtures live under /tmp/opencode and all stats/jobs go to an isolated
// CAVE_TOOLS_DATA_DIR so a developer's real cache is never touched.

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const TMP_ROOT = "/tmp/opencode";
mkdirSync(TMP_ROOT, { recursive: true });
const WORK_DIR = mkdtempSync(join(TMP_ROOT, "cave-edit-reliability-"));
const DATA_DIR = mkdtempSync(join(TMP_ROOT, "cave-edit-reliability-data-"));
process.env.CAVE_TOOLS_DATA_DIR = DATA_DIR;
// Redirect the live-user registry/db root too: read-registry and storage fall
// back to ~/.claude when CLAUDE_CONFIG_DIR is unset. WORK_DIR-owned, removed
// with it; never touches live entries.
const CLAUDE_DIR = join(WORK_DIR, "claude-config");
mkdirSync(CLAUDE_DIR, { recursive: true });
process.env.CLAUDE_CONFIG_DIR = CLAUDE_DIR;

const { editTool } = await import("../dist/tools/edit.js");
const { readTool } = await import("../dist/tools/read.js");

const text = (result) => result.content.map((part) => part.text ?? "").join("\n");
const rawHash = (filePath) =>
  createHash("sha256").update(readFileSync(filePath)).digest("hex");
const readText = (filePath) => readFileSync(filePath, "utf-8");
const write = (filePath, content) => {
  mkdirSync(dirname(filePath), { recursive: true });
  writeFileSync(filePath, content, "utf-8");
  return filePath;
};
const fixture = (name, content) => write(join(WORK_DIR, name), content);

let checks = 0;
function ok(condition, message) {
  assert.ok(condition, message);
  checks++;
}
function equal(actual, expected, message) {
  assert.equal(actual, expected, message);
  checks++;
}

async function rejectsUnchanged(filePath, args, pattern) {
  const before = readText(filePath);
  const result = await editTool.handler(args);
  equal(result.isError, true, `expected rejection for ${filePath}`);
  if (pattern) assert.match(text(result), pattern);
  equal(readText(filePath), before, `file must stay unchanged: ${filePath}`);
  return text(result);
}

try {
  console.log("1. string-mode new_string validation");

  const missingNew = fixture("missing-new.txt", "alpha\n");
  await rejectsUnchanged(
    missingNew,
    { file_path: missingNew, old_string: "alpha" },
    /new_string is required/i,
  );

  const nullNew = fixture("null-new.txt", "alpha\n");
  await rejectsUnchanged(
    nullNew,
    { file_path: nullNew, old_string: "alpha", new_string: null },
    /new_string is required/i,
  );

  const numberNew = fixture("number-new.txt", "alpha\n");
  await rejectsUnchanged(
    numberNew,
    { file_path: numberNew, old_string: "alpha", new_string: 5 },
    /new_string is required/i,
  );

  const emptyDelete = fixture("empty-delete.txt", "alpha\nbeta\n");
  const emptyDeleteResult = await editTool.handler({
    file_path: emptyDelete,
    old_string: "alpha\n",
    new_string: "",
  });
  equal(emptyDeleteResult.isError, undefined, text(emptyDeleteResult));
  equal(readText(emptyDelete), "beta\n", "explicit empty string deletes");

  const batchMissingNew = fixture("batch-missing-new.txt", "alpha\n");
  const batchMissingNewResult = await editTool.handler({
    file_path: batchMissingNew,
    edits: [{ old_string: "alpha" }],
  });
  equal(batchMissingNewResult.isError, true, "batch item without new_string must fail");
  assert.match(text(batchMissingNewResult), /new_string is required/i);
  equal(readText(batchMissingNew), "alpha\n", "batch validation must precede writes");

  const batchNumberNew = fixture("batch-number-new.txt", "alpha\n");
  const batchNumberNewResult = await editTool.handler({
    file_path: batchNumberNew,
    edits: [{ old_string: "alpha", new_string: 5 }],
  });
  equal(batchNumberNewResult.isError, true, "batch item with non-string new_string must fail");
  assert.match(text(batchNumberNewResult), /new_string is required/i);
  equal(readText(batchNumberNew), "alpha\n", "malformed batch item must not write");

  const nullItem = fixture("null-item.txt", "alpha\n");
  const nullItemResult = await editTool.handler({
    file_path: nullItem,
    edits: [null],
  });
  equal(nullItemResult.isError, true, "null batch item must fail cleanly");
  assert.match(text(nullItemResult), /edits\[0\]: must be an edit object/);
  equal(readText(nullItem), "alpha\n", "null batch item must not write");

  const editsWrongType = fixture("edits-wrong-type.txt", "alpha\n");
  const editsWrongTypeResult = await editTool.handler({
    file_path: editsWrongType,
    edits: "not-an-array",
  });
  equal(editsWrongTypeResult.isError, true, "non-array edits must fail cleanly");
  assert.match(text(editsWrongTypeResult), /edits must be an array/);
  equal(readText(editsWrongType), "alpha\n", "edits type rejection must not write");

  console.log("2. LF / BOM / CRLF hash and checksum agreement");

  const variants = [
    { label: "lf", eol: "\n", bom: false },
    { label: "lf-bom", eol: "\n", bom: true },
    { label: "crlf", eol: "\r\n", bom: false },
    { label: "crlf-bom", eol: "\r\n", bom: true },
  ];
  for (const variant of variants) {
    const filePath = join(WORK_DIR, `matrix-${variant.label}.txt`);
    const body = `alpha${variant.eol}beta${variant.eol}`;
    write(filePath, `${variant.bom ? "\uFEFF" : ""}${body}`);

    const expectedHash = rawHash(filePath);
    const readResult = await readTool.handler({
      file_path: filePath,
      line_numbers: true,
      force: true,
    });
    equal(readResult.isError, undefined, `${variant.label}: read failed: ${text(readResult)}`);
    const numbered = text(readResult);
    const footerHash = /file_hash: ([0-9a-f]+)/.exec(numbered)?.[1];
    const footerChecksum = /range_checksum: \d+-\d+:([0-9a-f]+)/.exec(numbered)?.[1];
    ok(footerHash, `${variant.label}: missing file_hash footer`);
    equal(
      footerHash,
      expectedHash.slice(0, footerHash.length),
      `${variant.label}: footer hash must cover original bytes (BOM included)`,
    );
    ok(footerChecksum, `${variant.label}: missing range_checksum footer`);
    if (variant.bom) {
      ok(!numbered.includes("\uFEFF"), `${variant.label}: BOM must stay out of visible text`);
    }

    const editResult = await editTool.handler({
      file_path: filePath,
      start_line: 1,
      end_line: 3,
      content: "ALPHA\nBETA\n",
      expected_hash: footerHash,
      expected_range_checksum: footerChecksum,
    });
    equal(editResult.isError, undefined, `${variant.label}: edit failed: ${text(editResult)}`);
    const after = readText(filePath);
    equal(
      after,
      `${variant.bom ? "\uFEFF" : ""}ALPHA${variant.eol}BETA${variant.eol}`,
      `${variant.label}: content/EOL/BOM must be preserved`,
    );
  }

  console.log("3. pure-string batch replace_all and overlap");

  const replaceAllFile = fixture("replace-all.txt", "foo foo\n");
  const replaceAllResult = await editTool.handler({
    file_path: replaceAllFile,
    edits: [{ old_string: "foo", new_string: "bar", replace_all: true }],
  });
  equal(replaceAllResult.isError, undefined, text(replaceAllResult));
  equal(readText(replaceAllFile), "bar bar\n", "batch replace_all must replace every match");

  const overlapFile = fixture("overlap-pure.txt", "foo bar baz\n");
  await rejectsUnchanged(
    overlapFile,
    {
      file_path: overlapFile,
      edits: [
        { old_string: "foo bar", new_string: "x" },
        { old_string: "bar baz", new_string: "y" },
      ],
    },
    /overlap/,
  );

  const expansionOverlapFile = fixture("overlap-expansion.txt", "foo foo\n");
  await rejectsUnchanged(
    expansionOverlapFile,
    {
      file_path: expansionOverlapFile,
      edits: [
        { old_string: "foo", new_string: "x", replace_all: true },
        { old_string: "foo", new_string: "y" },
      ],
    },
    /overlap|not unique/,
  );

  console.log("4. multi-file batch: validate/load/compute before writes");

  const atomicA = fixture("atomic-a.txt", "alpha\n");
  const atomicB = join(WORK_DIR, "atomic-b-missing.txt");
  const missingFileResult = await editTool.handler({
    edits: [
      { file_path: atomicA, old_string: "alpha", new_string: "ALPHA" },
      { file_path: atomicB, old_string: "beta", new_string: "BETA" },
    ],
  });
  equal(missingFileResult.isError, true, "missing second file must fail");
  assert.match(text(missingFileResult), /Cannot read file/);
  equal(readText(atomicA), "alpha\n", "fileA must stay unchanged when fileB cannot load");

  const globalIndexA = fixture("global-index-a.txt", "one\ntwo\n");
  const globalIndexB = fixture("global-index-b.txt", "three\n");
  const globalIndexResult = await editTool.handler({
    edits: [
      { file_path: globalIndexA, old_string: "one", new_string: "ONE" },
      { file_path: globalIndexA, old_string: "two", new_string: "TWO" },
      { file_path: globalIndexB, old_string: "three", new_string: "THREE" },
      { file_path: globalIndexB, old_string: "missing", new_string: "X" },
    ],
  });
  equal(globalIndexResult.isError, true, "failing later edit must abort the batch");
  assert.match(text(globalIndexResult), /edits\[3\]/, "error must keep original global index");
  equal(readText(globalIndexA), "one\ntwo\n", "fileA must stay unchanged when fileB edit fails");
  equal(readText(globalIndexB), "three\n", "fileB must stay unchanged");

  const hashA = fixture("multifile-hash-a.txt", "aaa\n");
  const hashB = fixture("multifile-hash-b.txt", "bbb\n");
  const topLevelHashResult = await editTool.handler({
    expected_hash: rawHash(hashA),
    edits: [
      { file_path: hashA, old_string: "aaa", new_string: "AAA" },
      { file_path: hashB, old_string: "bbb", new_string: "BBB" },
    ],
  });
  equal(topLevelHashResult.isError, true, "top-level hash must not be ignored for multi-file");
  assert.match(text(topLevelHashResult), /single-file/);
  assert.match(text(topLevelHashResult), /each edit item|per-item/);
  equal(readText(hashA), "aaa\n", "multi-file top-level hash rejection must not write");
  equal(readText(hashB), "bbb\n", "multi-file top-level hash rejection must not write");

  const strictFile = fixture("strict-batch.txt", "x\ny\n");
  const strictHash = rawHash(strictFile);
  const previousMode = process.env.CAVE_TOOLS_MODE;
  process.env.CAVE_TOOLS_MODE = "strict";
  let strictResult;
  try {
    strictResult = await editTool.handler({
      file_path: strictFile,
      expected_hash: strictHash,
      edits: [{ start_line: 1, end_line: 1, content: "X" }],
    });
  } finally {
    if (previousMode === undefined) delete process.env.CAVE_TOOLS_MODE;
    else process.env.CAVE_TOOLS_MODE = previousMode;
  }
  equal(strictResult.isError, undefined, `strict batch top-level hash failed: ${text(strictResult)}`);
  equal(readText(strictFile), "X\ny\n", "strict single-file batch should accept top-level hash");

  console.log("5. single-edit success/delete and batch empty-string delete");

  const singleFile = fixture("single-success.txt", "alpha\nbeta\n");
  const singleResult = await editTool.handler({
    file_path: singleFile,
    old_string: "beta",
    new_string: "BETA",
  });
  equal(singleResult.isError, undefined, text(singleResult));
  equal(readText(singleFile), "alpha\nBETA\n", "single string edit still works");

  const singleDeleteFile = fixture("single-range-delete.txt", "alpha\nbeta\n");
  const singleDeleteResult = await editTool.handler({
    file_path: singleDeleteFile,
    start_line: 1,
    end_line: 1,
    delete: true,
  });
  equal(singleDeleteResult.isError, undefined, text(singleDeleteResult));
  equal(readText(singleDeleteFile), "beta\n", "single range delete still works");

  const batchDeleteFile = fixture("batch-empty-delete.txt", "alpha\nbeta\n");
  const batchDeleteResult = await editTool.handler({
    file_path: batchDeleteFile,
    edits: [
      { old_string: "alpha\n", new_string: "" },
      { old_string: "beta\n", new_string: "" },
    ],
  });
  equal(batchDeleteResult.isError, undefined, text(batchDeleteResult));
  equal(readText(batchDeleteFile), "", "explicit empty batch replacements delete");

  if (process.platform === "linux" && typeof process.getuid === "function" && process.getuid() !== 0) {
    console.log("6. write-time failure reporting (partial batch)");

    const firstWrite = fixture("write-fail-first.txt", "first\n");
    const blockedWrite = fixture("write-fail-blocked.txt", "second\n");
    chmodSync(blockedWrite, 0o444);
    let writeFailResult;
    try {
      writeFailResult = await editTool.handler({
        edits: [
          { file_path: firstWrite, old_string: "first", new_string: "FIRST" },
          { file_path: blockedWrite, old_string: "second", new_string: "SECOND" },
        ],
      });
    } finally {
      chmodSync(blockedWrite, 0o644);
    }
    equal(writeFailResult.isError, true, "read-only second file must fail the write");
    const writeFailText = text(writeFailResult);
    assert.match(writeFailText, /Files written before failure: .*write-fail-first\.txt/);
    assert.match(writeFailText, /Failed file \(may be partially modified\): .*write-fail-blocked\.txt/);
    assert.match(writeFailText, /Files not attempted: \(none\)/);
    equal(readText(firstWrite), "FIRST\n", "earlier file may be written before a write-time failure");
    equal(readText(blockedWrite), "second\n", "blocked file must stay unchanged");
  } else {
    console.log("6. write-time failure reporting skipped (root/unsupported platform)");
  }

  console.log(`\nPASS: ${checks} assertions`);
} finally {
  rmSync(WORK_DIR, { recursive: true, force: true });
  rmSync(DATA_DIR, { recursive: true, force: true });
}
