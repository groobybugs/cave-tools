import { readFile } from "fs/promises";
import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  hashContent,
  invalidateFileCache,
  recordEdit,
  recordRangeEchoSaved,
} from "../compression/utils.js";
import { computeEditResult } from "./replacers.js";
import type { EditApplied } from "./edit-match.js";
import {
  activeEditMode,
  applyMoveLines,
  applyRangeEdit,
  verifyRangeChecksum,
} from "./line-range.js";
import { decodeUtf8PreserveBom, joinBom, withFileLocks, writeIfUnchanged } from "../runtime/file-mutation.js";
import { resolveMutationTarget } from "../runtime/path.js";

// Re-export for tests / external callers
export {
  applyRangeEdit,
  applyMoveLines,
  rangeChecksum,
  lineTag,
} from "./line-range.js";

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

function checkExpectedHash(
  expected: unknown,
  sourceBytes: Buffer,
): ToolResult | null {
  if (expected === undefined || expected === null) return null;
  const want = String(expected).trim().toLowerCase();
  if (want === "") return null;
  if (want.length < 8) {
    return err("expected_hash must be at least 8 hex characters");
  }
  if (!/^[0-9a-f]+$/.test(want)) {
    return err("expected_hash must be hexadecimal");
  }
  // Hash the exact bytes that were loaded for this edit (BOM included), so the
  // numbered-read footer and the edit check agree without a second file read.
  const actual = hashContent(sourceBytes);
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
  // `in`-narrowing (plain `!result.ok` does not narrow under some CLI builds).
  if (!("error" in result)) return err("checksum failed");
  return err(
    `${result.error}\n` +
      `expected: ${String(expected).trim().toLowerCase().slice(0, 16)}\n` +
      `actual:   ${result.actual}\n` +
      (result.window ? `window:\n${result.window}` : ""),
  );
}

/** True for plain editable argument objects (not null, arrays, or primitives). */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Shared field-type gate: present (non-null) argument values must already
 * have their declared schema types instead of being silently coerced.
 * label is "" for single-edit args or "edits[i]" for batch items.
 * (new_string is checked separately so its pinned "is required" message wins.)
 */
function checkFieldTypes(raw: Record<string, unknown>, label: string): ToolResult | null {
  const prefix = label === "" ? "" : `${label}: `;
  for (const key of ["start_line", "end_line", "insert_before"]) {
    const v = raw[key];
    if (v !== undefined && v !== null && typeof v !== "number") {
      return err(`${prefix}${key} must be a number`);
    }
  }
  for (const key of ["replace_all", "delete"]) {
    const v = raw[key];
    if (v !== undefined && v !== null && typeof v !== "boolean") {
      return err(`${prefix}${key} must be a boolean`);
    }
  }
  for (const key of ["content", "old_string", "file_path"]) {
    const v = raw[key];
    if (v !== undefined && v !== null && typeof v !== "string") {
      return err(`${prefix}${key} must be a string`);
    }
  }
  return null;
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
    "Edit by fuzzy old_string→new_string, or by inclusive 1-based line range from cave__read line_numbers=true " +
    "(start_line/end_line + content, delete, or insert_before to move). " +
    "expected_hash/expected_range_checksum reject stale edits; strict mode requires one for range/move. " +
    "Batch via edits[]; order ranges high→low.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path; default for edits[] items",
      },
      old_string: {
        type: "string",
        description: "Not combinable with range/move",
      },
      new_string: {
        type: "string",
      },
      replace_all: {
        type: "boolean",
        description: "Replace all, not just a unique match",
      },
      start_line: {
        type: "number",
      },
      end_line: {
        type: "number",
        description: "start_line-1 with content inserts before start_line",
      },
      content: {
        type: "string",
        description: "New range body, no line prefixes; empty deletes",
      },
      delete: {
        type: "boolean",
        description: "Delete the range",
      },
      insert_before: {
        type: "number",
        description: "Move range before this line (lineCount+1 = end)",
      },
      expected_hash: {
        type: "string",
        description: "File sha256 or ≥8-char prefix",
      },
      expected_range_checksum: {
        type: "string",
        description: "range_checksum of the edited span",
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
        description: "Batch; items may set file_path",
      },
    },
    required: [],
  },
  handler: async (args) => {
    if (!isRecord(args)) {
      return err("edit arguments must be an object");
    }
    if (args.edits !== undefined && !Array.isArray(args.edits)) {
      return err("edits must be an array of edit objects");
    }
    if (Array.isArray(args.edits)) {
      return handleBatchEdits(args);
    }

    let filePath = "";
    if (args.file_path !== undefined && args.file_path !== null) {
      if (typeof args.file_path !== "string") {
        return err("file_path must be a string");
      }
      filePath = args.file_path;
    }
    if (!filePath) return err("file_path is required");

    const fieldErr = checkFieldTypes(args, "");
    if (fieldErr) return fieldErr;

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
    if (hasStr && typeof args.new_string !== "string") {
      return err('new_string is required for string mode and must be a string (use "" to delete)');
    }

    let canonical: string;
    try {
      canonical = (await resolveMutationTarget(filePath)).canonical;
    } catch (error) {
      return err(error instanceof Error ? error.message : `Cannot resolve path: ${filePath}`);
    }

    // Read→compute→write→cache-invalidation runs under the canonical outer
    // lock (lock errors are tool errors, never uncaught).
    let singleResult: ToolResult;
    try {
      singleResult = await withFileLocks([canonical], async () => {
    const loaded = await loadFile(filePath);
    if (isToolResult(loaded)) return loaded;
    const { content, sourceBytes, bom } = loaded;

    const hashErr = checkExpectedHash(args.expected_hash, sourceBytes);
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
      if (!result.ok) return err("error" in result ? result.error : "move failed");
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
      if (!result.ok) return err("error" in result ? result.error : "range edit failed");
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

    // Str-replace. Per-span replacements, region-aware EOL, endingsOnly
    // handling, and computed-unchanged rejection all come from the shared
    // matcher (no first-literal split/join, no whole-file EOL normalization).
    const oldString = args.old_string as string;
    const newString = args.new_string as string;
    const replaceAll = args.replace_all === true;

    if (oldString === newString) {
      return err("old_string and new_string are identical");
    }
    if (oldString.length === 0) {
      return err("old_string cannot be empty");
    }

    const single = computeEditResult(content, oldString, newString, replaceAll);
    if (!single.ok) {
      if ("nonUnique" in single && single.nonUnique) {
        return err("old_string is not unique (2+ matches). Add more context or set replace_all.");
      }
      if (content.includes(newString)) {
        return err("old_string not found — new_string already present, edit already applied?");
      }
      return err(`${"error" in single ? single.error : "edit failed"}${findClosestLineHint(content, oldString)}`);
    }

    const writeErr = await writeEdited(filePath, sourceBytes, bom, single.updated);
    if (writeErr) return writeErr;

    const count = single.replacements;
    return ok(`Edited ${filePath} (${count} replacement${count === 1 ? "" : "s"})`);
      });
    } catch (error) {
      return err(error instanceof Error ? error.message : "edit failed");
    }
    return singleResult;
  },
};

async function applyEditsToContent(
  filePath: string,
  content: string,
  edits: Record<string, unknown>[],
  indices?: number[],
  fileHashProvided = false,
): Promise<{ ok: true; content: string } | ToolResult> {
  const ranges: (RangeEdit & { index: number })[] = [];
  const moves: (MoveEdit & { index: number })[] = [];
  const strEdits: { index: number; edit: StrEdit }[] = [];

  for (let i = 0; i < edits.length; i++) {
    const globalIndex = indices?.[i] ?? i;
    const raw = edits[i];
    const move = isMoveShape(raw);
    const range = isRangeShape(raw) || (raw.delete === true && raw.start_line !== undefined);
    const str = isStrShape(raw);

    const kinds = [move, range && !move, str].filter(Boolean).length;
    if (kinds > 1) {
      return err(`edits[${globalIndex}]: pass only one of str-replace, range/delete, or move`);
    }
    if (kinds === 0) {
      return err(
        `edits[${globalIndex}]: need old_string/new_string, start_line+content|delete, or start_line+insert_before`,
      );
    }
    if (str && typeof raw.new_string !== "string") {
      return err(
        `edits[${globalIndex}]: new_string is required and must be a string (use "" to delete)`,
      );
    }

    if (move) {
      const startLine = Number(raw.start_line);
      const endLine = Number(raw.end_line);
      const insertBefore = Number(raw.insert_before);
      if (!Number.isInteger(startLine) || !Number.isInteger(endLine) || !Number.isInteger(insertBefore)) {
        return err(`edits[${globalIndex}]: start_line, end_line, insert_before must be integers`);
      }
      const proofErr = requireProofInStrict(
        fileHashProvided ||
          (raw.expected_hash !== undefined && String(raw.expected_hash).trim() !== ""),
        raw.expected_range_checksum !== undefined &&
          String(raw.expected_range_checksum).trim() !== "",
      );
      if (proofErr) return err(`edits[${globalIndex}]: ${proofErr.content[0].type === "text" ? proofErr.content[0].text.replace(/^Error: /, "") : "strict proof required"}`);
      moves.push({
        index: globalIndex,
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
        return err(`edits[${globalIndex}]: start_line and end_line must be integers`);
      }
      if (raw.delete !== true && raw.content === undefined) {
        return err(`edits[${globalIndex}]: content is required (or delete:true)`);
      }
      const proofErr = requireProofInStrict(
        fileHashProvided ||
          (raw.expected_hash !== undefined && String(raw.expected_hash).trim() !== ""),
        raw.expected_range_checksum !== undefined &&
          String(raw.expected_range_checksum).trim() !== "",
      );
      if (proofErr) {
        const msg = proofErr.content[0];
        return err(
          `edits[${globalIndex}]: ${msg.type === "text" ? msg.text.replace(/^Error: /, "") : "strict proof required"}`,
        );
      }
      ranges.push({
        index: globalIndex,
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
      return err(`edits[${globalIndex}]: old_string and new_string are identical`);
    }
    strEdits.push({ index: globalIndex, edit });
  }

  // Minimal safe rule: more than one move per file batch would silently
  // operate on stale coordinates (each move renumbers the lines the next
  // move names). Reject with split-call advice instead of a fancy engine.
  if (moves.length > 1) {
    return err(
      "multiple moves in one batch for the same file are not supported; split into separate calls (each move changes line numbers for the next)",
    );
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

  // An insert whose point falls inside a replaced range is ambiguous: the
  // point names original lines, but the span they belong to is rewritten
  // (the range start boundary counts as inside — ordering alone would decide
  // whether the insert lands before or inside the new content).
  const insertSpans = ranges.filter((r) => r.end_line === r.start_line - 1);
  for (const ins of insertSpans) {
    for (const r of intervals) {
      if (ins.start_line >= r.s && ins.start_line <= r.e) {
        return err(
          `edits[${ins.index}]: insert point ${ins.start_line} lies inside replaced range ${r.s}-${r.e} (edits[${r.i}]); merge them or move the insert outside the range`,
        );
      }
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
    if (!result.ok) return err(`edits[${m.index}]: ${"error" in result ? result.error : "move failed"}`);
    recordRangeEchoSaved(result.movedChars);
    updated = result.content;
  }

  // If we applied moves, range line numbers on original may be stale when mixed.
  // Require: no mix of move+range in same file batch (simpler, safe).
  if (moves.length > 0 && ranges.length > 0) {
    return err("cannot mix move and range edits in one batch for the same file; split calls");
  }

  // High line numbers first so earlier spans keep original coordinates.
  // Explicit input-order tiebreak: same-point inserts apply deterministically.
  const rangesHighFirst = [...ranges].sort(
    (a, b) => b.start_line - a.start_line || a.index - b.index,
  );
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
    if (!result.ok) return err(`edits[${r.index}]: ${"error" in result ? result.error : "range edit failed"}`);
    recordRangeEchoSaved(result.replacedChars);
    updated = result.content;
  }

  if (strEdits.length > 0 && ranges.length === 0 && moves.length === 0) {
    // Match every applied span per item against the ORIGINAL snapshot, then
    // globally overlap-check source coordinates, then apply in reverse, so
    // disjoint items never observe each other's changes. Per-span
    // replacements come from the shared matcher (unicode/trailing variants
    // each change, not just the first literal shape).
    const allSpans: EditApplied[] = [];
    for (const { index, edit } of strEdits) {
      if (edit.old_string === edit.new_string) {
        return err(`edits[${index}]: old_string and new_string are identical`);
      }
      const r = computeEditResult(content, edit.old_string, edit.new_string, edit.replace_all ?? false);
      if (!r.ok) {
        if ("nonUnique" in r && r.nonUnique) {
          return err(
            `edits[${index}]: old_string is not unique (2+ matches). Add more context or set replace_all.`,
          );
        }
        return err(`edits[${index}]: ${"error" in r ? r.error : "string edit failed"}`);
      }
      allSpans.push(...r.applied);
    }
    allSpans.sort((a, b) => a.start - b.start);
    for (let i = 1; i < allSpans.length; i++) {
      const prev = allSpans[i - 1];
      const curr = allSpans[i];
      if (prev.end > curr.start) {
        return err("edits overlap. Merge them into one edit or target disjoint regions.");
      }
    }
    updated = content;
    for (let i = allSpans.length - 1; i >= 0; i--) {
      const m = allSpans[i];
      updated = updated.substring(0, m.start) + m.replacement + updated.substring(m.end);
    }
  } else if (strEdits.length > 0) {
    for (const { index, edit } of strEdits) {
      if (edit.old_string === edit.new_string) {
        return err(`edits[${index}]: old_string and new_string are identical`);
      }
      // Sequential on updated content (current mixed-batch semantics), but
      // each item still gets per-span replacements, endingsOnly handling,
      // and computed-unchanged rejection from the shared matcher.
      const r = computeEditResult(updated, edit.old_string, edit.new_string, edit.replace_all ?? false);
      if (!r.ok) {
        if ("nonUnique" in r && r.nonUnique) {
          return err(`edits[${index}]: old_string is not unique after prior edits.`);
        }
        return err(`edits[${index}]: ${"error" in r ? r.error : "string edit failed"}`);
      }
      updated = r.updated;
    }
  }

  void filePath; // used by callers for messaging
  return { ok: true, content: updated };
}

function validateBatchItemShape(index: number, value: unknown): ToolResult | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return err(`edits[${index}]: must be an edit object`);
  }
  const raw = value as Record<string, unknown>;
  const move = isMoveShape(raw);
  const range = isRangeShape(raw) || (raw.delete === true && raw.start_line !== undefined);
  const str = isStrShape(raw);
  const kinds = [move, range && !move, str].filter(Boolean).length;
  if (kinds > 1) {
    return err(`edits[${index}]: pass only one of str-replace, range/delete, or move`);
  }
  if (kinds === 0) {
    return err(
      `edits[${index}]: need old_string/new_string, start_line+content|delete, or start_line+insert_before`,
    );
  }
  if (str && typeof raw.new_string !== "string") {
    return err(`edits[${index}]: new_string is required and must be a string (use "" to delete)`);
  }
  return null;
}

function errorText(result: ToolResult): string {
  const first = result.content[0];
  return first && first.type === "text" ? first.text.replace(/^Error: /, "") : "operation failed";
}

async function handleBatchEdits(args: Record<string, unknown>): Promise<ToolResult> {
  const rawEdits = args.edits;
  if (!Array.isArray(rawEdits) || rawEdits.length === 0) {
    return err("edits array is empty");
  }
  const edits = rawEdits as Record<string, unknown>[];

  const topTypeErr = checkFieldTypes(args, "");
  if (topTypeErr) return topTypeErr;

  let defaultPath = "";
  if (args.file_path !== undefined && args.file_path !== null) {
    if (typeof args.file_path !== "string") {
      return err("file_path must be a string");
    }
    defaultPath = args.file_path;
  }

  // Validate every item before any file read/write. A malformed later item
  // must not leave earlier files written. Non-objects keep the pinned shape
  // message; objects get precise field-type errors before kind errors.
  for (let i = 0; i < edits.length; i++) {
    if (!isRecord(edits[i])) {
      return err(`edits[${i}]: must be an edit object`);
    }
    const typeErr = checkFieldTypes(edits[i], `edits[${i}]`);
    if (typeErr) return typeErr;
    const shapeErr = validateBatchItemShape(i, edits[i]);
    if (shapeErr) return shapeErr;
  }

  // Group by file_path
  const byFile = new Map<string, { index: number; edit: Record<string, unknown> }[]>();
  for (let i = 0; i < edits.length; i++) {
    const raw = edits[i];
    const fp =
      raw.file_path !== undefined && raw.file_path !== null && (raw.file_path as string).length > 0
        ? (raw.file_path as string)
        : defaultPath;
    if (!fp) {
      return err(`edits[${i}]: file_path required (set top-level file_path or per-item file_path)`);
    }
    const list = byFile.get(fp) || [];
    list.push({ index: i, edit: raw });
    byFile.set(fp, list);
  }

  const topLevelHash =
    args.expected_hash !== undefined &&
    args.expected_hash !== null &&
    String(args.expected_hash).trim() !== "";
  if (byFile.size > 1 && topLevelHash) {
    return err(
      "top-level expected_hash applies only to single-file batches; " +
        "set expected_hash on each edit item for multi-file batches",
    );
  }

  // Resolve every literal path to its canonical target BEFORE reading.
  // Distinct spellings of the same file (sub/../f.txt vs f.txt, symlinks)
  // would otherwise plan and write twice; reject with a clear error.
  // Paths were shape-validated above, so resolution failures are tool errors.
  const canonicalFp = new Map<string, { fp: string; index: number }>();
  for (const [fp, items] of byFile) {
    let canonical: string;
    try {
      canonical = (await resolveMutationTarget(fp)).canonical;
    } catch (error) {
      return err(error instanceof Error ? error.message : `Cannot resolve path: ${fp}`);
    }
    const clash = canonicalFp.get(canonical);
    if (clash && clash.fp !== fp) {
      return err(
        `edits[${items[0].index}] and edits[${clash.index}] target the same file ` +
          `via different paths ("${clash.fp}" and "${fp}"); use one path per file`,
      );
    }
    if (!clash) canonicalFp.set(canonical, { fp, index: items[0].index });
  }
  const lockPaths = [...canonicalFp.keys()].sort();

  // Everything from the first read through the last write and cache
  // invalidation runs under one canonical path-sorted outer lock, so
  // concurrent batches over disjoint spans of the same file serialize
  // instead of racing on stale snapshots. Lock errors are tool errors.
  let batchResult: ToolResult;
  try {
    batchResult = await withFileLocks(lockPaths, async () => {
  // Load and validate every file before writing any of them.
  const loadedFiles: {
    filePath: string;
    content: string;
    sourceBytes: Buffer;
    bom: boolean;
    items: { index: number; edit: Record<string, unknown> }[];
  }[] = [];
  for (const [filePath, items] of byFile) {
    const loaded = await loadFile(filePath);
    if (isToolResult(loaded)) return loaded;

    for (const { index, edit } of items) {
      const hashErr = checkExpectedHash(edit.expected_hash, loaded.sourceBytes);
      if (hashErr) {
        return err(`edits[${index}]: ${errorText(hashErr)}`);
      }
    }
    if (byFile.size === 1 && topLevelHash) {
      const hashErr = checkExpectedHash(args.expected_hash, loaded.sourceBytes);
      if (hashErr) return hashErr;
    }

    loadedFiles.push({ filePath, ...loaded, items });
  }

  // Compute every file's new content before the first write.
  const plans: {
    filePath: string;
    sourceBytes: Buffer;
    bom: boolean;
    updated: string;
    itemCount: number;
  }[] = [];
  for (const file of loadedFiles) {
    const result = await applyEditsToContent(
      file.filePath,
      file.content,
      file.items.map((x) => x.edit),
      file.items.map((x) => x.index),
      byFile.size === 1 && topLevelHash,
    );
    if (!("ok" in result) || result.ok !== true) {
      return result as ToolResult;
    }
    plans.push({
      filePath: file.filePath,
      sourceBytes: file.sourceBytes,
      bom: file.bom,
      updated: result.content,
      itemCount: file.items.length,
    });
  }

  const summaries: string[] = [];
  const written: string[] = [];
  for (let i = 0; i < plans.length; i++) {
    const plan = plans[i];
    const writeErr = await writeEdited(plan.filePath, plan.sourceBytes, plan.bom, plan.updated);
    if (writeErr) {
      const notAttempted = plans.slice(i + 1).map((p) => p.filePath);
      return err(
        `${errorText(writeErr)}\n` +
          `Failed file (may be partially modified): ${plan.filePath}\n` +
          `Files written before failure: ${written.length ? written.join(", ") : "(none)"}\n` +
          `Files not attempted: ${notAttempted.length ? notAttempted.join(", ") : "(none)"}`,
      );
    }
    written.push(plan.filePath);
    summaries.push(`${plan.filePath} (${plan.itemCount})`);
  }

  return ok(
    `Edited ${summaries.length} file${summaries.length === 1 ? "" : "s"}: ${summaries.join(", ")}`,
  );
    });
  } catch (error) {
    return err(error instanceof Error ? error.message : "edit batch failed");
  }
  return batchResult;
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
