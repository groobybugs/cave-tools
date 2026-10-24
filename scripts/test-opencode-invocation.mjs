#!/usr/bin/env node
// Dedicated tests for the OpenCode plugin invocation path.
//
// Covers V2 per-request native-tool filtering, strict missing-target errors,
// compact host validation errors, and V1 compatibility. Isolated from live
// user config/state; no install, build, or network access.
//
// Run: node scripts/test-opencode-invocation.mjs

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "..");
const pluginPath = path.join(repo, "src", "plugins", "opencode", "plugin.js");
const tmpBase = path.join(tmpdir(), "opencode");
mkdirSync(tmpBase, { recursive: true, mode: 0o700 });
const scratch = mkdtempSync(path.join(tmpBase, "cave-opencode-invocation-"));

let caseId = 0;
let pass = 0;
function ok(name) {
  pass += 1;
  console.log(`PASS ${name}`);
}

function definition() {
  return { description: "test definition", input: { type: "object" } };
}

async function loadPlugin(mode, tag) {
  const root = path.join(scratch, `${caseId++}-${tag}`);
  const xdg = path.join(root, "xdg");
  const claude = path.join(root, "claude");
  mkdirSync(xdg, { recursive: true, mode: 0o700 });
  mkdirSync(claude, { recursive: true, mode: 0o700 });
  process.env.XDG_CONFIG_HOME = xdg;
  process.env.CLAUDE_CONFIG_DIR = claude;
  process.env.CAVE_TOOLS_MODE = mode;
  const url = pathToFileURL(pluginPath).href + "?case=" + encodeURIComponent(tag + "-" + caseId);
  const mod = await import(url);
  return {
    mod,
    flagPath: path.join(xdg, "opencode", ".cave-tools-active"),
  };
}

function fakeCtx() {
  const hooks = new Map();
  return {
    hooks,
    ctx: {
      session: {
        hook: async (name, callback) => {
          hooks.set("session:" + name, callback);
          return { dispose() {} };
        },
      },
      tool: {
        hook: async (name, callback) => {
          hooks.set("tool:" + name, callback);
          return { dispose() {} };
        },
      },
    },
  };
}

async function setupV2(mode, tag) {
  const loaded = await loadPlugin(mode, tag);
  const fake = fakeCtx();
  await loaded.mod.default.setup(fake.ctx);
  return { ...loaded, hooks: fake.hooks };
}

function hook(hooks, name) {
  const callback = hooks.get(name);
  assert.equal(typeof callback, "function", `missing hook ${name}`);
  return callback;
}

function textOf(system) {
  return system.map((part) => (typeof part === "string" ? part : part.text || "")).join("\n");
}

try {
  // 1. enforce: exact native ids are hidden only with their exact Cave match.
  {
    const { hooks } = await setupV2("enforce", "filter-exact");
    const tools = {
      edit: definition(),
      write: definition(),
      patch: definition(),
      read: definition(),
      "cave-tools_cave__edit": definition(),
      "cave-tools_cave__write": definition(),
      "cave-tools_cave__apply_patch": definition(),
      "cave-tools_cave__read": definition(),
      evil_cave__edit: definition(),
      "cave-tools_cave__edit_extra": definition(),
      "notcave-tools_cave__write": definition(),
      "cave-tools_cave__write_extra": definition(),
    };
    await hook(hooks, "session:context")({ system: [], tools });
    assert.equal("edit" in tools, false);
    assert.equal("write" in tools, false);
    assert.equal("patch" in tools, false);
    assert.equal("read" in tools, false);
    assert.equal("cave-tools_cave__edit" in tools, true);
    assert.equal("cave-tools_cave__write" in tools, true);
    assert.equal("cave-tools_cave__apply_patch" in tools, true);
    assert.equal("cave-tools_cave__read" in tools, true);
    assert.equal("evil_cave__edit" in tools, true);
    assert.equal("cave-tools_cave__edit_extra" in tools, true);
    assert.equal("notcave-tools_cave__write" in tools, true);
    assert.equal("cave-tools_cave__write_extra" in tools, true);
    ok("enforce hides exact native ids only when exact Cave replacements exist");
  }

  // 2. Partial replacements keep native fallbacks.
  {
    const { hooks } = await setupV2("enforce", "filter-partial");
    const tools = {
      edit: definition(),
      write: definition(),
      patch: definition(),
      "cave__edit": definition(),
      "cave-tools_cave__apply_patch": definition(),
    };
    await hook(hooks, "session:context")({ system: [], tools });
    assert.equal("edit" in tools, false);
    assert.equal("write" in tools, true, "write has no Cave replacement");
    assert.equal("patch" in tools, false);
    assert.equal("cave__edit" in tools, true);
    assert.equal("cave-tools_cave__apply_patch" in tools, true);
    ok("partial replacements preserve native fallbacks");
  }

  // Exact legacy MCP prefixes and patch aliases are recognized, not substrings.
  {
    const { hooks } = await setupV2("enforce", "legacy-prefix");
    const tools = { edit: definition(), patch: definition(), apply_patch: definition(), write: definition(),
      "mcp__cave-tools__cave__edit": definition(), "mcp__cave-tools__cave__apply_patch": definition() };
    await hook(hooks, "session:context")({ system: [], tools });
    assert.equal("edit" in tools, false);
    assert.equal("patch" in tools, false);
    assert.equal("apply_patch" in tools, false);
    assert.equal("write" in tools, true);
    const unavailable = { edit: definition(), "cave-tools_cave__edit": null };
    await hook(hooks, "session:context")({ system: [], tools: unavailable });
    assert.equal("edit" in unavailable, true);
    const strict = await setupV2("strict", "legacy-patch-fallback");
    await hook(strict.hooks, "tool:execute.before")({ tool: "apply_patch", input: { patchText: "*** Begin Patch\n*** End Patch" } });
    ok("qualified patch aliases filter safely and preserve patch-shaped fallback");
  }


  // 3. No replacements: leave every native tool in place.
  {
    const { hooks } = await setupV2("enforce", "filter-absent");
    const tools = {
      edit: definition(),
      write: definition(),
      patch: definition(),
      "cave-tools_cave__read": definition(),
    };
    await hook(hooks, "session:context")({ system: [], tools });
    assert.equal("edit" in tools, true);
    assert.equal("write" in tools, true);
    assert.equal("patch" in tools, true);
    ok("absent replacements keep native fallbacks");
  }

  // 4. Mode toggles: hint/off never filter; enforce/strict do.
  {
    const loaded = await setupV2("enforce", "mode-toggles");
    const context = hook(loaded.hooks, "session:context");
    const native = () => ({ edit: definition(), "cave__edit": definition() });

    let tools = native();
    await context({ system: [], tools });
    assert.equal("edit" in tools, false, "enforce filters");

    writeFileSync(loaded.flagPath, "hint");
    tools = native();
    await context({ system: [], tools });
    assert.equal("edit" in tools, true, "hint does not filter");

    writeFileSync(loaded.flagPath, "off");
    tools = native();
    await context({ system: [], tools });
    assert.equal("edit" in tools, true, "off does not filter");

    writeFileSync(loaded.flagPath, "strict");
    tools = native();
    await context({ system: [], tools });
    assert.equal("edit" in tools, false, "strict filters");
    ok("mode toggles gate filtering (hint/off unchanged)");
  }

  // 5. Distinct requests do not leak deletions into later tool lists.
  {
    const { hooks } = await setupV2("enforce", "distinct-requests");
    const context = hook(hooks, "session:context");
    const first = { edit: definition(), "cave__edit": definition() };
    await context({ system: [], tools: first });
    assert.equal("edit" in first, false);

    const second = { edit: definition() };
    await context({ system: [], tools: second });
    assert.equal("edit" in second, true, "second request keeps native fallback");

    const third = { edit: definition(), evil_cave__edit: definition() };
    await context({ system: [], tools: third });
    assert.equal("edit" in third, true, "similarly named tool is not a replacement");
    ok("request-local filtering does not mutate shared or later tool lists");
  }

  // 6. Prompt guidance names the explicit string-edit contract.
  {
    const { hooks } = await setupV2("enforce", "prompt");
    const event = { system: [], tools: {} };
    await hook(hooks, "session:context")(event);
    const prompt = textOf(event.system);
    assert.match(prompt, /file_path/);
    assert.match(prompt, /old_string/);
    assert.match(prompt, /new_string/);
    assert.match(prompt, /never infer target/i);
    assert.match(prompt, /Native shell is the default/);
    assert.match(prompt, /RTK pre-rewrite or guaranteed failure compression/);
    assert.doesNotMatch(prompt, /cave__bash also fine/);
    ok("prompt shows explicit file_path + old_string/new_string");
  }

  // 7. Strict missing-target errors are actionable and value-free.
  {
    const { hooks } = await setupV2("strict", "strict-missing-target");
    const before = hook(hooks, "tool:execute.before");

    let editError = null;
    try {
      await before({
        tool: "edit",
        input: { oldString: "SECRET_OLD_VALUE", newString: "SECRET_NEW_VALUE" },
      });
    } catch (error) {
      editError = error;
    }
    assert.ok(editError instanceof Error);
    assert.match(editError.message, /edit target missing/i);
    assert.match(editError.message, /file_path/);
    assert.match(editError.message, /cave__edit/);
    assert.doesNotMatch(editError.message, /SECRET_OLD_VALUE|SECRET_NEW_VALUE/);

    let writeError = null;
    try {
      await before({ tool: "write", input: { content: "SECRET_CONTENT_VALUE" } });
    } catch (error) {
      writeError = error;
    }
    assert.ok(writeError instanceof Error);
    assert.match(writeError.message, /write target missing/i);
    assert.match(writeError.message, /cave__write/);
    assert.doesNotMatch(writeError.message, /SECRET_CONTENT_VALUE/);

    // Actual native patch uses patchText, not a top-level path. Strict mode
    // must not expand the edit guard into a misleading patch error.
    await before({ tool: "patch", input: { patchText: "*** Begin Patch\n*** End Patch" } });

    // Enforce must not blindly block a native edit that still carries a target.
    const enforced = await setupV2("enforce", "enforce-native-edit");
    await hook(enforced.hooks, "tool:execute.before")({
      tool: "edit",
      input: { path: "/tmp/target.txt", oldString: "a", newString: "b" },
    });
    ok("strict missing-target errors are actionable, value-free, and patch-safe");
  }

  // Explicit off must disable guards and feedback, not fall back to enforce.
  {
    const { hooks } = await setupV2("enforce", "explicit-off");
    await hook(hooks, "session:prompt")({ text: "/cave-tools off" });
    await hook(hooks, "tool:execute.before")({ tool: "read", input: { path: "/unused" } });
    await hook(hooks, "tool:execute.before")({ tool: "edit", input: {} });
    const event = { system: [], tools: { edit: definition(), read: definition(), cave__edit: definition(), cave__read: definition() } };
    await hook(hooks, "session:context")(event);
    assert.equal("edit" in event.tools, true);
    assert.equal(event.system.length, 0);
    const error = new Error('Invalid arguments for tool "edit":\n- path: Missing key\n\nArguments provided:\n{}\n\nUpdate the arguments and call the tool again.');
    const original = error.message;
    await hook(hooks, "tool:execute.after")({ tool: "edit", status: "error", error });
    assert.equal(error.message, original, "off preserves native feedback");
    await hook(hooks, "session:prompt")({ text: "/cave-tools enforce" });
    await hook(hooks, "session:context")(event);
    assert.equal("edit" in event.tools, false);
    await assert.rejects(async () => hook(hooks, "tool:execute.before")({ tool: "read", input: {} }), /Use cave__read/);
    ok("explicit off and reactivation preserve complete mode semantics");
  }


  // 8. Compact only recognized host input-validation errors.
  {
    const { hooks } = await setupV2("enforce", "compact-errors");
    const after = hook(hooks, "tool:execute.after");
    class ToolError extends Error {
      constructor(message) {
        super(message);
        this.name = "Tool.Error";
        this.type = "tool";
      }
    }
    const sentinel = "sk-ant-SENTINEL-0123456789abcdef";
    const huge = "x".repeat(100000);
    const payload = JSON.stringify({
      path: "/tmp/" + sentinel,
      oldString: huge,
      newString: "y",
    });
    const originalMessage =
      'Invalid arguments for tool "edit":\n- path: Missing key\n\nArguments provided:\n' +
      payload +
      "\n\nUpdate the arguments and call the tool again.";
    const original = new ToolError(originalMessage);
    const input = { path: "/tmp/" + sentinel, oldString: huge };
    const event = { status: "error", tool: "edit", error: original, input };
    await after(event);

    assert.equal(event.error, original, "error identity preserved");
    assert.equal(event.error.constructor, ToolError, "error class preserved");
    assert.equal(event.error.name, "Tool.Error");
    assert.equal(event.error.type, "tool");
    assert.equal(event.status, "error", "event status preserved");
    assert.equal(event.input, input, "input object untouched");
    assert.ok(event.error.message.startsWith('Invalid arguments for tool "edit":'));
    assert.match(event.error.message, /path: Missing key/);
    assert.match(event.error.message, /cave__edit/);
    assert.match(event.error.message, /file_path/);
    assert.doesNotMatch(event.error.message, /Arguments provided/);
    assert.doesNotMatch(event.error.message, new RegExp(sentinel));
    assert.doesNotMatch(event.error.message, /x{100}/);
    assert.ok(event.error.message.length < 600, "compact error stays bounded");

    const genericMessage =
      'Invalid arguments for tool "edit":\n- oldString: Expected string, received ' +
      sentinel +
      "\n\nArguments provided:\n" +
      JSON.stringify({ oldString: sentinel }) +
      "\n\nUpdate the arguments and call the tool again.";
    const generic = new ToolError(genericMessage);
    await after({ status: "error", tool: "edit", error: generic });
    assert.match(generic.message, /oldString: Expected string/);
    assert.doesNotMatch(generic.message, new RegExp(sentinel));

    const writeMessage =
      'Invalid arguments for tool "write":\n- path: Missing key\n\nArguments provided:\n' +
      JSON.stringify({ path: sentinel }) +
      "\n\nUpdate the arguments and call the tool again.";
    const writeError = new ToolError(writeMessage);
    await after({ status: "error", tool: "write", error: writeError });
    assert.match(writeError.message, /cave__write/);
    assert.match(writeError.message, /file_path/);
    assert.doesNotMatch(writeError.message, new RegExp(sentinel));

    const denied = new Error("permission denied: " + sentinel);
    const deniedMessage = denied.message;
    await after({ status: "error", tool: "edit", error: denied });
    assert.equal(denied.message, deniedMessage, "non-schema failure passes through");

    const completedError = new ToolError(originalMessage);
    await after({ status: "completed", tool: "edit", error: completedError });
    assert.equal(completedError.message, originalMessage, "completed events untouched");
    ok("compact validation errors preserve identity/status and never echo payloads");
  }

  // Unknown argument keys can themselves contain secrets; syntax is not an allowlist.
  {
    const { hooks } = await setupV2("enforce", "secret-field-names");
    const after = hook(hooks, "tool:execute.after");
    const secret = "PRIVATE_FIELD_NAME_938462";
    const body = `- ${secret}: Expected string, received ${secret}\n` +
      `- edits[1].${secret}: Unrecognized key\n` +
      `- edits[2].old_string: Expected string, received required ${secret}`;
    const original = new Error('Invalid arguments for tool "edit":\n' + body +
      '\n\nArguments provided:\n' + "x".repeat(5000) +
      '\n\nUpdate the arguments and call the tool again.');
    await after({ status: "error", tool: "edit", error: original });
    assert.doesNotMatch(original.message, new RegExp(secret));
    assert.match(original.message, /argument: Expected string/);
    assert.match(original.message, /edits\[2\]\.old_string: Expected string/);
    assert.doesNotMatch(original.message, /old_string: Missing key/);
    const frozen = Object.freeze(new Error('Invalid arguments for tool "edit":\n- path: Missing key\n\nArguments provided:\n{}\n\nUpdate the arguments and call the tool again.'));
    const frozenMessage = frozen.message;
    await after({ status: "error", tool: "edit", error: frozen });
    assert.equal(frozen.message, frozenMessage, "read-only errors retain their original feedback");
    assert.ok(Object.isFrozen(frozen), "read-only foreign errors are never rewrapped");
    ok("compact errors allowlist field names and anchor safe reasons");
  }


  // Hide native read only when its replacement is offered. Image passthrough stays for visible read.
  {
    const readDefinition = () => ({
      description: "Read a file from the local filesystem",
      input: { type: "object", properties: { path: { type: "string", minLength: 1 } } },
    });
    const { hooks } = await setupV2("enforce", "read-hide");
    const original = readDefinition();
    const snapshot = structuredClone(original);
    const tools = { read: original, "mcp__cave-tools__cave__read": definition(), grep: definition() };
    const event = { system: [], tools };
    await hook(hooks, "session:context")(event);
    assert.equal("read" in tools, false);
    assert.equal("grep" in tools, true);
    assert.equal("mcp__cave-tools__cave__read" in tools, true);
    assert.deepEqual(original, snapshot, "hiding read does not mutate the shared definition");
    assert.match(textOf(event.system), /cave__read, not read/);
    const before = hook(hooks, "tool:execute.before");
    const early = await setupV2("enforce", "read-before-replacement");
    await hook(early.hooks, "session:context")({ system: [], tools: { read: definition(), edit: definition() } });
    await hook(early.hooks, "tool:execute.before")({ tool: "read", input: { path: "/tmp/notes.txt" } });
    await before({ tool: "read", input: { path: "/tmp/diagram.PNG" } });
    await before({ tool: "read", input: { file_path: "/tmp/spec.pdf" } });
    await before({ tool: "read", input: { path: "/tmp/icon.svg" } });
    await assert.rejects(async () => before({ tool: "read", input: { path: "/tmp/notes.txt" } }), /cave__read/);
    await assert.rejects(async () => before({ tool: "read", input: { path: "/tmp/diagram.png.txt" } }), /cave__read/);

    const absent = readDefinition();
    const absentTools = { read: absent, "cave-tools_cave__read": null };
    await hook(hooks, "session:context")({ system: [], tools: absentTools });
    assert.equal(absentTools.read, absent);
    assert.equal(absentTools.read.description, "Read a file from the local filesystem");

    const hint = await setupV2("hint", "read-hide-hint");
    const hintRead = readDefinition();
    const hintTools = { read: hintRead, cave__read: definition() };
    await hook(hint.hooks, "session:context")({ system: [], tools: hintTools });
    assert.equal(hintTools.read, hintRead);
    await hook(hint.hooks, "tool:execute.before")({ tool: "read", input: { path: "/tmp/notes.txt" } });
    ok("native read hides only beside cave__read; visible read still passes images");
  }

  // Registry removal, not only the per-request tool map. Execution uses the registry.
  {
    const removed = [];
    const editor = {
      list: () => [
        { id: "read" }, { id: "edit" }, { id: "write" }, { id: "grep" },
        { id: "cave-tools_cave__read" }, { id: "cave__edit" },
      ],
      remove: (name) => removed.push(name),
    };
    const loaded = await loadPlugin("enforce", "registry-hide");
    const fake = fakeCtx();
    fake.ctx.tool.transform = async (callback) => {
      callback(editor);
      return { dispose() {} };
    };
    await loaded.mod.default.setup(fake.ctx);
    assert.deepEqual(removed.slice().sort(), ["edit", "read"]);
    const hintRemoved = [];
    const hint = await loadPlugin("hint", "registry-hint");
    const hintFake = fakeCtx();
    hintFake.ctx.tool.transform = async (callback) => {
      callback({ list: editor.list, remove: (name) => hintRemoved.push(name) });
      return { dispose() {} };
    };
    await hint.mod.default.setup(hintFake.ctx);
    assert.deepEqual(hintRemoved, []);
    ok("registry transform removes natives only when their Cave replacement is listed");
  }

  // 9. V1 compatibility remains; filtering is V2-only.
  {
    const strict = await loadPlugin("strict", "v1-strict");
    const strictHooks = await strict.mod.CaveToolsPlugin({});
    assert.equal(typeof strictHooks["tool.execute.before"], "function");
    assert.equal(strictHooks["session.context"], undefined);
    let missing = null;
    try {
      await strictHooks["tool.execute.before"](
        { tool: "edit" },
        { args: { oldString: "SECRET_V1_OLD", newString: "SECRET_V1_NEW" } },
      );
    } catch (error) {
      missing = error;
    }
    assert.ok(missing instanceof Error);
    assert.match(missing.message, /edit target missing/i);
    assert.match(missing.message, /cave__edit/);
    assert.doesNotMatch(missing.message, /SECRET_V1_OLD|SECRET_V1_NEW/);

    const enforce = await loadPlugin("enforce", "v1-enforce");
    const enforceHooks = await enforce.mod.CaveToolsPlugin({});
    let readError = null;
    try {
      await enforceHooks["tool.execute.before"](
        { tool: "read" },
        { args: { file_path: "/tmp/target.txt" } },
      );
    } catch (error) {
      readError = error;
    }
    assert.ok(readError instanceof Error, "V1 enforce still blocks read");
    await enforceHooks["tool.execute.before"](
      { tool: "edit" },
      { args: { path: "/tmp/target.txt", oldString: "a", newString: "b" } },
    );
    await enforceHooks["tool.execute.before"](
      { tool: "cave-tools_cave__read" },
      { args: { file_path: "/tmp/target.txt" } },
    );
    ok("V1 compatibility remains; no V1 filtering claim");
  }

  console.log(`\n${pass} OpenCode invocation tests passed`);
} finally {
  try {
    rmSync(scratch, { recursive: true, force: true });
  } catch (error) {
    console.error(`cleanup failed: ${error.message}`);
  }
}
