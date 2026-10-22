// Handler-level edit integration tests (no repo dist — fresh temp build).
//
// Covers the cave__edit handler in src/tools/edit.ts after wiring the shared
// matcher (computeEditResult): V2 string vectors at handler level (unicode,
// trailing-whitespace, CRLF/LF adaptation, endingsOnly, replace_all with
// multiple raw variants, unchanged/already-applied precedence, fuzzy +
// CAVE_TOOLS_FUZZY_EDIT flag), single/pure-batch/mixed-batch IO, BOM
// preservation, range EOF + mixed EOL, arg-contract validation without file
// changes, insert-overlap / multi-move / move+range guards, concurrent
// same-file disjoint edits (outer lock when file-mutation exports it —
// skipped with a clear note otherwise, never failing), alias-path rejection,
// and compatibility ports of scripts/test-edit-reliability.mjs.
//
// Run: node scripts/test-edit-handler-v2.mjs  (from the repo root)
//   1. typechecks + compiles the handler closure (edit.ts + deps) into a
//      unique mkdtemp dir under /tmp/opencode — real type errors fail the
//      run, no tolerance hacks, no repo dist build (baseline suite may be
//      running on repo dist).
//   2. exercises the compiled handler; fixtures live in the same scratch dir,
//      stats go to an isolated CAVE_TOOLS_DATA_DIR; scratch removed in
//      `finally` (only this run's dir).

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "..");
const opencodeTmp = path.join(tmpdir(), "opencode");
mkdirSync(opencodeTmp, { recursive: true });
const scratch = mkdtempSync(path.join(opencodeTmp, "cave-edit-handler-"));
const outDir = path.join(scratch, "dist");
const fixDir = path.join(scratch, "fixtures");
mkdirSync(outDir, { recursive: true });
mkdirSync(fixDir, { recursive: true });
// Resolve repo dependencies (better-sqlite3 etc.) from the temp build without
// installing anything: a symlink is removed with the scratch dir.
symlinkSync(path.join(repo, "node_modules"), path.join(scratch, "node_modules"));
const dataDir = mkdtempSync(path.join(opencodeTmp, "cave-edit-handler-data-"));
process.env.CAVE_TOOLS_DATA_DIR = dataDir;
// Redirect the live-user registry/db root too: read-registry and storage fall
// back to ~/.claude when CLAUDE_CONFIG_DIR is unset. Scratch-owned, removed
// with the scratch dir; never touches live entries.
const claudeDir = path.join(scratch, "claude");
mkdirSync(claudeDir, { recursive: true });
process.env.CLAUDE_CONFIG_DIR = claudeDir;

let exitCode = 0;
let skipped = 0;
try {
  // ── Step 1: compile the handler closure fresh ──────────────────────────
  try {
    execFileSync(
      "pnpm",
      [
        "exec", "tsc",
        "--outDir", outDir,
        "--rootDir", "src",
        "--target", "es2022",
        "--module", "nodenext",
        "--moduleResolution", "nodenext",
        "--skipLibCheck",
        "--declaration", "false",
        "--sourceMap", "false",
        "src/tools/edit.ts",
      ],
      { cwd: repo, stdio: "inherit" },
    );
  } catch (e) {
    console.error("COMPILE_FAIL: handler closure has type errors (see tsc output above).");
    exitCode = 1;
    throw e;
  }

  const em = await import(pathToFileURL(path.join(outDir, "tools", "edit.js")).href);
  const fm = await import(pathToFileURL(path.join(outDir, "runtime", "file-mutation.js")).href);
  const { editTool } = em;
  const fmLockPresent = typeof fm.withFileLocks === "function";

  // ── tiny harness ──────────────────────────────────────────────────────
  let pass = 0;
  let fail = 0;
  const failures = [];
  function ok(name, cond, detail = "") {
    if (cond) {
      pass++;
    } else {
      fail++;
      failures.push(`${name} ${detail}`);
      console.error(`FAIL ${name} ${detail}`);
    }
  }
  function eq(name, actual, expected) {
    ok(name, actual === expected, `want ${JSON.stringify(expected)} got ${JSON.stringify(actual)}`);
  }
  ok("H0 withFileLocks exported", fmLockPresent, "file-mutation.js must export withFileLocks");
  const text = (r) => r.content.map((p) => p.text ?? "").join("\n");
  const read = (p) => readFileSync(p, "utf8");
  let n = 0;
  const fixture = (name, content) => {
    const p = path.join(fixDir, `${n++}-${name}`);
    writeFileSync(p, content);
    return p;
  };
  async function rejectsUnchanged(filePath, args, pattern, label) {
    const before = read(filePath);
    const r = await editTool.handler(args);
    ok(`${label} isError`, r.isError === true, text(r));
    if (pattern) ok(`${label} pattern`, pattern.test(text(r)), text(r));
    eq(`${label} unchanged`, read(filePath), before);
    return r;
  }

  // ── A. single string edits: V2 vectors at handler level ───────────────
  {
    // Unicode tier matches curly source with an ASCII query; the new text
    // itself comes from new_string (only line endings are inherited).
    const f = fixture("unicode.txt", "it’s a “test” — done\n");
    const r = await editTool.handler({
      file_path: f, old_string: "it's a \"test\" - done", new_string: "it's a \"test\" - changed",
    });
    ok("A1 unicode ok", r.isError === undefined, text(r));
    eq("A1 unicode content", read(f), "it's a \"test\" - changed\n");
  }
  {
    const f = fixture("trail.txt", "alpha   \nbeta\t\n");
    const r = await editTool.handler({
      file_path: f, old_string: "alpha\nbeta", new_string: "alpha\nGAMMA",
    });
    ok("A2 trailing ok", r.isError === undefined, text(r));
    eq("A2 trailing content", read(f), "alpha\nGAMMA\n");
  }
  {
    const f = fixture("crlf.txt", "line1\r\nline2\r\nline3\r\n");
    const r = await editTool.handler({
      file_path: f, old_string: "line1\nline2", new_string: "changed1\nchanged2",
    });
    ok("A3 crlf ok", r.isError === undefined, text(r));
    eq("A3 crlf content", read(f), "changed1\r\nchanged2\r\nline3\r\n");
  }
  {
    const f = fixture("lf.txt", "line1\nline2\nline3\n");
    const r = await editTool.handler({
      file_path: f, old_string: "line1\r\nline2", new_string: "a\r\nb",
    });
    ok("A4 lf ok", r.isError === undefined, text(r));
    eq("A4 lf content", read(f), "a\nb\nline3\n");
  }
  {
    const f = fixture("variants.txt", "\u2018a\u2019 and \u201Aa\u201B\n");
    const r = await editTool.handler({
      file_path: f, old_string: "'a'", new_string: "'b'", replace_all: true,
    });
    ok("A5 variants ok", r.isError === undefined, text(r));
    eq("A5 variants content", read(f), "'b' and 'b'\n");
    ok("A5 variants count", /\(2 replacements\)/.test(text(r)), text(r));
  }
  {
    // Computed-unchanged coincides with new_string-present at handler level:
    // already-applied takes precedence (same as before).
    const f = fixture("unchanged.txt", "\u2018hi\u2019\n");
    const r = await editTool.handler({
      file_path: f, old_string: "'hi'", new_string: "\u2018hi\u2019",
    });
    ok("A6 already-applied precedence", r.isError === true, text(r));
    ok("A6 already-applied msg", /already applied/.test(text(r)), text(r));
    eq("A6 unchanged", read(f), "\u2018hi\u2019\n");
  }
  {
    const f = fixture("endings.txt", "a\nb\n");
    const r = await editTool.handler({
      file_path: f, old_string: "a\nb", new_string: "a\r\nb",
    });
    ok("A7 endingsOnly ok", r.isError === undefined, text(r));
    eq("A7 endingsOnly bytes", read(f), "a\r\nb\n");
  }
  {
    const f = fixture("fuzzy.txt", "const   x   =   1;\n");
    const saved = process.env.CAVE_TOOLS_FUZZY_EDIT;
    delete process.env.CAVE_TOOLS_FUZZY_EDIT;
    const r = await editTool.handler({ file_path: f, old_string: "const x = 1;", new_string: "const x = 2;" });
    ok("A8 fuzzy ok", r.isError === undefined, text(r));
    ok("A8 fuzzy content", read(f).includes("const x = 2;"), read(f));
    process.env.CAVE_TOOLS_FUZZY_EDIT = "0";
    const off = await editTool.handler({ file_path: f, old_string: "const x = 2;", new_string: "const x = 3;" });
    // "const x = 2;" now exists exactly, so the strict path matches exactly (flag only gates fuzzy).
    ok("A8 strict exact still works", off.isError === undefined, text(off));
    const offMiss = await editTool.handler({ file_path: f, old_string: "const   x   =   3;", new_string: "QQQZZZ" });
    ok("A8 strict miss", offMiss.isError === true && /Could not find oldString/.test(text(offMiss)), text(offMiss));
    if (saved === undefined) delete process.env.CAVE_TOOLS_FUZZY_EDIT;
    else process.env.CAVE_TOOLS_FUZZY_EDIT = saved;
  }
  {
    const f = fixture("nonunique.txt", "beta beta\n");
    const r = await editTool.handler({ file_path: f, old_string: "beta", new_string: "gamma" });
    ok("A9 nonUnique", r.isError === true, text(r));
    ok("A9 nonUnique msg", /not unique/.test(text(r)), text(r));
    eq("A9 unchanged", read(f), "beta beta\n");
  }
  {
    const f = fixture("hint.txt", "function myFunction() {\n  return 42;\n}\n");
    const r = await editTool.handler({ file_path: f, old_string: "function notHere() {", new_string: "x" });
    ok("A10 not-found", r.isError === true, text(r));
    ok("A10 hint", /Closest match/.test(text(r)), text(r));
    eq("A10 unchanged", read(f), "function myFunction() {\n  return 42;\n}\n");
  }
  {
    const f = fixture("crlfmulti.txt", "a\r\nb\r\n");
    const r = await editTool.handler({ file_path: f, old_string: "b", new_string: "x\ny" });
    ok("A11 crlf multiline ok", r.isError === undefined, text(r));
    eq("A11 crlf multiline content", read(f), "a\r\nx\r\ny\r\n");
    ok("A11 no lone LF", !/(?<!\r)\n/.test(read(f)), JSON.stringify(read(f)));
  }
  {
    const f = fixture("mixedmatch.txt", "a\r\nb\nc\r\n");
    const r = await editTool.handler({
      file_path: f, old_string: "a\r\nb\nc", new_string: "a\nB\nc\nd",
    });
    ok("A12 mixed region ok", r.isError === undefined, text(r));
    eq("A12 mixed region content", read(f), "a\r\nB\nc\r\nd\r\n");
  }

  // ── B. pure-string batches ────────────────────────────────────────────
  {
    const f = fixture("batch.txt", "alpha beta gamma delta\n");
    const r = await editTool.handler({
      file_path: f,
      edits: [
        { old_string: "alpha", new_string: "one" },
        { old_string: "gamma", new_string: "three" },
      ],
    });
    ok("B1 batch ok", r.isError === undefined, text(r));
    eq("B1 batch content", read(f), "one beta three delta\n");
  }
  {
    const f = fixture("batchra.txt", "foo foo\n");
    const r = await editTool.handler({
      file_path: f, edits: [{ old_string: "foo", new_string: "bar", replace_all: true }],
    });
    ok("B2 batch replace_all ok", r.isError === undefined, text(r));
    eq("B2 batch replace_all content", read(f), "bar bar\n");
  }
  {
    const f = fixture("batchoverlap.txt", "foo bar baz\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { old_string: "foo bar", new_string: "x" },
        { old_string: "bar baz", new_string: "y" },
      ],
    }, /overlap/, "B3 overlap");
  }
  {
    const f = fixture("batchexpand.txt", "foo foo\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { old_string: "foo", new_string: "x", replace_all: true },
        { old_string: "foo", new_string: "y" },
      ],
    }, /not unique/, "B4 expansion nonUnique");
  }
  {
    // Per-span (not first-literal) replacement in pure batches: the second
    // raw variant changes too.
    const f = fixture("batchvariants.txt", "\u2018a\u2019 and \u201Aa\u201B\n");
    const r = await editTool.handler({
      file_path: f,
      edits: [{ old_string: "x", new_string: "1" }, { old_string: "'a'", new_string: "'b'", replace_all: true }],
    });
    // First item misses -> whole batch fails before any write.
    ok("B5 batch miss aborts", r.isError === true, text(r));
    eq("B5 batch miss unchanged", read(f), "\u2018a\u2019 and \u201Aa\u201B\n");
    const r2 = await editTool.handler({
      file_path: f,
      edits: [{ old_string: "'a'", new_string: "'b'", replace_all: true }],
    });
    ok("B5 variants ok", r2.isError === undefined, text(r2));
    eq("B5 variants content", read(f), "'b' and 'b'\n");
  }

  // ── C. mixed range+string batches ─────────────────────────────────────
  {
    const f = fixture("mixed.txt", "one\ntwo\nthree\n");
    const r = await editTool.handler({
      file_path: f,
      edits: [
        { start_line: 2, end_line: 2, content: "TWO" },
        { old_string: "three", new_string: "THREE" },
      ],
    });
    ok("C1 mixed ok", r.isError === undefined, text(r));
    eq("C1 mixed content", read(f), "one\nTWO\nTHREE\n");
  }
  {
    // Item order does not matter: strings match content, ranges coordinates.
    const f = fixture("mixedrev.txt", "one\ntwo\nthree\n");
    const r = await editTool.handler({
      file_path: f,
      edits: [
        { old_string: "three", new_string: "THREE" },
        { start_line: 2, end_line: 2, content: "TWO" },
      ],
    });
    ok("C2 mixed reversed ok", r.isError === undefined, text(r));
    eq("C2 mixed reversed content", read(f), "one\nTWO\nTHREE\n");
  }
  {
    const f = fixture("mixednoop.txt", "one\ntwo\nthree\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { start_line: 2, end_line: 2, content: "TWO" },
        { old_string: "missing", new_string: "X" },
      ],
    }, /Could not find/, "C3 mixed miss");
  }
  {
    const f = fixture("mixedidentical.txt", "one\ntwo\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { start_line: 1, end_line: 1, content: "ONE" },
        { old_string: "two", new_string: "two" },
      ],
    }, /identical/, "C4 mixed identical");
  }

  // ── D. BOM preservation ───────────────────────────────────────────────
  {
    const f = fixture("bom.txt", "﻿alpha\nbeta\n");
    const r = await editTool.handler({ file_path: f, old_string: "beta", new_string: "BETA" });
    ok("D1 bom string ok", r.isError === undefined, text(r));
    eq("D1 bom string content", read(f), "﻿alpha\nBETA\n");
  }
  {
    const f = fixture("bomrange.txt", "﻿alpha\nbeta\n");
    const r = await editTool.handler({ file_path: f, start_line: 1, end_line: 1, content: "ALPHA" });
    ok("D2 bom range ok", r.isError === undefined, text(r));
    eq("D2 bom range content", read(f), "﻿ALPHA\nbeta\n");
  }
  {
    const f = fixture("bomcrlf.txt", "﻿a\r\nb\r\n");
    const r = await editTool.handler({ file_path: f, start_line: 2, end_line: 2, content: "B" });
    ok("D3 bom+crlf ok", r.isError === undefined, text(r));
    eq("D3 bom+crlf content", read(f), "﻿a\r\nB\r\n");
  }

  // ── E. range EOF + mixed EOL via handler ──────────────────────────────
  {
    const f = fixture("append.txt", "a\nb\n");
    const r = await editTool.handler({ file_path: f, start_line: 4, end_line: 3, content: "c" });
    ok("E1 append model+1 ok", r.isError === undefined, text(r));
    eq("E1 append model+1 content", read(f), "a\nb\nc\n");
  }
  {
    const f = fixture("appendnonl.txt", "a\nb");
    const r = await editTool.handler({ file_path: f, start_line: 3, end_line: 2, content: "c" });
    ok("E2 append no-trailing-NL ok", r.isError === undefined, text(r));
    eq("E2 append no-trailing-NL content", read(f), "a\nb\nc");
  }
  {
    const f = fixture("mixrange.txt", "1\r\n2\n3\r\n4\n");
    const r = await editTool.handler({ file_path: f, start_line: 2, end_line: 2, content: "TWO" });
    ok("E3 mixed range ok", r.isError === undefined, text(r));
    eq("E3 mixed range content", read(f), "1\r\nTWO\n3\r\n4\n");
  }
  {
    const f = fixture("mixmove.txt", "1\r\n2\n3\r\n4\n");
    const r = await editTool.handler({ file_path: f, start_line: 1, end_line: 1, insert_before: 5 });
    ok("E4 mixed move ok", r.isError === undefined, text(r));
    eq("E4 mixed move content", read(f), "2\n3\r\n4\n1\r\n");
  }

  // ── F. arg-contract validation (files must stay unchanged) ─────────────
  {
    const f = fixture("invalid.txt", "alpha\n");
    for (const [label, args] of [
      ["F1 undefined", undefined],
      ["F2 null", null],
      ["F3 string", "x"],
      ["F4 array", []],
    ]) {
      const before = read(f);
      let r;
      try {
        r = await editTool.handler(args);
      } catch (e) {
        ok(`${label} no-throw`, false, `threw: ${e}`);
        continue;
      }
      ok(`${label} isError`, r.isError === true, text(r));
      eq(`${label} unchanged`, read(f), before);
    }
    await rejectsUnchanged(f, { file_path: f, old_string: 5, new_string: "x" }, /old_string must be a string/, "F5 old_string type");
    await rejectsUnchanged(f, { file_path: 5, old_string: "alpha", new_string: "x" }, /file_path must be a string/, "F6 file_path type");
    await rejectsUnchanged(f, { file_path: f, old_string: "alpha", new_string: "x", replace_all: 1 }, /replace_all must be a boolean/, "F7 replace_all type");
    await rejectsUnchanged(f, { file_path: f, start_line: 1, end_line: 1, content: 5 }, /content must be a string/, "F8 content type");
    await rejectsUnchanged(f, { file_path: f, start_line: "1", end_line: 1, content: "x" }, /start_line must be a number/, "F9 start_line type");
    await rejectsUnchanged(f, {
      file_path: f, edits: [{ old_string: "alpha", new_string: 5 }],
    }, /new_string is required/, "F10 batch new_string type");
    await rejectsUnchanged(f, {
      file_path: f, edits: [null],
    }, /must be an edit object/, "F11 batch null item");
    await rejectsUnchanged(f, {
      file_path: f, edits: [{ old_string: 5, new_string: "x" }],
    }, /old_string must be a string/, "F12 batch old_string type");
    await rejectsUnchanged(f, {
      file_path: f, edits: [{ start_line: "1", end_line: 1, content: "x" }],
    }, /start_line must be a number/, "F13 batch start_line type");
    await rejectsUnchanged(f, { file_path: f, edits: [] }, /edits array is empty/, "F14 empty edits");
  }

  // ── G. batch safety guards ────────────────────────────────────────────
  {
    const f = fixture("guard.txt", "1\n2\n3\n4\n5\n6\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { start_line: 3, end_line: 6, content: "X" },
        { start_line: 5, end_line: 4, content: "y" },
      ],
    }, /lies inside/, "G1 insert inside range");
  }
  {
    const f = fixture("guardstart.txt", "1\n2\n3\n4\n5\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { start_line: 3, end_line: 5, content: "X" },
        { start_line: 3, end_line: 2, content: "y" },
      ],
    }, /lies inside/, "G2 insert at start boundary");
  }
  {
    // Insert right after a replaced range is safe (applies first, high→low).
    const f = fixture("guardafter.txt", "1\n2\n3\n4\n5\n");
    const r = await editTool.handler({
      file_path: f,
      edits: [
        { start_line: 3, end_line: 4, content: "X" },
        { start_line: 5, end_line: 4, content: "y" },
      ],
    });
    ok("G3 insert after range ok", r.isError === undefined, text(r));
    eq("G3 insert after range content", read(f), "1\n2\nX\ny\n5\n");
  }
  {
    // Same-point inserts are deterministic (input order, high→low stable):
    // second insert lands before the first.
    const f = fixture("guardsame.txt", "1\n2\n3\n");
    const r = await editTool.handler({
      file_path: f,
      edits: [
        { start_line: 2, end_line: 1, content: "A" },
        { start_line: 2, end_line: 1, content: "B" },
      ],
    });
    ok("G4 same-point inserts ok", r.isError === undefined, text(r));
    eq("G4 same-point inserts content", read(f), "1\nB\nA\n2\n3\n");
  }
  {
    const f = fixture("guardmove.txt", "1\n2\n3\n4\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { start_line: 1, end_line: 2, insert_before: 5 },
        { start_line: 3, end_line: 3, insert_before: 1 },
      ],
    }, /separate calls/, "G5 multi-move rejected");
  }
  {
    const f = fixture("guardmix.txt", "1\n2\n3\n4\n");
    await rejectsUnchanged(f, {
      file_path: f,
      edits: [
        { start_line: 1, end_line: 2, insert_before: 5 },
        { start_line: 3, end_line: 3, content: "X" },
      ],
    }, /cannot mix/, "G6 move+range rejected");
  }

  // ── H. concurrency, aliases, canonical locks ──────────────────────────
  {
    // Outer lock is mandatory: concurrent disjoint edits on the same file
    // must serialize and both succeed.
    const f = fixture("conc.txt", "a1\nb2\nc3\nd4\n");
    const [r1, r2] = await Promise.all([
      editTool.handler({ file_path: f, old_string: "a1", new_string: "A1" }),
      editTool.handler({ file_path: f, old_string: "d4", new_string: "D4" }),
    ]);
    ok("H1 both succeed", r1.isError === undefined && r2.isError === undefined, `${text(r1)} / ${text(r2)}`);
    eq("H1 both applied", read(f), "A1\nb2\nc3\nD4\n");
  }
  {
    const f = fixture("alias.txt", "one\ntwo\n");
    // Deliberately un-normalized spelling (path.join would collapse the "..").
    const aliasSpelling = `${path.dirname(f)}/sub/../${path.basename(f)}`;
    const r = await editTool.handler({
      edits: [
        { file_path: aliasSpelling, old_string: "one", new_string: "ONE" },
        { file_path: f, old_string: "two", new_string: "TWO" },
      ],
    });
    ok("H2 alias rejected", r.isError === true, text(r));
    ok("H2 alias msg", /same file/.test(text(r)), text(r));
    eq("H2 alias unchanged", read(f), "one\ntwo\n");
  }
  {
    // Same literal path twice in one batch is fine (existing semantics).
    const f = fixture("samelit.txt", "one\ntwo\n");
    const r = await editTool.handler({
      edits: [
        { file_path: f, old_string: "one", new_string: "ONE" },
        { file_path: f, old_string: "two", new_string: "TWO" },
      ],
    });
    ok("H3 same literal ok", r.isError === undefined, text(r));
    eq("H3 same literal content", read(f), "ONE\nTWO\n");
  }

  // ── I. reliability-suite compatibility ports ──────────────────────────
  {
    const f = fixture("rel-missing-new.txt", "alpha\n");
    await rejectsUnchanged(f, { file_path: f, old_string: "alpha" }, /new_string is required/, "I1 missing new_string");
    await rejectsUnchanged(f, { file_path: f, old_string: "alpha", new_string: 5 }, /new_string is required/, "I2 numeric new_string");
    const f2 = fixture("rel-batch-new.txt", "alpha\n");
    await rejectsUnchanged(f2, { file_path: f2, edits: [{ old_string: "alpha", new_string: 5 }] }, /new_string is required/, "I3 batch numeric new_string");
  }
  {
    // BOM/CRLF range edit keeps EOL + BOM (matrix spot check).
    const f = fixture("rel-matrix.txt", "﻿alpha\r\nbeta\r\n");
    const r = await editTool.handler({ file_path: f, start_line: 1, end_line: 3, content: "ALPHA\nBETA\n" });
    ok("I4 bom+crlf matrix ok", r.isError === undefined, text(r));
    eq("I4 bom+crlf matrix content", read(f), "﻿ALPHA\r\nBETA\r\n");
  }
  {
    const fa = fixture("rel-atomic-a.txt", "alpha\n");
    const fb = path.join(fixDir, "rel-atomic-missing.txt");
    const r = await editTool.handler({
      edits: [
        { file_path: fa, old_string: "alpha", new_string: "ALPHA" },
        { file_path: fb, old_string: "beta", new_string: "BETA" },
      ],
    });
    ok("I5 missing file fails", r.isError === true, text(r));
    ok("I5 missing file msg", /Cannot read file/.test(text(r)), text(r));
    eq("I5 first file unchanged", read(fa), "alpha\n");
  }
  {
    const fa = fixture("rel-idx-a.txt", "one\ntwo\n");
    const fb = fixture("rel-idx-b.txt", "three\n");
    const r = await editTool.handler({
      edits: [
        { file_path: fa, old_string: "one", new_string: "ONE" },
        { file_path: fa, old_string: "two", new_string: "TWO" },
        { file_path: fb, old_string: "three", new_string: "THREE" },
        { file_path: fb, old_string: "missing", new_string: "X" },
      ],
    });
    ok("I6 late failure", r.isError === true, text(r));
    ok("I6 global index", /edits\[3\]/.test(text(r)), text(r));
    eq("I6 fileA unchanged", read(fa), "one\ntwo\n");
    eq("I6 fileB unchanged", read(fb), "three\n");
  }
  {
    const fa = fixture("rel-hash-a.txt", "aaa\n");
    const fb = fixture("rel-hash-b.txt", "bbb\n");
    const r = await editTool.handler({
      expected_hash: "0".repeat(64),
      edits: [
        { file_path: fa, old_string: "aaa", new_string: "AAA" },
        { file_path: fb, old_string: "bbb", new_string: "BBB" },
      ],
    });
    ok("I7 top hash multi fails", r.isError === true, text(r));
    ok("I7 top hash msg", /single-file/.test(text(r)), text(r));
    eq("I7 hashA unchanged", read(fa), "aaa\n");
    eq("I7 hashB unchanged", read(fb), "bbb\n");
  }
  {
    const f = fixture("rel-strict.txt", "x\ny\n");
    const saved = process.env.CAVE_TOOLS_MODE;
    process.env.CAVE_TOOLS_MODE = "strict";
    let r;
    try {
      r = await editTool.handler({ file_path: f, start_line: 1, end_line: 1, content: "X" });
    } finally {
      if (saved === undefined) delete process.env.CAVE_TOOLS_MODE;
      else process.env.CAVE_TOOLS_MODE = saved;
    }
    ok("I8 strict proof required", r.isError === true && /STRICT/.test(text(r)), text(r));
    eq("I8 strict unchanged", read(f), "x\ny\n");
  }
  {
    // Batch failure labels: precise failed-file vs not-attempted wording.
    if (process.platform === "linux" && typeof process.getuid === "function" && process.getuid() !== 0) {
      const first = fixture("rel-wf-first.txt", "first\n");
      const blocked = fixture("rel-wf-blocked.txt", "second\n");
      chmodSync(blocked, 0o444);
      let r;
      try {
        r = await editTool.handler({
          edits: [
            { file_path: first, old_string: "first", new_string: "FIRST" },
            { file_path: blocked, old_string: "second", new_string: "SECOND" },
          ],
        });
      } finally {
        chmodSync(blocked, 0o644);
      }
      ok("I9 write fail isError", r.isError === true, text(r));
      ok("I9 failed-file label", /Failed file \(may be partially modified\): .*rel-wf-blocked\.txt/.test(text(r)), text(r));
      ok("I9 written label", /Files written before failure: .*rel-wf-first\.txt/.test(text(r)), text(r));
      ok("I9 not-attempted label", /Files not attempted: \(none\)/.test(text(r)), text(r));
      eq("I9 first written", read(first), "FIRST\n");
      eq("I9 blocked unchanged", read(blocked), "second\n");
    } else {
      skipped++;
      console.log("SKIP I9 write-failure labels: needs non-root linux");
    }
  }

  console.log(`\nedit-handler: ${pass} passed, ${fail} failed, ${skipped} skipped`);
  console.log(
    "COVERAGE: handler IO (single/pure-batch/mixed-batch, BOM, range EOF, " +
    "mixed EOL, arg contract, guards, aliases, reliability ports) under the mandatory outer lock.",
  );
  if (fail > 0) {
    for (const f of failures) console.error(`  - ${f}`);
    exitCode = 1;
  }
} finally {
  // Clean only this run's scratch + isolated data dir (unique mkdtemp dirs).
  rmSync(scratch, { recursive: true, force: true });
  rmSync(dataDir, { recursive: true, force: true });
}
process.exitCode = exitCode;
