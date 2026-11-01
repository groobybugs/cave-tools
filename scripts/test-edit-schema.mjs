// Model-facing edit contract: validate the advertised schema, then check IO.
// Run after pnpm build. No model/network calls; all state stays in scratch.
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AjvJsonSchemaValidator } from "@modelcontextprotocol/sdk/validation/ajv";

const base = process.platform === "linux" ? "/tmp/opencode" : tmpdir();
mkdirSync(base, { recursive: true });
const root = mkdtempSync(join(base, "cave-edit-schema-"));
process.env.CAVE_TOOLS_DATA_DIR = join(root, "data");
process.env.CLAUDE_CONFIG_DIR = join(root, "claude");
process.env.CAVE_TOOLS_MODE = "enforce";
let checks = 0;

try {
  const { editTool } = await import("../dist/tools/edit.js");
  const validate = new AjvJsonSchemaValidator().getValidator(editTool.inputSchema);
  const a = join(root, "a.txt");
  const b = join(root, "b.txt");
  const str = { old_string: "alpha", new_string: "ALPHA" };
  const range = { start_line: 1, end_line: 1, content: "ALPHA" };
  const del = { start_line: 1, end_line: 1, delete: true };
  const move = { start_line: 1, end_line: 1, insert_before: 3 };
  const cases = [
    ["single string", { file_path: a, ...str }, true],
    ["explicit string deletion", { file_path: a, ...str, new_string: "" }, true],
    ["range", { file_path: a, ...range }, true],
    ["empty range content", { file_path: a, ...range, content: "" }, true],
    ["insertion", { file_path: a, ...range, end_line: 0 }, true],
    ["delete", { file_path: a, ...del }, true],
    ["delete with content", { file_path: a, ...del, content: "ignored" }, true],
    ["move", { file_path: a, ...move }, true],
    ["batch default path", { file_path: a, edits: [str, { old_string: "beta", new_string: "BETA" }] }, true],
    ["batch default plus override", { file_path: a, edits: [str, { file_path: b, ...str }] }, true],
    ["batch empty item path inherits default", { file_path: a, edits: [{ file_path: "", ...str }] }, true],
    ["pathless multi-file batch", { edits: [{ file_path: a, ...str }, { file_path: b, ...str }] }, true],
    ["empty default with explicit item path", { file_path: "", edits: [{ file_path: a, ...str }] }, true],
    ["pathless batch modes", { edits: [{ file_path: a, ...range }, { file_path: b, ...move }] }, true],
    ["missing everything", {}, false],
    ["missing single path", str, false],
    ["empty single path", { file_path: "", ...str }, false],
    ["missing mode", { file_path: a }, false],
    ["missing new string", { file_path: a, old_string: "alpha" }, false],
    ["null new string", { file_path: a, ...str, new_string: null }, false],
    ["empty search", { file_path: a, old_string: "", new_string: "x" }, false],
    ["native key names", { path: a, oldString: "alpha", newString: "ALPHA" }, false],
    ["range missing end", { file_path: a, start_line: 1, content: "ALPHA" }, false],
    ["range missing body", { file_path: a, start_line: 1, end_line: 1 }, false],
    ["false delete without body", { file_path: a, ...del, delete: false }, false],
    ["move missing end", { file_path: a, start_line: 1, insert_before: 2 }, false],
    ["fractional lines", { file_path: a, ...range, end_line: 1.5 }, false],
    ["empty batch", { file_path: a, edits: [] }, false],
    ["batch missing target", { edits: [str] }, false],
    ["batch empty item target", { edits: [{ file_path: "", ...str }] }, false],
    ["batch partially missing targets", { edits: [{ file_path: a, ...str }, str] }, false],
    ["batch missing item replacement", { file_path: a, edits: [{ old_string: "alpha" }] }, false],
    ["batch missing item mode", { edits: [{ file_path: a }] }, false],
    ["batch item null", { file_path: a, edits: [null] }, false],
    ["wrong batch type", { file_path: a, edits: "not an array" }, false],
    ["mixed single and batch", { file_path: a, ...str, edits: [str] }, false],
    ...Object.entries({
      old_string: "alpha", new_string: "ALPHA", replace_all: true,
      start_line: 1, end_line: 1, content: "ALPHA", delete: true,
      insert_before: 3, expected_range_checksum: "1-1:deadbeef",
    }).map(([key, value]) => [
      `batch top-level operation ${key}`, { file_path: a, [key]: value, edits: [str] }, false,
    ]),
  ];
  // The root stays a plain object: strict providers (xAI/grok) reject a root
  // anyOf/oneOf. Shape rules that lived there (path, mode, single vs batch)
  // are enforced by the handler; the schema still rejects these type errors.
  const schemaRejects = new Set([
    "null new string", "fractional lines", "empty batch", "batch missing item replacement",
    "batch missing item mode", "batch item null", "wrong batch type",
  ]);
  const assertPlainObjectRoot = (schema, label) => {
    assert.equal(schema.type, "object", `${label}: root type`);
    for (const key of ["anyOf", "oneOf", "allOf", "not"]) {
      assert.equal(key in schema, false, `${label}: root ${key}`);
    }
    checks++;
  };
  assertPlainObjectRoot(editTool.inputSchema, "local schema");
  for (const [name, input, valid] of cases) {
    assert.equal(validate(input).valid, valid || !schemaRejects.has(name), name);
    checks++;
  }

  // Check the actual MCP tools/list wire schema, not only the local definition.
  const { Client } = await import("@modelcontextprotocol/sdk/client/index.js");
  const { StdioClientTransport } = await import("@modelcontextprotocol/sdk/client/stdio.js");
  const { fileURLToPath } = await import("node:url");
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: [fileURLToPath(new URL("../dist/cli.js", import.meta.url)), "mcp"],
    env: { ...process.env, CAVE_TOOLS_DAEMON: "0", CAVE_TOOLS_PROFILE: "full" },
    stderr: "pipe",
  });
  const client = new Client({ name: "edit-schema-regression", version: "1" });
  try {
    await client.connect(transport);
    const listed = await client.listTools();
    const advertised = listed.tools.find((tool) => tool.name === "cave__edit");
    assert.ok(advertised, "MCP advertises cave__edit");
    assertPlainObjectRoot(advertised.inputSchema, "MCP schema");
    const wireValidate = new AjvJsonSchemaValidator().getValidator(advertised.inputSchema);
    for (const [name, input, valid] of cases) {
      assert.equal(wireValidate(input).valid, valid || !schemaRejects.has(name), `MCP schema: ${name}`);
      checks++;
    }
    checks++;
  } finally {
    await client.close();
    await transport.close();
  }


  const body = "alpha\nbeta\ngamma\n";
  const reset = () => { writeFileSync(a, body); writeFileSync(b, body); };
  const text = (result) => result.content.map((part) => part.text ?? "").join("\n");
  for (const [name, input, valid] of cases) {
    if (valid) continue;
    reset();
    const result = await editTool.handler(input);
    assert.equal(result.isError, true, `handler rejects ${name}: ${text(result)}`);
    assert.equal(readFileSync(a, "utf8"), body, `${name} left first target unchanged`);
    assert.equal(readFileSync(b, "utf8"), body, `${name} left second target unchanged`);
    checks += 3;
  }

  const upper = "ALPHA\nbeta\ngamma\n";
  const moved = "beta\nalpha\ngamma\n";
  const expected = new Map([
    ["single string", [upper, body]],
    ["explicit string deletion", ["\nbeta\ngamma\n", body]],
    ["range", [upper, body]],
    ["empty range content", ["beta\ngamma\n", body]],
    ["insertion", ["ALPHA\nalpha\nbeta\ngamma\n", body]],
    ["delete", ["beta\ngamma\n", body]],
    ["delete with content", ["beta\ngamma\n", body]],
    ["move", [moved, body]],
    ["batch default path", ["ALPHA\nBETA\ngamma\n", body]],
    ["batch default plus override", [upper, upper]],
    ["batch empty item path inherits default", [upper, body]],
    ["pathless multi-file batch", [upper, upper]],
    ["empty default with explicit item path", [upper, body]],
    ["pathless batch modes", [upper, moved]],
  ]);
  for (const [name, input, valid] of cases) {
    if (!valid) continue;
    assert.ok(expected.has(name), `${name} needs an IO expectation`);
    reset();
    const result = await editTool.handler(input);
    assert.notEqual(result.isError, true, `${name}: ${text(result)}`);
    const [wantA, wantB] = expected.get(name);
    assert.equal(readFileSync(a, "utf8"), wantA, `${name}: first target`);
    assert.equal(readFileSync(b, "utf8"), wantB, `${name}: second target`);
    checks += 4;
  }
  console.log(`edit schema: ${checks} checks passed`);
} finally {
  rmSync(root, { recursive: true, force: true });
}
