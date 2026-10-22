// Line-range helper reliability (helper-only — no handler integration).
//
// What this covers: the pure helpers in src/tools/line-range.ts
// (applyRangeEdit / applyMoveLines) after the trailing-newline + mixed-EOL
// fixes. Covers LF/CRLF with/without final newline, append at actual content
// end and at documented model lineCount+1, move first<->last, mixed-EOL
// edits/moves preserving untouched bytes, delete/replace/insert boundaries
// and no-ops, old + new error bounds, and the read-model line-count parity
// invariant. edit.ts handler integration is NOT covered (edit.ts is owned by
// another worker; the parent runs the existing full suite after all writers
// complete — this script must stay standalone until dist/test wiring is
// authorized).
//
// Run: node scripts/test-line-range-reliability.mjs  (from the repo root)
//   1. typechecks + compiles ONLY the owned closure
//      (line-range.ts, runtime/context.ts) into a unique mkdtemp dir under
//      /tmp/opencode — real type errors fail the run, no tolerance hacks,
//      no full build (other workers own the other files).
//   2. exercises the compiled helper; scratch is removed in `finally`.

import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "..");
const opencodeTmp = path.join(tmpdir(), "opencode");
mkdirSync(opencodeTmp, { recursive: true });
const scratch = mkdtempSync(path.join(opencodeTmp, "cave-line-range-"));
const outDir = path.join(scratch, "dist");
mkdirSync(outDir, { recursive: true });

let exitCode = 0;
try {
  // ── Step 1: compile the owned closure only ────────────────────────────
  try {
    execFileSync(
      "pnpm",
      [
        "exec",
        "tsc",
        "--outDir", outDir,
        "--rootDir", "src",
        "--target", "es2022",
        "--module", "nodenext",
        "--moduleResolution", "nodenext",
        "--skipLibCheck",
        "--declaration", "false",
        "--sourceMap", "false",
        "src/tools/line-range.ts",
        "src/runtime/context.ts",
      ],
      { cwd: repo, stdio: "inherit" },
    );
  } catch (e) {
    console.error("COMPILE_FAIL: owned closure has type errors (see tsc output above).");
    exitCode = 1;
    throw e;
  }

  const lr = await import(pathToFileURL(path.join(outDir, "tools", "line-range.js")).href);
  const { applyRangeEdit, applyMoveLines, rangeChecksum, verifyRangeChecksum, toLfLines } = lr;

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

  // ── B. existing test.mjs semantics preserved (pure-helper asserts) ──────
  {
    const h1 = applyRangeEdit("a\nb\nc\n", 2, 2, "B");
    ok("B1 ok", h1.ok === true, JSON.stringify(h1));
    if (h1.ok) {
      eq("B1 content", h1.content, "a\nB\nc\n");
      eq("B1 replacedChars", h1.replacedChars, 1);
    }
    const hIns = applyRangeEdit("a\nb\n", 2, 1, "x");
    ok("B2 ok", hIns.ok === true, JSON.stringify(hIns));
    if (hIns.ok) eq("B2 content", hIns.content, "a\nx\nb\n");
    const hDel = applyRangeEdit("a\nb\nc\n", 2, 3, "");
    ok("B3 ok", hDel.ok === true, JSON.stringify(hDel));
    if (hDel.ok) eq("B3 content", hDel.content, "a\n");
    const hBad = applyRangeEdit("a\nb\n", 5, 5, "z");
    ok("B4 bad", hBad.ok === false, JSON.stringify(hBad));
    const hCrlf = applyRangeEdit("a\r\nb\r\nc\r\n", 2, 2, "B");
    ok("B5 crlf ok", hCrlf.ok === true, JSON.stringify(hCrlf));
    if (hCrlf.ok) eq("B5 crlf content", hCrlf.content, "a\r\nB\r\nc\r\n");
    const mv = applyMoveLines("a\nb\nc\nd\n", 2, 3, 5);
    ok("B6 move ok", mv.ok === true, JSON.stringify(mv));
    if (mv.ok) {
      eq("B6 move content", mv.content, "a\nd\nb\nc\n");
      eq("B6 movedChars", mv.movedChars, 3);
    }
    const cs = rangeChecksum(["a", "b", "c"], 1, 2);
    ok("B7 checksum shape", typeof cs === "string" && cs.length === 8 && /^[0-9a-f]{8}$/.test(cs), cs);
    const vr = verifyRangeChecksum("a\nb\nc", 1, 2, rangeChecksum(["a", "b", "c"], 1, 2));
    ok("B7 checksum round-trip", vr.ok === true, JSON.stringify(vr));
    const vb = verifyRangeChecksum("a\nb\nc", 1, 2, "ffffffff");
    ok("B7 checksum mismatch shape", vb.ok === false && !!vb.actual && !!vb.window && !!vb.error, JSON.stringify(vb));
  }

  // ── C. trailing-newline append/move defects ─────────────────────────────
  {
    // Append at documented model lineCount+1 on trailing-NL file ("a\nb\n"
    // has lineCount 3): folds to insert-before-sentinel, keeps final NL.
    // Previously produced "a\nb\n\nc" (blank line, no final NL).
    const a1 = applyRangeEdit("a\nb\n", 4, 3, "c");
    ok("C1 model+1 ok", a1.ok === true, JSON.stringify(a1));
    if (a1.ok) eq("C1 model+1 content", a1.content, "a\nb\nc\n");
    // Existing append-before-sentinel behavior preserved.
    const a2 = applyRangeEdit("a\nb\n", 3, 2, "c");
    ok("C2 sentinel append ok", a2.ok === true, JSON.stringify(a2));
    if (a2.ok) eq("C2 sentinel append content", a2.content, "a\nb\nc\n");
    // Append at actual content end of a file WITHOUT trailing newline:
    // no trailing newline is added.
    const a3 = applyRangeEdit("a\nb", 3, 2, "c");
    ok("C3 no-trailing append ok", a3.ok === true, JSON.stringify(a3));
    if (a3.ok) eq("C3 no-trailing append content", a3.content, "a\nb\nc");
    // Empty file appends.
    const a4 = applyRangeEdit("", 2, 1, "c");
    ok("C4 empty model+1 ok", a4.ok === true, JSON.stringify(a4));
    if (a4.ok) eq("C4 empty model+1 content", a4.content, "c\n");
    const a5 = applyRangeEdit("", 1, 0, "c");
    ok("C5 empty insert ok", a5.ok === true, JSON.stringify(a5));
    if (a5.ok) eq("C5 empty insert content", a5.content, "c\n");
    const a6 = applyRangeEdit("\n", 3, 2, "c");
    ok("C6 lone-NL model+1 ok", a6.ok === true, JSON.stringify(a6));
    if (a6.ok) eq("C6 lone-NL model+1 content", a6.content, "\nc\n");
    // Move first line to documented end (insert_before lineCount+1):
    // previously "b\nc\n\na" (sentinel dragged into the middle).
    const m1 = applyMoveLines("a\nb\nc\n", 1, 1, 5);
    ok("C7 move-first-to-end ok", m1.ok === true, JSON.stringify(m1));
    if (m1.ok) eq("C7 move-first-to-end content", m1.content, "b\nc\na\n");
    // Move last content line to first.
    const m2 = applyMoveLines("a\nb\nc\n", 3, 3, 1);
    ok("C8 move-last-to-first ok", m2.ok === true, JSON.stringify(m2));
    if (m2.ok) eq("C8 move-last-to-first content", m2.content, "c\na\nb\n");
    // Same moves on files WITHOUT trailing newline.
    const m3 = applyMoveLines("a\nb\nc", 1, 1, 4);
    ok("C9 no-trailing first-to-end ok", m3.ok === true, JSON.stringify(m3));
    if (m3.ok) eq("C9 no-trailing first-to-end content", m3.content, "b\nc\na");
    const m4 = applyMoveLines("a\nb\nc", 3, 3, 1);
    ok("C10 no-trailing last-to-first ok", m4.ok === true, JSON.stringify(m4));
    if (m4.ok) eq("C10 no-trailing last-to-first content", m4.content, "c\na\nb");
    // CRLF variants.
    const m5 = applyMoveLines("a\r\nb\r\nc\r\n", 1, 1, 5);
    ok("C11 crlf first-to-end ok", m5.ok === true, JSON.stringify(m5));
    if (m5.ok) eq("C11 crlf first-to-end content", m5.content, "b\r\nc\r\na\r\n");
    const m6 = applyMoveLines("a\r\nb\r\nc", 1, 1, 4);
    ok("C12 crlf no-trailing first-to-end ok", m6.ok === true, JSON.stringify(m6));
    if (m6.ok) eq("C12 crlf no-trailing first-to-end content", m6.content, "b\r\nc\r\na");
    const r6 = applyRangeEdit("a\r\nb\r\n", 2, 1, "x");
    ok("C13 crlf insert ok", r6.ok === true, JSON.stringify(r6));
    if (r6.ok) eq("C13 crlf insert content", r6.content, "a\r\nx\r\nb\r\n");
    // The terminal sentinel is the trailing newline, not content: reject.
    const s1 = applyMoveLines("a\nb\n", 3, 3, 1);
    ok("C14 sentinel move rejected", s1.ok === false && /trailing newline/.test(s1.error), JSON.stringify(s1));
    const s2 = applyMoveLines("", 1, 1, 1);
    ok("C15 empty move rejected", s2.ok === false, JSON.stringify(s2));
    // Read-model lineCount (sentinel included) still reported in bounds.
    const b1 = applyRangeEdit("a\nb\n", 5, 5, "z");
    ok("C16 count parity in error", b1.ok === false && /file has 3 lines/.test(b1.error), JSON.stringify(b1));
    // Replacing the sentinel line itself via range stays out of range.
    const b2 = applyRangeEdit("a\nb\n", 4, 4, "c");
    ok("C17 sentinel replace out of range", b2.ok === false && /out of range/.test(b2.error), JSON.stringify(b2));
    // Move no-ops preserved.
    const n1 = applyMoveLines("a\nb\nc\n", 2, 2, 2);
    ok("C18 noop same-spot ok", n1.ok === true, JSON.stringify(n1));
    if (n1.ok) eq("C18 noop same-spot content", n1.content, "a\nb\nc\n");
    const n2 = applyMoveLines("a\nb\nc\n", 2, 2, 3);
    ok("C19 noop end-plus-one ok", n2.ok === true, JSON.stringify(n2));
    if (n2.ok) eq("C19 noop end-plus-one content", n2.content, "a\nb\nc\n");
  }

  // ── D. mixed-EOL preservation (no whole-file normalization) ────────────
  {
    // Replace keeps every untouched line's exact bytes (old code rewrote
    // line 3's "\r\n" to match the majority/detected EOL).
    const r1 = applyRangeEdit("1\r\n2\n3\r\n4\n", 2, 2, "TWO");
    ok("D1 mixed replace ok", r1.ok === true, JSON.stringify(r1));
    if (r1.ok) {
      eq("D1 mixed replace content", r1.content, "1\r\nTWO\n3\r\n4\n");
      ok("D1 prefix bytes", r1.content.startsWith("1\r\n"), JSON.stringify(r1.content));
      ok("D1 suffix bytes", r1.content.endsWith("\n3\r\n4\n"), JSON.stringify(r1.content));
    }
    // Inserts take the following line's terminator.
    const r2 = applyRangeEdit("1\r\n2\n3\r\n4\n", 3, 2, "x");
    ok("D2 insert-before-crlf ok", r2.ok === true, JSON.stringify(r2));
    if (r2.ok) eq("D2 insert-before-crlf content", r2.content, "1\r\n2\nx\r\n3\r\n4\n");
    const r3 = applyRangeEdit("1\r\n2\n3\r\n4\n", 2, 1, "x");
    ok("D3 insert-before-lf ok", r3.ok === true, JSON.stringify(r3));
    if (r3.ok) eq("D3 insert-before-lf content", r3.content, "1\r\nx\n2\n3\r\n4\n");
    // Multi-line replacement: position-wise region EOLs, extras take the
    // region's last line ending.
    const r4 = applyRangeEdit("1\r\n2\n3\r\n", 2, 3, "A\nB\nC");
    ok("D4 multi replace ok", r4.ok === true, JSON.stringify(r4));
    if (r4.ok) eq("D4 multi replace content", r4.content, "1\r\nA\nB\r\nC\r\n");
    // Moves carry the block's terminators; untouched lines keep theirs.
    const m1 = applyMoveLines("1\r\n2\n3\r\n4\n", 2, 2, 4);
    ok("D5 mixed move ok", m1.ok === true, JSON.stringify(m1));
    if (m1.ok) eq("D5 mixed move content", m1.content, "1\r\n3\r\n2\n4\n");
    const m2 = applyMoveLines("1\r\n2\n3\r\n", 1, 1, 5);
    ok("D6 mixed first-to-end ok", m2.ok === true, JSON.stringify(m2));
    if (m2.ok) eq("D6 mixed first-to-end content", m2.content, "2\n3\r\n1\r\n");
    // Deletes preserve neighbors.
    const d1 = applyRangeEdit("1\r\n2\n3\r\n", 2, 2, "");
    ok("D7 mixed delete ok", d1.ok === true, JSON.stringify(d1));
    if (d1.ok) eq("D7 mixed delete content", d1.content, "1\r\n3\r\n");
    const d2 = applyRangeEdit("1\r\n2\n3\r\n", 1, 1, "");
    ok("D8 mixed delete-first ok", d2.ok === true, JSON.stringify(d2));
    if (d2.ok) eq("D8 mixed delete-first content", d2.content, "2\n3\r\n");
    // File-final no-newline state preserved through replace.
    const f1 = applyRangeEdit("1\r\n2", 2, 2, "TWO");
    ok("D9 final-no-NL replace ok", f1.ok === true, JSON.stringify(f1));
    if (f1.ok) eq("D9 final-no-NL replace content", f1.content, "1\r\nTWO");
    const f2 = applyRangeEdit("1\r\n2", 1, 1, "A\nB");
    ok("D10 extend-first ok", f2.ok === true, JSON.stringify(f2));
    if (f2.ok) eq("D10 extend-first content", f2.content, "A\r\nB\r\n2");
    // Replacement trailing-newline semantics preserved as before: a trailing
    // "\n" in `content` yields a trailing "" entry, i.e. an extra empty line
    // (same as the old join-based implementation; existing tests rely on the
    // split, this pins it explicitly).
    const t1 = applyRangeEdit("a\nb\n", 2, 1, "x\n");
    ok("D11 insert trailing-NL ok", t1.ok === true, JSON.stringify(t1));
    if (t1.ok) eq("D11 insert trailing-NL content", t1.content, "a\nx\n\nb\n");
    const t2 = applyRangeEdit("a\nb\nc\n", 2, 2, "B\n");
    ok("D12 replace trailing-NL ok", t2.ok === true, JSON.stringify(t2));
    if (t2.ok) eq("D12 replace trailing-NL content", t2.content, "a\nB\n\nc\n");
    // No-op successes stay successes (content byte-identical).
    const z1 = applyRangeEdit("a\nb\nc\n", 2, 2, "b");
    ok("D13 same-text noop ok", z1.ok === true, JSON.stringify(z1));
    if (z1.ok) eq("D13 same-text noop content", z1.content, "a\nb\nc\n");
    const z2 = applyRangeEdit("a\nb\n", 2, 1, "");
    ok("D14 empty insert ok", z2.ok === true, JSON.stringify(z2));
    if (z2.ok) eq("D14 empty insert content", z2.content, "a\nb\n");
    // Delete through the terminal sentinel keeps prior semantics (final NL
    // goes with the deleted span).
    const s1 = applyRangeEdit("a\nb\nc\n", 2, 4, "");
    ok("D15 delete-thru-sentinel ok", s1.ok === true, JSON.stringify(s1));
    if (s1.ok) eq("D15 delete-thru-sentinel content", s1.content, "a");
    const s2 = applyRangeEdit("a\nb\nc\n", 1, 3, "");
    ok("D16 delete-all-content ok", s2.ok === true, JSON.stringify(s2));
    if (s2.ok) eq("D16 delete-all-content content", s2.content, "");
  }

  // ── E. error bounds (old + new) ─────────────────────────────────────────
  {
    const e1 = applyRangeEdit("a\nb\n", 1.5, 1, "x");
    ok("E1 non-integer", e1.ok === false && /integers/.test(e1.error), JSON.stringify(e1));
    const e2 = applyRangeEdit("a\nb\n", 0, 0, "x");
    ok("E2 start<1", e2.ok === false && /must be >= 1/.test(e2.error), JSON.stringify(e2));
    const e3 = applyRangeEdit("a\nb\n", 3, 1, "x");
    ok("E3 end<start-1", e3.ok === false && /must be >= start_line-1/.test(e3.error), JSON.stringify(e3));
    const e4 = applyRangeEdit("a\nb\n", 1, 5, "x");
    ok("E4 end>count", e4.ok === false && /end_line 5 out of range/.test(e4.error), JSON.stringify(e4));
    const e5 = applyRangeEdit("a\nb\n", 6, 5, "x");
    ok("E5 insert>max", e5.ok === false && /max insert is 4/.test(e5.error), JSON.stringify(e5));
    const e6 = applyMoveLines("a\nb\n", 1.5, 1, 1);
    ok("E6 move non-integer", e6.ok === false && /integers/.test(e6.error), JSON.stringify(e6));
    const e7 = applyMoveLines("a\nb\n", 0, 1, 1);
    ok("E7 move start<1", e7.ok === false && /end_line >= start_line >= 1/.test(e7.error), JSON.stringify(e7));
    const e8 = applyMoveLines("a\nb\n", 2, 1, 1);
    ok("E8 move end<start", e8.ok === false && /end_line >= start_line >= 1/.test(e8.error), JSON.stringify(e8));
    const e9 = applyMoveLines("a\nb\n", 1, 4, 1);
    ok("E9 move end>count", e9.ok === false && /end_line 4 out of range/.test(e9.error), JSON.stringify(e9));
    const e10 = applyMoveLines("a\nb\n", 1, 1, 0);
    ok("E10 move dest<1", e10.ok === false && /insert_before 0 out of range/.test(e10.error), JSON.stringify(e10));
    const e11 = applyMoveLines("a\nb\n", 1, 1, 5);
    ok("E11 move dest>max", e11.ok === false && /insert_before 5 out of range/.test(e11.error), JSON.stringify(e11));
    const e12 = applyMoveLines("a\nb\nc\nd\n", 2, 3, 3);
    ok("E12 move inside", e12.ok === false && /lies inside/.test(e12.error), JSON.stringify(e12));
    // New: sentinel / empty edge cases rejected with clear errors.
    const e13 = applyMoveLines("a\nb\nc\n", 2, 4, 1);
    ok("E13 move span incl sentinel", e13.ok === false && /trailing newline/.test(e13.error), JSON.stringify(e13));
  }

  // ── F. model-parity + checksum-representation invariants ────────────────
  {
    // Segment line count matches the toLfLines model (active core worker
    // imports toLfLines) for every EOL shape, pinned via reported counts.
    const fixtures = [
      "",
      "a",
      "a\n",
      "a\nb",
      "a\nb\n",
      "\n",
      "\n\n",
      "a\r\nb",
      "a\rb",
      "a\r",
      "a\r\n",
      "a\n\nb",
      "1\r\n2\n3\r\n4",
      "1\r\n2\n3\r\n4\n",
    ];
    for (const fx of fixtures) {
      const want = toLfLines(fx).length;
      const r = applyRangeEdit(fx, 999, 999, "x");
      ok(
        `F1 count parity ${JSON.stringify(fx)}`,
        r.ok === false && r.error.includes(`file has ${want} lines`),
        JSON.stringify(r),
      );
    }
    // Strict checksum representation untouched (function not modified):
    // stable value across calls, 8 lowercase hex, verify round-trip.
    const c1 = rangeChecksum(["a", "b", "c", ""], 2, 2);
    const c2 = rangeChecksum(["a", "b", "c", ""], 2, 2);
    ok("F2 checksum stable", c1 === c2 && /^[0-9a-f]{8}$/.test(c1), c1);
    const c3 = rangeChecksum(["a", "b", "c", ""], 2, 1);
    ok("F3 insert checksum differs", c3 !== c1 && /^[0-9a-f]{8}$/.test(c3), `${c1} vs ${c3}`);
  }

  console.log(`\nline-range: ${pass} passed, ${fail} failed`);
  console.log(
    "COVERAGE: helper-only (applyRangeEdit/applyMoveLines + model invariants). " +
    "edit.ts handler + existing test.mjs suite NOT run — pending parent authorization after writers complete.",
  );
  if (fail > 0) {
    for (const f of failures) console.error(`  - ${f}`);
    exitCode = 1;
  }
} finally {
  // Clean only this run's scratch (unique mkdtemp dir): no races with other
  // test processes, no stale compiled outputs left behind.
  rmSync(scratch, { recursive: true, force: true });
}
process.exitCode = exitCode;
