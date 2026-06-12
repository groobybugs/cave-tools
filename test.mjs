import { bashTool } from "./dist/tools/bash.js";
import { compressTool } from "./dist/tools/compress.js";
import { editTool } from "./dist/tools/edit.js";
import { findTool } from "./dist/tools/find.js";
import { grepTool } from "./dist/tools/grep.js";
import { lsTool } from "./dist/tools/ls.js";
import { statusTool } from "./dist/tools/status.js";
import { writeTool } from "./dist/tools/write.js";
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

  // Test compress
  console.log("2. Testing cave__compress:");
  const compressResult = await compressTool.handler({
    text: "Line 1\n\n\n\nLine 2\n\n\nLine 3",
    tool_name: "bash",
  });
  console.log(compressResult.content[0].text);
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
