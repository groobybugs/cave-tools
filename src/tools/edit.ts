import { readFile } from "fs/promises";
import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { getFileHash, invalidateFileCache, recordEdit } from "../compression/utils.js";
import { findReplacement } from "./replacers.js";
import { decodeUtf8PreserveBom, joinBom, writeIfUnchanged } from "../runtime/file-mutation.js";

// Adapt new_string line endings to match the matched span's style so an edit
// against a CRLF file doesn't inject lone LFs (and vice-versa).
function adaptLineEndings(search: string, newString: string): string {
  const searchCRLF = search.includes("\r\n");
  const newCRLF = newString.includes("\r\n");
  if (searchCRLF && !newCRLF) return newString.replace(/\n/g, "\r\n");
  if (!searchCRLF && newCRLF) return newString.replace(/\r\n/g, "\n");
  return newString;
}

function detectEol(content: string): "\r\n" | "\n" {
  return content.includes("\r\n") ? "\r\n" : "\n";
}

/** Normalize to LF lines for indexing (same line-count model as cave__read). */
function toLfLines(content: string): string[] {
  return content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
}

function fromLfLines(lines: string[], eol: "\r\n" | "\n"): string {
  const body = lines.join("\n");
  return eol === "\r\n" ? body.replace(/\n/g, "\r\n") : body;
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
): { ok: true; content: string; lineCount: number } | { ok: false; error: string } {
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
  // Empty string → delete range / empty insert. Trailing \n keeps final empty line.
  const newLines = adapted.length === 0 ? [] : adapted.split("\n");

  const before = lines.slice(0, startLine - 1);
  const after = isInsert ? lines.slice(startLine - 1) : lines.slice(endLine);
  const next = before.concat(newLines, after);
  return { ok: true, content: fromLfLines(next, eol), lineCount };
}

function findClosestLineHint(content: string, oldStr: string): string {
  const firstLine = (oldStr.split("\n")[0] || "").trim();
  if (firstLine.length < 4) return "";

  const lines = content.split("\n");
  let bestLine: { num: number; text: string } | null = null;

  for (let i = 0; i < lines.length; i++) {
    if (lines[i].includes(firstLine)) {
      bestLine = { num: i + 1, text: lines[i] };
      break;
    }
  }

  if (!bestLine) {
    const keywords = firstLine.split(/[^a-zA-Z0-9_]/).filter((w) => w.length >= 4);
    if (keywords.length > 0) {
      for (let i = 0; i < lines.length; i++) {
        if (lines[i].includes(keywords[0])) {
          bestLine = { num: i + 1, text: lines[i] };
          break;
        }
      }
    }
  }

  if (!bestLine) return "";
  const preview =
    bestLine.text.trim().length > 100
      ? bestLine.text.trim().slice(0, 97) + "..."
      : bestLine.text.trim();
  return `\nClosest match at line ${bestLine.num}: \`${preview}\`\nHint: check indentation/whitespace differences.`;
}

interface StrEdit {
  old_string: string;
  new_string: string;
  replace_all?: boolean;
}

interface RangeEdit {
  start_line: number;
  end_line: number;
  content: string;
}

interface MatchedEdit {
  matchIndex: number;
  matchLength: number;
  new_string: string;
}

function isRangeShape(edit: Record<string, unknown>): boolean {
  return edit.start_line !== undefined && edit.start_line !== null;
}

function isStrShape(edit: Record<string, unknown>): boolean {
  return typeof edit.old_string === "string" && edit.old_string.length > 0;
}

async function checkExpectedHash(
  filePath: string,
  expected: unknown,
): Promise<ToolResult | null> {
  if (expected === undefined || expected === null) return null;
  const want = String(expected).trim().toLowerCase();
  if (want === "") return null;
  if (want.length < 8) {
    return err("expected_hash must be at least 8 hex characters");
  }
  if (!/^[0-9a-f]+$/.test(want)) {
    return err("expected_hash must be hexadecimal");
  }
  const actual = await getFileHash(filePath);
  if (!actual) {
    return err(`could not hash file for expected_hash check: ${filePath}`);
  }
  const actualLc = actual.toLowerCase();
  if (actualLc === want || actualLc.startsWith(want)) return null;
  return err(
    `expected_hash mismatch — file changed since read. Re-run cave__read with line_numbers=true.\n` +
      `expected: ${want.slice(0, 16)}${want.length > 16 ? "…" : ""}\n` +
      `actual:   ${actualLc.slice(0, 16)}…`,
  );
}

async function writeEdited(
  filePath: string,
  sourceBytes: Uint8Array,
  bom: boolean,
  updated: string,
): Promise<ToolResult | null> {
  try {
    const writeResult = await writeIfUnchanged(filePath, sourceBytes, joinBom(updated, bom));
    invalidateFileCache(writeResult.canonical);
    recordEdit(writeResult.canonical);
  } catch (error) {
    return err(error instanceof Error ? error.message : `Cannot write file: ${filePath}`);
  }
  invalidateFileCache(filePath);
  return null;
}

export const editTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__edit",
  description:
    "Edit a file by search/replace OR by line range. " +
    "Str-replace: old_string → new_string (fuzzy match chain). " +
    "Line-range (token-cheap): start_line + end_line + content (new body only; no old echo). " +
    "Inclusive 1-based lines matching cave__read line_numbers. " +
    "Insert before N: start_line=N, end_line=N-1. Append: start_line=lineCount+1, end_line=lineCount. " +
    "Prefer line-range for medium+ hunks after cave__read(..., line_numbers=true); pass expected_hash from the read footer when available. " +
    "Batch via edits[] (mixed str/range). Range edits applied high→low. Adapts CRLF/LF and invalidates read cache.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to modify",
      },
      old_string: {
        type: "string",
        description: "Text to replace (fuzzy). Mutually exclusive with start_line.",
      },
      new_string: {
        type: "string",
        description: "Replacement text for str-replace mode",
      },
      replace_all: {
        type: "boolean",
        description: "Replace every occurrence instead of requiring uniqueness (default false)",
      },
      start_line: {
        type: "number",
        description: "1-based inclusive start line for range mode (mutually exclusive with old_string)",
      },
      end_line: {
        type: "number",
        description: "1-based inclusive end line. Use start_line-1 to insert before start_line.",
      },
      content: {
        type: "string",
        description: "Replacement body for range mode (multi-line OK; empty deletes). No line-number prefixes.",
      },
      expected_hash: {
        type: "string",
        description: "Optional file sha256 (full or ≥8-char prefix from cave__read footer). Rejects if file changed.",
      },
      edits: {
        type: "array",
        items: {
          type: "object",
          properties: {
            old_string: { type: "string" },
            new_string: { type: "string" },
            replace_all: { type: "boolean" },
            start_line: { type: "number" },
            end_line: { type: "number" },
            content: { type: "string" },
            expected_hash: { type: "string" },
          },
        },
        description: "Multiple edits (str-replace and/or range). Ranges applied high→low, then str-replace.",
      },
    },
    required: ["file_path"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path);

    let sourceBytes: Buffer;
    let content: string;
    let bom = false;
    try {
      sourceBytes = await readFile(filePath);
      const decoded = decodeUtf8PreserveBom(sourceBytes);
      content = decoded.text;
      bom = decoded.bom;
    } catch (error) {
      return err(
        error instanceof Error && error.message.includes("encoded data")
          ? `File is not valid UTF-8: ${filePath}`
          : `Cannot read file: ${filePath}`,
      );
    }

    if (Array.isArray(args.edits)) {
      return handleBatchEdits(filePath, content, sourceBytes, bom, args.edits as Record<string, unknown>[]);
    }

    const hasRange = args.start_line !== undefined && args.start_line !== null;
    const hasStr =
      args.old_string !== undefined &&
      args.old_string !== null &&
      String(args.old_string).length > 0;

    if (hasRange && hasStr) {
      return err("pass either old_string/new_string or start_line/end_line/content, not both");
    }

    const hashErr = await checkExpectedHash(filePath, args.expected_hash);
    if (hashErr) return hashErr;

    if (hasRange) {
      const startLine = Number(args.start_line);
      const endLine = Number(args.end_line);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine)) {
        return err("start_line and end_line must be integers for range mode");
      }
      if (args.content === undefined || args.content === null) {
        return err("content is required for range mode (use empty string to delete lines)");
      }
      const result = applyRangeEdit(content, startLine, endLine, String(args.content));
      if (!result.ok) return err(result.error);

      const writeErr = await writeEdited(filePath, sourceBytes, bom, result.content);
      if (writeErr) return writeErr;

      const replacement = String(args.content);
      const kind =
        endLine === startLine - 1 ? "insert" : replacement.length === 0 ? "delete" : "range";
      return ok(`Edited ${filePath} (${kind} lines ${startLine}-${endLine})`);
    }

    const oldString = String(args.old_string ?? "");
    const newString = String(args.new_string ?? "");
    const replaceAll = args.replace_all === true;

    if (oldString === newString) {
      return err("old_string and new_string are identical");
    }
    if (oldString.length === 0) {
      return err("old_string cannot be empty (or use start_line/end_line/content for range mode)");
    }

    const match = findReplacement(content, oldString, replaceAll);
    if (match.error || match.search === undefined) {
      if (content.includes(newString)) {
        return err("old_string not found — new_string already present, edit already applied?");
      }
      if (match.nonUnique) {
        return err("old_string is not unique (2+ matches). Add more context or set replace_all.");
      }
      return err(
        `${match.error ?? "old_string not found in file"}.${findClosestLineHint(content, oldString)}`,
      );
    }

    const search = match.search;
    const replacement = adaptLineEndings(search, newString);

    let updated: string;
    let count: number;
    if (replaceAll) {
      updated = content.split(search).join(replacement);
      count = content.split(search).length - 1;
    } else {
      updated =
        content.slice(0, match.index!) +
        replacement +
        content.slice(match.index! + search.length);
      count = 1;
    }

    const writeErr = await writeEdited(filePath, sourceBytes, bom, updated);
    if (writeErr) return writeErr;

    return ok(`Edited ${filePath} (${count} replacement${count === 1 ? "" : "s"})`);
  },
};

async function handleBatchEdits(
  filePath: string,
  content: string,
  sourceBytes: Uint8Array,
  bom: boolean,
  edits: Record<string, unknown>[],
): Promise<ToolResult> {
  if (edits.length === 0) {
    return err("edits array is empty");
  }

  for (let i = 0; i < edits.length; i++) {
    const hashErr = await checkExpectedHash(filePath, edits[i].expected_hash);
    if (hashErr) {
      const msg = hashErr.content[0];
      const text = msg.type === "text" ? msg.text.replace(/^Error: /, "") : "expected_hash check failed";
      return err(`edits[${i}]: ${text}`);
    }
  }

  const ranges: RangeEdit[] = [];
  const strEdits: { index: number; edit: StrEdit }[] = [];

  for (let i = 0; i < edits.length; i++) {
    const raw = edits[i];
    const range = isRangeShape(raw);
    const str = isStrShape(raw);

    if (range && str) {
      return err(`edits[${i}]: pass either old_string/new_string or start_line/end_line/content, not both`);
    }
    if (!range && !str) {
      return err(`edits[${i}]: need old_string/new_string or start_line/end_line/content`);
    }

    if (range) {
      const startLine = Number(raw.start_line);
      const endLine = Number(raw.end_line);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine)) {
        return err(`edits[${i}]: start_line and end_line must be integers`);
      }
      if (raw.content === undefined || raw.content === null) {
        return err(`edits[${i}]: content is required for range mode`);
      }
      ranges.push({
        start_line: startLine,
        end_line: endLine,
        content: String(raw.content),
      });
      continue;
    }

    const edit = raw as unknown as StrEdit;
    if (edit.old_string === edit.new_string) {
      return err(`edits[${i}]: old_string and new_string are identical`);
    }
    strEdits.push({ index: i, edit });
  }

  // Non-insert range intervals must not overlap (on original line numbers).
  const intervals = ranges
    .filter((r) => r.end_line >= r.start_line)
    .map((r) => ({ s: r.start_line, e: r.end_line }))
    .sort((a, b) => a.s - b.s);
  for (let i = 1; i < intervals.length; i++) {
    if (intervals[i].s <= intervals[i - 1].e) {
      return err("edits overlap. Merge them into one edit or target disjoint regions.");
    }
  }

  let updated = content;

  // Apply ranges high→low so lower line numbers stay valid.
  const rangesHighFirst = [...ranges].sort((a, b) => b.start_line - a.start_line);
  for (const r of rangesHighFirst) {
    const result = applyRangeEdit(updated, r.start_line, r.end_line, r.content);
    if (!result.ok) return err(result.error);
    updated = result.content;
  }

  if (strEdits.length > 0 && ranges.length === 0) {
    // Pure str-replace batch — original reverse-position path (stable indices).
    const matched: MatchedEdit[] = [];
    for (const { index, edit } of strEdits) {
      const match = findReplacement(content, edit.old_string, edit.replace_all ?? false);
      if (match.error || match.search === undefined) {
        if (match.nonUnique) {
          return err(
            `edits[${index}]: old_string is not unique (2+ matches). Add more context or set replace_all.`,
          );
        }
        return err(`edits[${index}]: ${match.error ?? "old_string not found in file."}`);
      }
      matched.push({
        matchIndex: match.index!,
        matchLength: match.search.length,
        new_string: adaptLineEndings(match.search, edit.new_string),
      });
    }
    matched.sort((a, b) => a.matchIndex - b.matchIndex);
    for (let i = 1; i < matched.length; i++) {
      const prev = matched[i - 1];
      const curr = matched[i];
      if (prev.matchIndex + prev.matchLength > curr.matchIndex) {
        return err("edits overlap. Merge them into one edit or target disjoint regions.");
      }
    }
    updated = content;
    for (let i = matched.length - 1; i >= 0; i--) {
      const m = matched[i];
      updated =
        updated.substring(0, m.matchIndex) +
        m.new_string +
        updated.substring(m.matchIndex + m.matchLength);
    }
  } else if (strEdits.length > 0) {
    // After ranges, resolve str edits against the updated buffer (order as given).
    for (const { index, edit } of strEdits) {
      const match = findReplacement(updated, edit.old_string, edit.replace_all ?? false);
      if (match.error || match.search === undefined) {
        if (match.nonUnique) {
          return err(`edits[${index}]: old_string is not unique after prior range edits.`);
        }
        return err(
          `edits[${index}]: ${match.error ?? "old_string not found after prior range edits."}`,
        );
      }
      const replacement = adaptLineEndings(match.search, edit.new_string);
      if (edit.replace_all) {
        updated = updated.split(match.search).join(replacement);
      } else {
        updated =
          updated.slice(0, match.index!) +
          replacement +
          updated.slice(match.index! + match.search.length);
      }
    }
  }

  const writeErr = await writeEdited(filePath, sourceBytes, bom, updated);
  if (writeErr) return writeErr;

  return ok(`Edited ${filePath} (${edits.length} edit${edits.length === 1 ? "" : "s"})`);
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
