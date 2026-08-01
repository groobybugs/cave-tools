import { readFile } from "fs/promises";
import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  getFileHash,
  invalidateFileCache,
  recordEdit,
  recordRangeEchoSaved,
} from "../compression/utils.js";
import { findReplacement } from "./replacers.js";
import {
  activeEditMode,
  applyMoveLines,
  applyRangeEdit,
  verifyRangeChecksum,
} from "./line-range.js";
import { decodeUtf8PreserveBom, joinBom, writeIfUnchanged } from "../runtime/file-mutation.js";

// Re-export for tests / external callers
export {
  applyRangeEdit,
  applyMoveLines,
  rangeChecksum,
  lineTag,
} from "./line-range.js";

// Adapt new_string line endings to match the matched span's style so an edit
// against a CRLF file doesn't inject lone LFs (and vice-versa).
function adaptLineEndings(search: string, newString: string): string {
  const searchCRLF = search.includes("\r\n");
  const newCRLF = newString.includes("\r\n");
  if (searchCRLF && !newCRLF) return newString.replace(/\n/g, "\r\n");
  if (!searchCRLF && newCRLF) return newString.replace(/\r\n/g, "\n");
  return newString;
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
  expected_range_checksum?: string;
}

interface MoveEdit {
  start_line: number;
  end_line: number;
  insert_before: number;
  expected_range_checksum?: string;
}

interface MatchedEdit {
  matchIndex: number;
  matchLength: number;
  new_string: string;
}

function isMoveShape(edit: Record<string, unknown>): boolean {
  return (
    edit.insert_before !== undefined &&
    edit.insert_before !== null &&
    edit.start_line !== undefined &&
    edit.start_line !== null
  );
}

function isRangeShape(edit: Record<string, unknown>): boolean {
  return (
    edit.start_line !== undefined &&
    edit.start_line !== null &&
    !isMoveShape(edit) &&
    (edit.content !== undefined || edit.delete === true)
  );
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

function checkRangeChecksumOrError(
  content: string,
  startLine: number,
  endLine: number,
  expected: unknown,
): ToolResult | null {
  if (expected === undefined || expected === null || String(expected).trim() === "") {
    return null;
  }
  const result = verifyRangeChecksum(content, startLine, endLine, String(expected));
  if (result.ok) return null;
  return err(
    `${result.error}\n` +
      `expected: ${String(expected).trim().toLowerCase().slice(0, 16)}\n` +
      `actual:   ${result.actual}\n` +
      (result.window ? `window:\n${result.window}` : ""),
  );
}

function requireProofInStrict(
  hasFileHash: boolean,
  hasRangeChecksum: boolean,
): ToolResult | null {
  if (activeEditMode() !== "strict") return null;
  if (hasFileHash || hasRangeChecksum) return null;
  return err(
    "STRICT: range/move edit requires expected_hash or expected_range_checksum " +
      "(from cave__read line_numbers footer)",
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

async function loadFile(
  filePath: string,
): Promise<{ content: string; sourceBytes: Buffer; bom: boolean } | ToolResult> {
  try {
    const sourceBytes = await readFile(filePath);
    const decoded = decodeUtf8PreserveBom(sourceBytes);
    return { content: decoded.text, sourceBytes, bom: decoded.bom };
  } catch (error) {
    return err(
      error instanceof Error && error.message.includes("encoded data")
        ? `File is not valid UTF-8: ${filePath}`
        : `Cannot read file: ${filePath}`,
    );
  }
}

function isToolResult(v: unknown): v is ToolResult {
  return (
    typeof v === "object" &&
    v !== null &&
    "content" in v &&
    Array.isArray((v as ToolResult).content)
  );
}

export const editTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__edit",
  description:
    "Edit a file by search/replace, line-range, or move. " +
    "Str-replace: old_string → new_string (fuzzy). " +
    "Line-range (token-cheap): start_line + end_line + content (new body only). " +
    "delete: true with start/end deletes lines. insert_before with start/end moves block. " +
    "Inclusive 1-based lines; tags/checksums from cave__read line_numbers=true. " +
    "expected_hash (file) and expected_range_checksum (span) reject stale edits. " +
    "STRICT mode (CAVE_TOOLS_MODE=strict) requires one of those proofs for range/move. " +
    "Batch via edits[] (mixed; optional per-item file_path for multi-file). Ranges high→low.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to modify (default for batch items without file_path)",
      },
      old_string: {
        type: "string",
        description: "Text to replace (fuzzy). Mutually exclusive with range/move.",
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
        description: "1-based inclusive start line for range/move/delete",
      },
      end_line: {
        type: "number",
        description: "1-based inclusive end line. Use start_line-1 with content to insert before start_line.",
      },
      content: {
        type: "string",
        description: "Replacement body for range mode (multi-line OK; empty deletes). No line-number prefixes.",
      },
      delete: {
        type: "boolean",
        description: "If true, delete lines [start_line, end_line] (same as content:\"\")",
      },
      insert_before: {
        type: "number",
        description: "Move lines [start_line, end_line] to insert before this 1-based line (lineCount+1 = end)",
      },
      expected_hash: {
        type: "string",
        description: "Optional file sha256 (full or ≥8-char prefix from cave__read footer)",
      },
      expected_range_checksum: {
        type: "string",
        description: "Optional span checksum from cave__read footer (range_checksum). Verifies only the edited lines.",
      },
      edits: {
        type: "array",
        items: {
          type: "object",
          properties: {
            file_path: { type: "string" },
            old_string: { type: "string" },
            new_string: { type: "string" },
            replace_all: { type: "boolean" },
            start_line: { type: "number" },
            end_line: { type: "number" },
            content: { type: "string" },
            delete: { type: "boolean" },
            insert_before: { type: "number" },
            expected_hash: { type: "string" },
            expected_range_checksum: { type: "string" },
          },
        },
        description: "Multiple edits (str/range/move). Optional file_path per item for multi-file batch.",
      },
    },
    required: [],
  },
  handler: async (args) => {
    if (Array.isArray(args.edits)) {
      return handleBatchEdits(args);
    }

    const filePath = args.file_path !== undefined ? String(args.file_path) : "";
    if (!filePath) return err("file_path is required");

    const loaded = await loadFile(filePath);
    if (isToolResult(loaded)) return loaded;
    const { content, sourceBytes, bom } = loaded;

    const hasMove = isMoveShape(args);
    const hasRange =
      args.start_line !== undefined &&
      args.start_line !== null &&
      !hasMove &&
      (args.content !== undefined || args.delete === true);
    const hasStrKey = args.old_string !== undefined && args.old_string !== null;
    const hasStr = hasStrKey && String(args.old_string).length > 0;

    if (hasStrKey && !hasStr && !hasRange && !hasMove) {
      return err("old_string cannot be empty (or use start_line/end_line/content for range mode)");
    }

    const modes = [hasMove, hasRange, hasStr].filter(Boolean).length;
    if (modes > 1) {
      return err("pass either old_string/new_string or start_line/end_line/content (or delete/insert_before), not both");
    }
    if (modes === 0) {
      return err("need old_string/new_string, or start_line/end_line with content|delete|insert_before");
    }

    const hashErr = await checkExpectedHash(filePath, args.expected_hash);
    if (hashErr) return hashErr;

    if (hasMove || hasRange) {
      const proofErr = requireProofInStrict(
        args.expected_hash !== undefined && String(args.expected_hash).trim() !== "",
        args.expected_range_checksum !== undefined &&
          String(args.expected_range_checksum).trim() !== "",
      );
      if (proofErr) return proofErr;
    }

    if (hasMove) {
      const startLine = Number(args.start_line);
      const endLine = Number(args.end_line);
      const insertBefore = Number(args.insert_before);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine) || !Number.isInteger(insertBefore)) {
        return err("start_line, end_line, and insert_before must be integers");
      }
      const csErr = checkRangeChecksumOrError(
        content,
        startLine,
        endLine,
        args.expected_range_checksum,
      );
      if (csErr) return csErr;

      const result = applyMoveLines(content, startLine, endLine, insertBefore);
      if (!result.ok) return err(result.error);
      recordRangeEchoSaved(result.movedChars);

      const writeErr = await writeEdited(filePath, sourceBytes, bom, result.content);
      if (writeErr) return writeErr;
      return ok(
        `Edited ${filePath} (move lines ${startLine}-${endLine} → before ${insertBefore})`,
      );
    }

    if (hasRange) {
      const startLine = Number(args.start_line);
      const endLine = Number(args.end_line);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine)) {
        return err("start_line and end_line must be integers for range mode");
      }
      const replacement = args.delete === true ? "" : String(args.content ?? "");
      if (args.delete !== true && (args.content === undefined || args.content === null)) {
        return err("content is required for range mode (or set delete:true)");
      }

      const csErr = checkRangeChecksumOrError(
        content,
        startLine,
        endLine,
        args.expected_range_checksum,
      );
      if (csErr) return csErr;

      const result = applyRangeEdit(content, startLine, endLine, replacement);
      if (!result.ok) return err(result.error);
      recordRangeEchoSaved(result.replacedChars);

      const writeErr = await writeEdited(filePath, sourceBytes, bom, result.content);
      if (writeErr) return writeErr;

      const kind =
        endLine === startLine - 1
          ? "insert"
          : replacement.length === 0
            ? "delete"
            : "range";
      return ok(`Edited ${filePath} (${kind} lines ${startLine}-${endLine})`);
    }

    // Str-replace
    const oldString = String(args.old_string ?? "");
    const newString = String(args.new_string ?? "");
    const replaceAll = args.replace_all === true;

    if (oldString === newString) {
      return err("old_string and new_string are identical");
    }
    if (oldString.length === 0) {
      return err("old_string cannot be empty");
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

async function applyEditsToContent(
  filePath: string,
  content: string,
  edits: Record<string, unknown>[],
): Promise<{ ok: true; content: string } | ToolResult> {
  const ranges: (RangeEdit & { index: number })[] = [];
  const moves: (MoveEdit & { index: number })[] = [];
  const strEdits: { index: number; edit: StrEdit }[] = [];

  for (let i = 0; i < edits.length; i++) {
    const raw = edits[i];
    const move = isMoveShape(raw);
    const range = isRangeShape(raw) || (raw.delete === true && raw.start_line !== undefined);
    const str = isStrShape(raw);

    const kinds = [move, range && !move, str].filter(Boolean).length;
    if (kinds > 1) {
      return err(`edits[${i}]: pass only one of str-replace, range/delete, or move`);
    }
    if (kinds === 0) {
      return err(
        `edits[${i}]: need old_string/new_string, start_line+content|delete, or start_line+insert_before`,
      );
    }

    if (move) {
      const startLine = Number(raw.start_line);
      const endLine = Number(raw.end_line);
      const insertBefore = Number(raw.insert_before);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine) || !Number.isInteger(insertBefore)) {
        return err(`edits[${i}]: start_line, end_line, insert_before must be integers`);
      }
      const proofErr = requireProofInStrict(
        raw.expected_hash !== undefined && String(raw.expected_hash).trim() !== "",
        raw.expected_range_checksum !== undefined &&
          String(raw.expected_range_checksum).trim() !== "",
      );
      if (proofErr) return err(`edits[${i}]: ${proofErr.content[0].type === "text" ? proofErr.content[0].text.replace(/^Error: /, "") : "strict proof required"}`);
      moves.push({
        index: i,
        start_line: startLine,
        end_line: endLine,
        insert_before: insertBefore,
        expected_range_checksum:
          raw.expected_range_checksum !== undefined
            ? String(raw.expected_range_checksum)
            : undefined,
      });
      continue;
    }

    if (range || raw.delete === true) {
      const startLine = Number(raw.start_line);
      const endLine = Number(raw.end_line);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine)) {
        return err(`edits[${i}]: start_line and end_line must be integers`);
      }
      if (raw.delete !== true && raw.content === undefined) {
        return err(`edits[${i}]: content is required (or delete:true)`);
      }
      const proofErr = requireProofInStrict(
        raw.expected_hash !== undefined && String(raw.expected_hash).trim() !== "",
        raw.expected_range_checksum !== undefined &&
          String(raw.expected_range_checksum).trim() !== "",
      );
      if (proofErr) {
        const msg = proofErr.content[0];
        return err(
          `edits[${i}]: ${msg.type === "text" ? msg.text.replace(/^Error: /, "") : "strict proof required"}`,
        );
      }
      ranges.push({
        index: i,
        start_line: startLine,
        end_line: endLine,
        content: raw.delete === true ? "" : String(raw.content),
        expected_range_checksum:
          raw.expected_range_checksum !== undefined
            ? String(raw.expected_range_checksum)
            : undefined,
      });
      continue;
    }

    const edit = raw as unknown as StrEdit;
    if (edit.old_string === edit.new_string) {
      return err(`edits[${i}]: old_string and new_string are identical`);
    }
    strEdits.push({ index: i, edit });
  }

  // Overlap check for non-insert ranges on original lines
  const intervals = ranges
    .filter((r) => r.end_line >= r.start_line)
    .map((r) => ({ s: r.start_line, e: r.end_line, i: r.index }))
    .sort((a, b) => a.s - b.s);
  for (let i = 1; i < intervals.length; i++) {
    if (intervals[i].s <= intervals[i - 1].e) {
      return err("edits overlap. Merge them into one edit or target disjoint regions.");
    }
  }

  let updated = content;

  // Moves first (on original coordinates), high start_line first
  const movesSorted = [...moves].sort((a, b) => b.start_line - a.start_line);
  for (const m of movesSorted) {
    const csErr = checkRangeChecksumOrError(
      updated,
      m.start_line,
      m.end_line,
      m.expected_range_checksum,
    );
    if (csErr) {
      const msg = csErr.content[0];
      return err(
        `edits[${m.index}]: ${msg.type === "text" ? msg.text.replace(/^Error: /, "") : "checksum failed"}`,
      );
    }
    const result = applyMoveLines(updated, m.start_line, m.end_line, m.insert_before);
    if (!result.ok) return err(`edits[${m.index}]: ${result.error}`);
    recordRangeEchoSaved(result.movedChars);
    updated = result.content;
  }

  // If we applied moves, range line numbers on original may be stale when mixed.
  // Require: no mix of move+range in same file batch (simpler, safe).
  if (moves.length > 0 && ranges.length > 0) {
    return err("cannot mix move and range edits in one batch for the same file; split calls");
  }

  const rangesHighFirst = [...ranges].sort((a, b) => b.start_line - a.start_line);
  for (const r of rangesHighFirst) {
    const csErr = checkRangeChecksumOrError(
      updated,
      r.start_line,
      r.end_line,
      r.expected_range_checksum,
    );
    if (csErr) {
      const msg = csErr.content[0];
      return err(
        `edits[${r.index}]: ${msg.type === "text" ? msg.text.replace(/^Error: /, "") : "checksum failed"}`,
      );
    }
    const result = applyRangeEdit(updated, r.start_line, r.end_line, r.content);
    if (!result.ok) return err(`edits[${r.index}]: ${result.error}`);
    recordRangeEchoSaved(result.replacedChars);
    updated = result.content;
  }

  if (strEdits.length > 0 && ranges.length === 0 && moves.length === 0) {
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
    for (const { index, edit } of strEdits) {
      const match = findReplacement(updated, edit.old_string, edit.replace_all ?? false);
      if (match.error || match.search === undefined) {
        if (match.nonUnique) {
          return err(`edits[${index}]: old_string is not unique after prior edits.`);
        }
        return err(
          `edits[${index}]: ${match.error ?? "old_string not found after prior edits."}`,
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

  void filePath; // used by callers for messaging
  return { ok: true, content: updated };
}

async function handleBatchEdits(args: Record<string, unknown>): Promise<ToolResult> {
  const edits = args.edits as Record<string, unknown>[];
  if (!Array.isArray(edits) || edits.length === 0) {
    return err("edits array is empty");
  }

  const defaultPath = args.file_path !== undefined ? String(args.file_path) : "";

  // Group by file_path
  const byFile = new Map<string, { index: number; edit: Record<string, unknown> }[]>();
  for (let i = 0; i < edits.length; i++) {
    const raw = edits[i];
    const fp =
      raw.file_path !== undefined && raw.file_path !== null && String(raw.file_path).length > 0
        ? String(raw.file_path)
        : defaultPath;
    if (!fp) {
      return err(`edits[${i}]: file_path required (set top-level file_path or per-item file_path)`);
    }
    const list = byFile.get(fp) || [];
    list.push({ index: i, edit: raw });
    byFile.set(fp, list);
  }

  // Top-level expected_hash applies when single file
  if (byFile.size === 1 && args.expected_hash !== undefined) {
    const only = [...byFile.keys()][0];
    const hashErr = await checkExpectedHash(only, args.expected_hash);
    if (hashErr) return hashErr;
  }

  const summaries: string[] = [];

  for (const [filePath, items] of byFile) {
    // Per-item file hash checks
    for (const { index, edit } of items) {
      if (edit.expected_hash !== undefined) {
        const hashErr = await checkExpectedHash(filePath, edit.expected_hash);
        if (hashErr) {
          const msg = hashErr.content[0];
          return err(
            `edits[${index}]: ${msg.type === "text" ? msg.text.replace(/^Error: /, "") : "hash failed"}`,
          );
        }
      }
    }

    const loaded = await loadFile(filePath);
    if (isToolResult(loaded)) return loaded;
    const { content, sourceBytes, bom } = loaded;

    const fileEdits = items.map((x) => x.edit);
    const result = await applyEditsToContent(filePath, content, fileEdits);
    if (!("ok" in result) || result.ok !== true) {
      return result as ToolResult;
    }

    const writeErr = await writeEdited(filePath, sourceBytes, bom, result.content);
    if (writeErr) return writeErr;
    summaries.push(`${filePath} (${items.length})`);
  }

  return ok(
    `Edited ${summaries.length} file${summaries.length === 1 ? "" : "s"}: ${summaries.join(", ")}`,
  );
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
