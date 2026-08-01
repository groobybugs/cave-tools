import { createHash } from "crypto";

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
 * Append after last: startLine=lineCount+1, endLine=lineCount.
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

  const eol = detectEol(content);
  const lines = toLfLines(content);
  const lineCount = lines.length;
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

  const adapted = replacement.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  const newLines = adapted.length === 0 ? [] : adapted.split("\n");

  const replacedSlice = isInsert ? [] : lines.slice(startLine - 1, endLine);
  const replacedChars = replacedSlice.join("\n").length;

  const before = lines.slice(0, startLine - 1);
  const after = isInsert ? lines.slice(startLine - 1) : lines.slice(endLine);
  const next = before.concat(newLines, after);
  return {
    ok: true,
    content: fromLfLines(next, eol),
    lineCount,
    replacedChars,
  };
}

/**
 * Move inclusive lines [startLine, endLine] to insert before insertBefore
 * (1-based). insertBefore may equal lineCount+1 to move to end.
 * Computed on original line numbers (cut first, then insert).
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

  const eol = detectEol(content);
  const lines = toLfLines(content);
  const lineCount = lines.length;
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

  const block = lines.slice(startLine - 1, endLine);
  const movedChars = block.join("\n").length;
  const without = lines.slice(0, startLine - 1).concat(lines.slice(endLine));

  // Destination index into `without` (1-based insert-before).
  let dest = insertBefore;
  if (insertBefore > endLine) {
    dest = insertBefore - blockLen;
  }
  // insertBefore <= startLine: dest stays insertBefore

  const next = without.slice(0, dest - 1).concat(block, without.slice(dest - 1));
  return { ok: true, content: fromLfLines(next, eol), movedChars };
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
  const raw = (process.env.CAVE_TOOLS_MODE || "enforce").toLowerCase().trim();
  if (raw === "off" || raw === "hint" || raw === "enforce" || raw === "strict") return raw;
  return "enforce";
}
