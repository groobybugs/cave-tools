import { createHash } from "crypto";
import { getEnv } from "../runtime/context.js";

export function detectEol(content: string): "\r\n" | "\n" {
  return content.includes("\r\n") ? "\r\n" : "\n";
}

/** Normalize to LF lines for indexing (same line-count model as cave__read). */
export function toLfLines(content: string): string[] {
  return content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
}

export function fromLfLines(lines: string[], eol: "\r\n" | "\n"): string {
  const body = lines.join("\n");
  return eol === "\r\n" ? body.replace(/\n/g, "\r\n") : body;
}

type LineEol = "" | "\n" | "\r\n";

/** One line of content plus its own terminator ("" = file-final, no newline). */
interface LineSeg {
  text: string;
  eol: LineEol;
}

/**
 * Split content into per-line segments keeping each line's terminator.
 * Line count is identical to toLfLines(content).length: lone \r counts as a
 * line break (normalized to "\n"), matching toLfLines.
 */
function parseLineSegs(content: string): LineSeg[] {
  const segs: LineSeg[] = [];
  const re = /[^\r\n]*(?:\r\n|\n|\r|$)/g;
  for (;;) {
    const m = re.exec(content);
    if (m === null) break;
    const raw = m[0];
    if (raw.length === 0) {
      // Empty tail match: real only as the terminal sentinel of a file that
      // ends with a line break (or for empty content itself).
      if (segs.length === 0 || segs[segs.length - 1].eol !== "") {
        segs.push({ text: "", eol: "" });
      }
      break;
    }
    if (raw.endsWith("\r\n")) segs.push({ text: raw.slice(0, -2), eol: "\r\n" });
    else if (raw.endsWith("\n") || raw.endsWith("\r")) segs.push({ text: raw.slice(0, -1), eol: "\n" });
    else segs.push({ text: raw, eol: "" });
  }
  return segs;
}

/** File fallback EOL: majority of present terminators; ties/empty -> "\n". */
function dominantEol(segs: LineSeg[]): "\n" | "\r\n" {
  let crlf = 0;
  let lf = 0;
  for (const s of segs) {
    if (s.eol === "\r\n") crlf++;
    else if (s.eol === "\n") lf++;
  }
  return crlf > lf ? "\r\n" : "\n";
}

function renderSegs(segs: LineSeg[]): string {
  let out = "";
  for (const s of segs) out += s.text + s.eol;
  return out;
}

/** Split replacement text into LF lines (same semantics as before: a trailing
 * newline yields a trailing "" entry, i.e. an extra empty line). */
function splitReplacementLines(replacement: string): string[] {
  const adapted = replacement.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  return adapted.length === 0 ? [] : adapted.split("\n");
}

/** Short stable tag for one line (trueline-style fingerprint). */
export function lineTag(line: string): string {
  return createHash("sha1").update(line, "utf8").digest("hex").slice(0, 2);
}

/**
 * Checksum for inclusive 1-based line range (or empty insert span).
 * Format: 8 hex chars over "start:end\\n" + joined lines.
 */
export function rangeChecksum(
  lines: string[],
  startLine: number,
  endLine: number,
): string {
  const isInsert = endLine === startLine - 1;
  const slice = isInsert ? [] : lines.slice(startLine - 1, endLine);
  const payload = `${startLine}:${endLine}\n${slice.join("\n")}`;
  return createHash("sha256").update(payload, "utf8").digest("hex").slice(0, 8);
}

/** `00012:ab| content` when withTags, else `00012| content`. */
export function formatNumberedLine(
  lineNo: number,
  line: string,
  withTags = true,
): string {
  const num = lineNo.toString().padStart(5, "0");
  if (!withTags) return `${num}| ${line}`;
  return `${num}:${lineTag(line)}| ${line}`;
}

export function formatNumberedWindow(
  lines: string[],
  startLine: number,
  endLine: number,
  withTags = true,
): string {
  const out: string[] = [];
  const lo = Math.max(1, startLine);
  const hi = Math.min(lines.length, endLine);
  for (let n = lo; n <= hi; n++) {
    out.push(formatNumberedLine(n, lines[n - 1] ?? "", withTags));
  }
  return out.join("\n");
}

/**
 * Replace inclusive 1-based lines [startLine, endLine] with replacement.
 * Insert before N: startLine=N, endLine=N-1.
 * Append after last: startLine=lineCount+1, endLine=lineCount. On a file that
 * ends with a newline, lineCount includes the terminal empty sentinel and
 * lineCount+1 folds to insert-before-sentinel, so the final newline is kept.
 * Only touched lines change: every other line keeps its exact bytes and
 * terminator (no whole-file EOL normalization). Replacement lines take the
 * terminator of the region line they replace (position-wise; extras take the
 * region's last line ending, inserts take the following line's ending),
 * falling back to the file's dominant EOL.
 */
export function applyRangeEdit(
  content: string,
  startLine: number,
  endLine: number,
  replacement: string,
):
  | { ok: true; content: string; lineCount: number; replacedChars: number }
  | { ok: false; error: string } {
  if (!Number.isInteger(startLine) || !Number.isInteger(endLine)) {
    return { ok: false, error: "start_line and end_line must be integers" };
  }
  if (startLine < 1) {
    return { ok: false, error: `start_line must be >= 1 (got ${startLine})` };
  }

  const segs = parseLineSegs(content);
  const lineCount = segs.length;
  const isInsert = endLine === startLine - 1;

  if (isInsert) {
    if (startLine > lineCount + 1) {
      return {
        ok: false,
        error: `insert start_line ${startLine} out of range (file has ${lineCount} lines; max insert is ${lineCount + 1})`,
      };
    }
  } else {
    if (endLine < startLine) {
      return {
        ok: false,
        error: `end_line (${endLine}) must be >= start_line-1 (${startLine - 1}); use end_line=start_line-1 to insert`,
      };
    }
    if (startLine > lineCount) {
      return {
        ok: false,
        error: `start_line ${startLine} out of range (file has ${lineCount} lines)`,
      };
    }
    if (endLine > lineCount) {
      return {
        ok: false,
        error: `end_line ${endLine} out of range (file has ${lineCount} lines)`,
      };
    }
  }

  // Append at lineCount+1 on a trailing-newline file targets the terminal
  // empty sentinel (not content): fold to insert-before-sentinel so the
  // result keeps its final newline instead of gaining a blank line.
  let s = startLine;
  let e = endLine;
  if (
    isInsert &&
    s === lineCount + 1 &&
    lineCount >= 1 &&
    segs[lineCount - 1].text === "" &&
    segs[lineCount - 1].eol === ""
  ) {
    s = lineCount;
    e = lineCount - 1;
  }
  const ins = e === s - 1;

  const newTexts = splitReplacementLines(replacement);

  const region = ins ? [] : segs.slice(s - 1, e);
  const replacedChars = region
    .map((r) => r.text)
    .join("\n").length;

  const fallback = dominantEol(segs);
  const following = ins ? segs[s - 1] : undefined;
  const newSegs: LineSeg[] = newTexts.map((t, i) => {
    let le: LineEol;
    if (ins) {
      le = following && following.eol !== "" ? following.eol : fallback;
    } else if (i < region.length) {
      le = region[i].eol !== "" ? region[i].eol : fallback;
    } else {
      const last = region[region.length - 1];
      le = last.eol !== "" ? last.eol : fallback;
    }
    return { text: t, eol: le };
  });

  const before = segs.slice(0, s - 1);
  const after = ins ? segs.slice(s - 1) : segs.slice(e);
  const next = before.concat(newSegs, after);
  // Preserve the file-final newline state when the op consumed the original
  // final segment (append/replace/delete through end of file).
  if (after.length === 0 && next.length > 0) {
    next[next.length - 1].eol = segs[segs.length - 1].eol;
  }
  // A former file-final line that is now followed by more lines takes the
  // file fallback EOL (untouched lines already carry their own terminators).
  for (let i = 0; i + 1 < next.length; i++) {
    if (next[i].eol === "") next[i].eol = fallback;
  }
  return {
    ok: true,
    content: renderSegs(next),
    lineCount,
    replacedChars,
  };
}

/**
 * Move inclusive lines [startLine, endLine] to insert before insertBefore
 * (1-based). insertBefore may equal lineCount+1 to move to end.
 * Computed on original line numbers (cut first, then insert).
 * The terminal empty sentinel of a trailing-newline file is not movable
 * content: ranges including it are rejected, and insert_before lineCount+1
 * folds to end-of-content so the final newline survives. The moved block
 * carries its original terminators; untouched lines keep exact bytes.
 */
export function applyMoveLines(
  content: string,
  startLine: number,
  endLine: number,
  insertBefore: number,
):
  | { ok: true; content: string; movedChars: number }
  | { ok: false; error: string } {
  if (!Number.isInteger(startLine) || !Number.isInteger(endLine) || !Number.isInteger(insertBefore)) {
    return { ok: false, error: "start_line, end_line, and insert_before must be integers" };
  }
  if (startLine < 1 || endLine < startLine) {
    return { ok: false, error: "move requires end_line >= start_line >= 1" };
  }

  const segs = parseLineSegs(content);
  const lineCount = segs.length;
  const blockLen = endLine - startLine + 1;

  if (endLine > lineCount) {
    return { ok: false, error: `end_line ${endLine} out of range (file has ${lineCount} lines)` };
  }
  if (insertBefore < 1 || insertBefore > lineCount + 1) {
    return {
      ok: false,
      error: `insert_before ${insertBefore} out of range (valid 1..${lineCount + 1})`,
    };
  }
  // Inside the block (not start or end+1 no-ops)
  if (insertBefore > startLine && insertBefore <= endLine) {
    return {
      ok: false,
      error: `insert_before ${insertBefore} lies inside moved block ${startLine}-${endLine}`,
    };
  }

  // The terminal empty sentinel is the trailing newline, not content:
  // folding it into the block (or landing on it) silently corrupts the
  // final newline, so reject it with a clear error instead.
  const hasSentinel =
    lineCount >= 1 &&
    segs[lineCount - 1].text === "" &&
    segs[lineCount - 1].eol === "";
  if (hasSentinel && endLine === lineCount) {
    return {
      ok: false,
      error: `cannot move line ${lineCount}: it is the trailing newline, not content (move lines 1..${lineCount - 1})`,
    };
  }

  // Work on real content lines; the sentinel (if any) is re-attached after.
  const w = hasSentinel ? segs.slice(0, lineCount - 1) : segs.slice();
  const block = w.slice(startLine - 1, endLine).map((s) => ({ ...s }));
  const movedChars = block
    .map((b) => b.text)
    .join("\n").length;
  const without = w.slice(0, startLine - 1).concat(w.slice(endLine));

  // Destination index into `without` (1-based insert-before). insert_before
  // lineCount+1 on a trailing-newline file means end-of-content: the
  // sentinel is not addressable, so clamp to it.
  const dest0 = hasSentinel ? Math.min(insertBefore, w.length + 1) : insertBefore;
  let dest = dest0;
  if (insertBefore > endLine) {
    dest = dest0 - blockLen;
  }
  // insertBefore <= startLine: dest stays insertBefore

  const fallback = dominantEol(segs);
  const nextW = without.slice(0, dest - 1).concat(block, without.slice(dest - 1));
  const next = hasSentinel ? nextW.concat([{ text: "", eol: "" }]) : nextW;
  for (let i = 0; i + 1 < next.length; i++) {
    if (next[i].eol === "") next[i].eol = fallback;
  }
  if (!hasSentinel && next.length > 0) {
    next[next.length - 1].eol = "";
  }
  return { ok: true, content: renderSegs(next), movedChars };
}

export function verifyRangeChecksum(
  content: string,
  startLine: number,
  endLine: number,
  expected: string,
): { ok: true } | { ok: false; actual: string; window: string; error: string } {
  const want = expected.trim().toLowerCase();
  if (want.length < 4) {
    return {
      ok: false,
      actual: "",
      window: "",
      error: "expected_range_checksum must be at least 4 hex characters",
    };
  }
  const lines = toLfLines(content);
  const actual = rangeChecksum(lines, startLine, endLine);
  // Accept full match or expected as prefix of actual (footer may shorten later).
  if (actual === want || actual.startsWith(want)) {
    return { ok: true };
  }
  const isInsert = endLine === startLine - 1;
  const winStart = isInsert ? Math.max(1, startLine - 2) : startLine;
  const winEnd = isInsert ? Math.min(lines.length, startLine + 1) : endLine;
  const window = formatNumberedWindow(lines, winStart, winEnd, true);
  return {
    ok: false,
    actual,
    window,
    error:
      `expected_range_checksum mismatch for lines ${startLine}-${endLine}. ` +
      `Re-read with cave__read offset=${isInsert ? startLine : startLine} ` +
      `limit=${isInsert ? 5 : Math.max(1, endLine - startLine + 1)} line_numbers=true.`,
  };
}

/** Active cave-tools intensity from env (off|hint|enforce|strict). Default enforce. */
export function activeEditMode(): "off" | "hint" | "enforce" | "strict" {
  const raw = (getEnv().CAVE_TOOLS_MODE || "enforce").toLowerCase().trim();
  if (raw === "off" || raw === "hint" || raw === "enforce" || raw === "strict") return raw;
  return "enforce";
}
