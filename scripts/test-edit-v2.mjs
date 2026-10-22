// V2.1.2 edit-matching verification (API-level only — no handler integration).
//
// What this covers: the pure matching + EOL semantics in
// src/tools/edit-match.ts (canonical OpenCode v2.1.2 port) and the
// Cave-extension fallback wrapper in src/tools/replacers.ts
// (computeEditResult / findReplacement adapter).
// What this does NOT cover: edit.ts handler integration (file read/write,
// permission, diff, formatter). That stays untouched until the parent
// authorizes integration — see the coverage note printed at the end.
//
// Run: node scripts/test-edit-v2.mjs  (from the repo root)
//   1. typechecks + compiles ONLY the owned closure
//      (edit-match.ts, replacers.ts, runtime/context.ts) into a unique
//      mkdtemp dir under /tmp/opencode — real type errors fail the run,
//      no tolerance hacks, no full build (workers own the other files).
//   2. exercises the compiled JS with temp fixtures in the same scratch dir.
// Scratch is unique per run (no interprocess races) and removed in `finally`.

import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "..");
const opencodeTmp = path.join(tmpdir(), "opencode");
mkdirSync(opencodeTmp, { recursive: true });
const scratch = mkdtempSync(path.join(opencodeTmp, "cave-edit-v2-"));
const outDir = path.join(scratch, "dist");
const fixDir = path.join(scratch, "fixtures");
mkdirSync(outDir, { recursive: true });
mkdirSync(fixDir, { recursive: true });

let exitCode = 0;
try {

// ── Step 1: compile the owned closure only ────────────────────────────────
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
      "src/tools/edit-match.ts",
      "src/tools/replacers.ts",
      "src/runtime/context.ts",
    ],
    { cwd: repo, stdio: "inherit" },
  );
} catch (e) {
  console.error("COMPILE_FAIL: owned closure has type errors (see tsc output above).");
  exitCode = 1;
  throw e;
}

const em = await import(pathToFileURL(path.join(outDir, "tools", "edit-match.js")).href);
const rep = await import(pathToFileURL(path.join(outDir, "tools", "replacers.js")).href);
const { computeEdit, matchView, lineEnding, withRegionEndings, isEndingsOnlyChange } = em;
const { findReplacement, computeEditResult } = rep;

// ── tiny harness ──────────────────────────────────────────────────────────
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

// ── A. legacy findReplacement contract (used by active edit.ts worker) ────
{
  const m = findReplacement("hello world\n", "world", false);
  eq("A1 search", m.search, "world");
  eq("A1 index", m.index, 6);
}
{
  const m = findReplacement("a a a", "a", false);
  ok("A2 ambiguous", m.nonUnique === true && m.error === "non-unique", JSON.stringify(m));
  const all = findReplacement("a a a", "a", true);
  eq("A2 replaceAll first", all.search, "a");
  eq("A2 replaceAll index", all.index, 0);
}
{
  const content = "say \u201Chello\u201D ok";
  const m = findReplacement(content, '"hello"', false);
  eq("A3 unicode search", m.search, "\u201Chello\u201D");
  eq("A3 unicode index", m.index, content.indexOf("\u201Chello\u201D"));
}
{
  const m = findReplacement("foo   \n", "foo", false);
  eq("A4 trailing search", m.search, "foo");
  eq("A4 trailing index", m.index, 0);
}
{
  const m = findReplacement("abc", "zzz", false);
  ok("A5 not-found", !!m.error && !m.nonUnique, JSON.stringify(m));
  const e = findReplacement("abc", "", false);
  ok("A6 empty", !!e.error, JSON.stringify(e));
}

// ── B. pure upstream computeEdit ──────────────────────────────────────────
{
  const r = computeEdit("hello world\n", "world", "there", false);
  ok("B1 ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    eq("B1 updated", r.updated, "hello there\n");
    eq("B1 replacements", r.replacements, 1);
    eq("B1 endingsOnly", r.endingsOnly, false);
  }
}
{
  const single = computeEdit("a a a", "a", "b", false);
  ok("B2 ambiguous", single.ok === false && single.nonUnique === true, JSON.stringify(single));
  if (!single.ok) eq("B2 matches", single.matches.length, 3);
  const all = computeEdit("a a a", "a", "b", true);
  ok("B2 all ok", all.ok === true, JSON.stringify(all));
  if (all.ok) {
    eq("B2 all updated", all.updated, "b b b");
    eq("B2 all applied", all.applied.length, 3);
  }
}
{
  // ASCII query inside a larger curly-punctuation line (old conditional
  // unicode tier missed this: normalize(query) === query skipped the tier).
  const src = "const s = \u201Chello\u201D;";
  const r = computeEdit(src, "hello", "hi", false);
  ok("B3 curly-line ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    eq("B3 slice", src.slice(r.matches[0].start, r.matches[0].end), "hello");
    eq("B3 updated", r.updated, "const s = \u201Chi\u201D;");
  }
}
{
  // Multiple distinct unicode variants, replace_all must change every span
  // (first-literal split/join only changed one shape).
  const src = "\u2018a\u2019 and \u201Aa\u201B\n";
  const r = computeEdit(src, "'a'", "'b'", true);
  ok("B4 variants ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    eq("B4 count", r.replacements, 2);
    eq("B4 applied", r.applied.length, 2);
    const slices = r.matches.map((m) => src.slice(m.start, m.end)).sort();
    eq("B4 distinct raws", JSON.stringify(slices), JSON.stringify(["\u2018a\u2019", "\u201Aa\u201B"].sort()));
    eq("B4 updated", r.updated, "'b' and 'b'\n");
    for (const a of r.applied) eq("B4 per-span text", a.replacement, "'b'");
  }
}
{
  // Differing trailing-whitespace spans via the trailing tier (exact and
  // unicode both miss: no literal "foo  " anywhere). Every span must be
  // replaced with its own region-aware text — first-literal split/join
  // would leave the "foo\t" line behind.
  const src = "foo \nfoo\t\n";
  const r = computeEdit(src, "foo  ", "bar", true);
  ok("B5 trailing-all ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    eq("B5 count", r.replacements, 2);
    eq("B5 updated", r.updated, "bar\nbar\n");
    for (const a of r.applied) eq("B5 per-span text", a.replacement, "bar");
  }
  // Exact tier still short-circuits when it hits (same query, LF file).
  const e = computeEdit("foo  \nfoo\t\nfoo\n", "foo", "bar", true);
  ok("B5 exact-wins ok", e.ok === true, JSON.stringify(e));
  if (e.ok) eq("B5 exact-wins bytes", e.updated, "bar  \nbar\t\nbar\n");
}
{
  // EOL counting + LF-view -> source offset mapping.
  eq("B6 crlf-majority", lineEnding("a\r\nb\r\nc\n", "\n"), "\r\n");
  eq("B6 lf-majority", lineEnding("a\nb\nc\r\n", "X"), "\n");
  eq("B6 tie-fallback", lineEnding("a\r\nb\n", "F"), "F");
  const src = "a\r\nb\r\nc\n";
  const r = computeEdit(src, "b\nc", "X", false);
  ok("B6 mapped ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    eq("B6 region", src.slice(r.matches[0].start, r.matches[0].end), "b\r\nc");
    eq("B6 updated", r.updated, "a\r\nX\n");
  }
  // Same mapping exercised through a real CRLF fixture file under /tmp/opencode.
  const p = path.join(fixDir, "crlf.txt");
  writeFileSync(p, "a\r\nb\r\nc\n");
  const bytes = readFileSync(p, "utf8");
  const rf = computeEdit(bytes, "b\nc", "X", false);
  ok("B6 fixture ok", rf.ok === true, JSON.stringify(rf));
  if (rf.ok) {
    writeFileSync(p, rf.updated);
    eq("B6 fixture bytes", readFileSync(p, "utf8"), "a\r\nX\n");
  }
}
{
  // Single CRLF line -> multiline replacement uses region (CRLF) endings.
  const r = computeEdit("a\r\nb\r\n", "b", "x\ny", false);
  ok("B7 ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    eq("B7 per-span", r.applied[0].replacement, "x\r\ny");
    eq("B7 updated", r.updated, "a\r\nx\r\ny\r\n");
    ok("B7 no lone LF", !/(?<!\r)\n/.test(r.updated), JSON.stringify(r.updated));
  }
}
{
  // Region-aware EOL: kept lines reuse their own endings, changed-in-place
  // reuses the replaced line's ending, extras take the fallback; trailing
  // untouched bytes stay outside the span.
  eq(
    "B8 withRegionEndings",
    withRegionEndings("a\r\nb\nc", "a\nB\nc\nd", "\r\n"),
    "a\r\nB\nc\r\nd",
  );
  const src = "a\r\nb\nc\r\n";
  const r = computeEdit(src, "a\r\nb\nc", "a\nB\nc\nd", false);
  ok("B8 mixed ok", r.ok === true, JSON.stringify(r));
  if (r.ok) eq("B8 updated", r.updated, "a\r\nB\nc\r\nd\r\n");
}
{
  // Endings-only changes apply to the exact bytes, no EOL adaptation.
  const fwd = computeEdit("a\nb\n", "a\nb", "a\r\nb", false);
  ok("B9 fwd ok", fwd.ok === true && fwd.endingsOnly === true, JSON.stringify(fwd));
  if (fwd.ok) eq("B9 fwd bytes", fwd.updated, "a\r\nb\n");
  const rev = computeEdit("a\r\nb\r\n", "a\r\nb", "a\nb", false);
  ok("B9 rev ok", rev.ok === true && rev.endingsOnly === true, JSON.stringify(rev));
  if (rev.ok) eq("B9 rev bytes", rev.updated, "a\nb\r\n");
  const miss = computeEdit("a\r\nb", "a\nb", "a\r\nb", false);
  ok("B9 raw miss", miss.ok === false && !miss.nonUnique, JSON.stringify(miss));
  ok("B9 helper", isEndingsOnlyChange("a\nb", "a\r\nb") === true, "");
}
{
  // Computed-unchanged rejects.
  const u1 = computeEdit("\u2018hi\u2019", "'hi'", "\u2018hi\u2019", false);
  ok("B10 unicode-unchanged", u1.ok === false && /unchanged/.test(u1.error), JSON.stringify(u1));
  const u2 = computeEdit("foo\n", "foo   ", "foo", false);
  ok("B10 trailing-unchanged", u2.ok === false && /unchanged/.test(u2.error), JSON.stringify(u2));
}
{
  const id = computeEdit("x", "x", "x", false);
  ok("B11 identical", id.ok === false && /identical/.test(id.error), JSON.stringify(id));
  const ey = computeEdit("x", "", "y", false);
  ok("B11 empty", ey.ok === false && /must not be empty/.test(ey.error), JSON.stringify(ey));
}

// ── C. fallback wrapper (Cave extension, flag-gated) ───────────────────────
{
  const src = "    foo\n    bar\n";
  const saved = process.env.CAVE_TOOLS_FUZZY_EDIT;
  delete process.env.CAVE_TOOLS_FUZZY_EDIT;
  const r = computeEditResult(src, "foo\nbar", "x\ny", false);
  ok("C1 fuzzy ok", r.ok === true && r.fuzzy === true, JSON.stringify(r));
  if (r.ok) eq("C1 fuzzy updated", r.updated, "x\ny\n");
  process.env.CAVE_TOOLS_FUZZY_EDIT = "0";
  const strict = computeEditResult(src, "foo\nbar", "x\ny", false);
  ok("C2 strict miss", strict.ok === false && /Could not find/.test(strict.error), JSON.stringify(strict));
  // matchView-level sanity: pure miss stays a miss with the flag disabled.
  const mv = matchView(src, "foo\nbar", "x\ny");
  eq("C2 view miss", mv.matches.length, 0);
  if (saved === undefined) delete process.env.CAVE_TOOLS_FUZZY_EDIT;
  else process.env.CAVE_TOOLS_FUZZY_EDIT = saved;
  const up = computeEditResult("hello world\n", "world", "there", false);
  ok("C3 upstream not fuzzy", up.ok === true && up.fuzzy === false, JSON.stringify(up));
}
{
  // Zero upstream spans but ambiguous legacy-fuzzy candidates: non-unique
  // semantics win over not-found (pre-V2 handler behavior).
  const src = "  foo\n  bar\n  foo\n  bar\n";
  const saved = process.env.CAVE_TOOLS_FUZZY_EDIT;
  delete process.env.CAVE_TOOLS_FUZZY_EDIT;
  const amb = computeEditResult(src, "foo\nbar", "x\ny", false);
  ok("C4 fuzzy-ambiguous nonUnique", amb.ok === false && amb.nonUnique === true, JSON.stringify(amb));
  ok("C4 fuzzy flag", amb.fuzzy === true, JSON.stringify(amb));
  if (!amb.ok) ok("C4 no notfound", !/Could not find/.test(amb.error), amb.error);
  process.env.CAVE_TOOLS_FUZZY_EDIT = "0";
  const dis = computeEditResult(src, "foo\nbar", "x\ny", false);
  ok("C4 disabled still pure miss", dis.ok === false && dis.nonUnique !== true && /Could not find/.test(dis.error), JSON.stringify(dis));
  if (saved === undefined) delete process.env.CAVE_TOOLS_FUZZY_EDIT;
  else process.env.CAVE_TOOLS_FUZZY_EDIT = saved;
}
{
  // Intentional no fall-through: upstream ambiguity / no-op rejects stay
  // pure even with fuzzy enabled.
  const saved = process.env.CAVE_TOOLS_FUZZY_EDIT;
  delete process.env.CAVE_TOOLS_FUZZY_EDIT;
  const ua = computeEditResult("a a a", "a", "b", false);
  ok("C5 upstream-ambiguous final", ua.ok === false && ua.nonUnique === true && ua.fuzzy !== true, JSON.stringify(ua));
  const un = computeEditResult("foo\n", "foo   ", "foo", false);
  ok("C5 upstream-unchanged final", un.ok === false && /unchanged/.test(un.error) && un.fuzzy !== true, JSON.stringify(un));
  if (saved === undefined) delete process.env.CAVE_TOOLS_FUZZY_EDIT;
  else process.env.CAVE_TOOLS_FUZZY_EDIT = saved;
}

// ── D. mixed-EOL fixture round-trip ─────────────────────────────────────────
{
  const p = path.join(fixDir, "mixed.txt");
  writeFileSync(p, "a\r\nb\nc\r\n");
  const bytes = readFileSync(p, "utf8");
  const r = computeEdit(bytes, "a\r\nb\nc", "a\nB\nc\nd", false);
  ok("D1 mixed ok", r.ok === true, JSON.stringify(r));
  if (r.ok) {
    writeFileSync(p, r.updated);
    eq("D1 mixed bytes", readFileSync(p, "utf8"), "a\r\nB\nc\r\nd\r\n");
  }
}

console.log(`\nedit-v2: ${pass} passed, ${fail} failed`);
console.log(
  "COVERAGE: API-level only (pure matching + EOL + fallback). " +
  "Handler integration (edit.ts file I/O, permission, diff, formatter) NOT exercised — pending parent authorization.",
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
