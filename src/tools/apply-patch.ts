import { readFile, rm, stat, writeFile } from "fs/promises";
import path from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";
import { ensureParentDirectory, resolveMutationTarget, toPosixPath } from "../runtime/path.js";
import { decodeUtf8PreserveBom, joinBom, lockedWrite, writeIfUnchanged } from "../runtime/file-mutation.js";

interface UpdateFileChunk {
  old_lines: string[];
  new_lines: string[];
  change_context?: string;
  is_end_of_file?: boolean;
}

type Hunk =
  | { type: "add"; path: string; contents: string }
  | { type: "delete"; path: string }
  | { type: "update"; path: string; move_path?: string; chunks: UpdateFileChunk[] };

// PlannedChange represents one mutation, fully resolved and computed, ready
// for atomic application. Build every PlannedChange before writing anything;
// if planning throws, no files are touched.
type PlannedChange =
  | { type: "add"; path: string; target: string; content: string }
  | { type: "delete"; path: string; target: string }
  | {
      type: "update";
      path: string;
      target: string;
      sourceBytes: Uint8Array;
      content: string;
      bom: boolean;
    }
  | {
      type: "move";
      path: string;
      target: string;
      movePath: string;
      moveTarget: string;
      sourceBytes: Uint8Array;
      content: string;
      bom: boolean;
    };

// Normalize Unicode punctuation to ASCII equivalents (like Rust's normalize_unicode)
function normalizeUnicode(str: string): string {
  return str
    .replace(/[‘’‚‛]/g, "'")
    .replace(/[“”„‟]/g, '"')
    .replace(/[‐‑‒–—―]/g, "-")
    .replace(/…/g, "...")
    .replace(/\u00A0/g, " ");
}

type Comparator = (a: string, b: string) => boolean;

function tryMatch(
  lines: string[],
  pattern: string[],
  startIndex: number,
  compare: Comparator,
  eof: boolean,
): number {
  // If EOF anchor, try matching from end of file first
  if (eof) {
    const fromEnd = lines.length - pattern.length;
    if (fromEnd >= startIndex) {
      let matches = true;
      for (let j = 0; j < pattern.length; j++) {
        if (!compare(lines[fromEnd + j], pattern[j])) {
          matches = false;
          break;
        }
      }
      if (matches) return fromEnd;
    }
  }

  // Forward search from startIndex
  for (let i = startIndex; i <= lines.length - pattern.length; i++) {
    let matches = true;
    for (let j = 0; j < pattern.length; j++) {
      if (!compare(lines[i + j], pattern[j])) {
        matches = false;
        break;
      }
    }
    if (matches) return i;
  }

  return -1;
}

function seekSequence(lines: string[], pattern: string[], startIndex: number, eof = false): number {
  if (pattern.length === 0) return -1;

  // Pass 1: exact match
  const exact = tryMatch(lines, pattern, startIndex, (a, b) => a === b, eof);
  if (exact !== -1) return exact;

  // Pass 2: rstrip (trim trailing whitespace)
  const rstrip = tryMatch(lines, pattern, startIndex, (a, b) => a.trimEnd() === b.trimEnd(), eof);
  if (rstrip !== -1) return rstrip;

  // Pass 3: trim (both ends)
  const trim = tryMatch(lines, pattern, startIndex, (a, b) => a.trim() === b.trim(), eof);
  if (trim !== -1) return trim;

  // Pass 4: normalized (Unicode punctuation to ASCII)
  const normalized = tryMatch(
    lines,
    pattern,
    startIndex,
    (a, b) => normalizeUnicode(a.trim()) === normalizeUnicode(b.trim()),
    eof,
  );
  return normalized;
}

function parsePatchHeader(
  lines: string[],
  startIdx: number,
): { filePath: string; movePath?: string; nextIdx: number } | null {
  const line = lines[startIdx];

  if (line.startsWith("*** Add File:")) {
    const filePath = line.slice("*** Add File:".length).trim();
    return filePath ? { filePath, nextIdx: startIdx + 1 } : null;
  }

  if (line.startsWith("*** Delete File:")) {
    const filePath = line.slice("*** Delete File:".length).trim();
    return filePath ? { filePath, nextIdx: startIdx + 1 } : null;
  }

  if (line.startsWith("*** Update File:")) {
    const filePath = line.slice("*** Update File:".length).trim();
    let movePath: string | undefined;
    let nextIdx = startIdx + 1;

    if (nextIdx < lines.length && lines[nextIdx].startsWith("*** Move to:")) {
      movePath = lines[nextIdx].slice("*** Move to:".length).trim();
      nextIdx++;
    }

    return filePath ? { filePath, movePath, nextIdx } : null;
  }

  return null;
}

function parseUpdateFileChunks(lines: string[], startIdx: number): { chunks: UpdateFileChunk[]; nextIdx: number } {
  const chunks: UpdateFileChunk[] = [];
  let i = startIdx;

  // Backward-compatible whole-file blob: no @@ context headers. Cave extension.
  if (i < lines.length && !lines[i].startsWith("***") && !lines[i].startsWith("@@")) {
    const oldLines: string[] = [];
    const newLines: string[] = [];
    let isEndOfFile = false;
    while (i < lines.length && !lines[i].startsWith("***")) {
      const line = lines[i];
      if (line === "*** End of File") {
        isEndOfFile = true;
        i++;
        break;
      }
      if (line.startsWith(" ")) {
        const content = line.substring(1);
        oldLines.push(content);
        newLines.push(content);
      } else if (line.startsWith("-")) {
        oldLines.push(line.substring(1));
      } else if (line.startsWith("+")) {
        newLines.push(line.substring(1));
      }
      i++;
    }
    chunks.push({
      old_lines: oldLines,
      new_lines: newLines,
      is_end_of_file: isEndOfFile || undefined,
    });
    return { chunks, nextIdx: i };
  }

  while (i < lines.length && !lines[i].startsWith("***")) {
    if (lines[i].startsWith("@@")) {
      // Parse context line
      const contextLine = lines[i].substring(2).trim();
      i++;

      const oldLines: string[] = [];
      const newLines: string[] = [];
      let isEndOfFile = false;

      // Parse change lines
      while (i < lines.length && !lines[i].startsWith("@@") && !lines[i].startsWith("***")) {
        const changeLine = lines[i];

        if (changeLine === "*** End of File") {
          isEndOfFile = true;
          i++;
          break;
        }

        if (changeLine.startsWith(" ")) {
          const content = changeLine.substring(1);
          oldLines.push(content);
          newLines.push(content);
        } else if (changeLine.startsWith("-")) {
          oldLines.push(changeLine.substring(1));
        } else if (changeLine.startsWith("+")) {
          newLines.push(changeLine.substring(1));
        }

        i++;
      }

      chunks.push({
        old_lines: oldLines,
        new_lines: newLines,
        change_context: contextLine || undefined,
        is_end_of_file: isEndOfFile || undefined,
      });
    } else {
      i++;
    }
  }

  return { chunks, nextIdx: i };
}

function parseAddFileContent(lines: string[], startIdx: number): { content: string; nextIdx: number } {
  let content = "";
  let i = startIdx;

  while (i < lines.length && !lines[i].startsWith("***")) {
    if (lines[i].startsWith("+")) {
      content += lines[i].substring(1) + "\n";
    }
    i++;
  }

  // Remove trailing newline
  if (content.endsWith("\n")) {
    content = content.slice(0, -1);
  }

  return { content, nextIdx: i };
}

function stripHeredoc(input: string): string {
  // Match heredoc patterns like: cat <<'EOF'\n...\nEOF or <<EOF\n...\nEOF
  const heredocMatch = input.match(/^(?:cat\s+)?<<['"]?(\w+)['"]?\s*\n([\s\S]*?)\n\1\s*$/);
  if (heredocMatch) {
    return heredocMatch[2];
  }
  return input;
}

function parsePatch(patchText: string): { hunks: Hunk[] } {
  const cleaned = stripHeredoc(patchText.replace(/\r\n/g, "\n").trim());
  const lines = cleaned.split("\n");
  const hunks: Hunk[] = [];
  let i = 0;

  const beginIdx = lines.findIndex((line) => line.trim() === "*** Begin Patch");
  const endIdx = lines.findIndex((line) => line.trim() === "*** End Patch");

  if (beginIdx === -1 || endIdx === -1 || beginIdx >= endIdx) {
    throw new Error("Invalid patch format: missing Begin/End markers");
  }

  i = beginIdx + 1;

  while (i < endIdx) {
    const header = parsePatchHeader(lines, i);
    if (!header) {
      i++;
      continue;
    }

    if (lines[i].startsWith("*** Add File:")) {
      const { content, nextIdx } = parseAddFileContent(lines, header.nextIdx);
      hunks.push({ type: "add", path: header.filePath, contents: content });
      i = nextIdx;
    } else if (lines[i].startsWith("*** Delete File:")) {
      hunks.push({ type: "delete", path: header.filePath });
      i = header.nextIdx;
    } else if (lines[i].startsWith("*** Update File:")) {
      const { chunks, nextIdx } = parseUpdateFileChunks(lines, header.nextIdx);
      hunks.push({
        type: "update",
        path: header.filePath,
        move_path: header.movePath,
        chunks,
      });
      i = nextIdx;
    } else {
      i++;
    }
  }

  return { hunks };
}

function computeReplacements(
  originalLines: string[],
  filePath: string,
  chunks: UpdateFileChunk[],
): Array<[number, number, string[]]> {
  const replacements: Array<[number, number, string[]]> = [];
  let lineIndex = 0;

  for (const chunk of chunks) {
    // Handle context-based seeking
    if (chunk.change_context) {
      const contextIdx = seekSequence(originalLines, [chunk.change_context], lineIndex);
      if (contextIdx === -1) {
        throw new Error(`Failed to find context '${chunk.change_context}' in ${filePath}`);
      }
      lineIndex = contextIdx + 1;
    }

    // Handle pure addition (no old lines)
    if (chunk.old_lines.length === 0) {
      const insertionIdx =
        originalLines.length > 0 && originalLines[originalLines.length - 1] === ""
          ? originalLines.length - 1
          : originalLines.length;
      replacements.push([insertionIdx, 0, chunk.new_lines]);
      continue;
    }

    // Try to match old lines in the file
    let pattern = chunk.old_lines;
    let newSlice = chunk.new_lines;
    let found = seekSequence(originalLines, pattern, lineIndex, chunk.is_end_of_file);

    // Retry without trailing empty line if not found
    if (found === -1 && pattern.length > 0 && pattern[pattern.length - 1] === "") {
      pattern = pattern.slice(0, -1);
      if (newSlice.length > 0 && newSlice[newSlice.length - 1] === "") {
        newSlice = newSlice.slice(0, -1);
      }
      found = seekSequence(originalLines, pattern, lineIndex, chunk.is_end_of_file);
    }

    if (found !== -1) {
      replacements.push([found, pattern.length, newSlice]);
      lineIndex = found + pattern.length;
    } else {
      throw new Error(`Failed to find expected lines in ${filePath}:\n${chunk.old_lines.join("\n")}`);
    }
  }

  // Sort replacements by index to apply in order
  replacements.sort((a, b) => a[0] - b[0]);

  return replacements;
}

function applyReplacements(lines: string[], replacements: Array<[number, number, string[]]>): string[] {
  // Apply replacements in reverse order to avoid index shifting
  const result = [...lines];

  for (let i = replacements.length - 1; i >= 0; i--) {
    const [startIdx, oldLen, newSegment] = replacements[i];

    // Remove old lines
    result.splice(startIdx, oldLen);

    // Insert new lines
    for (let j = 0; j < newSegment.length; j++) {
      result.splice(startIdx + j, 0, newSegment[j]);
    }
  }

  return result;
}

function deriveNewContents(filePath: string, chunks: UpdateFileChunk[], originalText: string): string {
  const originalLines = originalText.split("\n");

  // Drop trailing empty element for consistent line counting
  if (originalLines.length > 0 && originalLines[originalLines.length - 1] === "") {
    originalLines.pop();
  }

  const replacements = computeReplacements(originalLines, filePath, chunks);
  let newLines = applyReplacements(originalLines, replacements);

  // Ensure trailing newline
  if (newLines.length === 0 || newLines[newLines.length - 1] !== "") {
    newLines.push("");
  }

  return newLines.join("\n");
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}

function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}

// Wrap inner errors with the opencode-style verification prefix so agents can
// tell the patch is rejected, not a transient IO failure.
function verificationError(reason: string): Error {
  return new Error(`apply_patch verification failed: ${reason}`);
}

interface UpdateBucket {
  sourceBytes: Uint8Array;
  bom: boolean;
  decodedText: string;
  firstPath: string;
  movePath?: string;
}

// Read source once per canonical path and apply each chunk-set on top of the
// previously-computed buffer. First call seeds the bucket; later calls append
// on top of the planned buffer so duplicate update sections don't race
// writeIfUnchanged during apply.
async function ensureUpdateBucket(
  existing: UpdateBucket | undefined,
  canonical: string,
  chunks: UpdateFileChunk[],
  firstPath: string,
  movePath: string | undefined,
): Promise<UpdateBucket> {
  if (!existing) {
    let sourceBytes: Buffer;
    try {
      sourceBytes = await readFile(canonical);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      throw verificationError(`Failed to read file to update: ${canonical} (${message})`);
    }
    let decoded: { text: string; bom: boolean };
    try {
      decoded = decodeUtf8PreserveBom(new Uint8Array(sourceBytes));
    } catch {
      throw verificationError(`File is not valid UTF-8: ${canonical}`);
    }
    let derived: string;
    try {
      derived = deriveNewContents(canonical, chunks, decoded.text);
    } catch (error) {
      throw verificationError(error instanceof Error ? error.message : String(error));
    }
    return {
      sourceBytes: new Uint8Array(sourceBytes),
      bom: decoded.bom,
      decodedText: derived,
      firstPath,
      movePath,
    };
  }
  let derived: string;
  try {
    derived = deriveNewContents(canonical, chunks, existing.decodedText);
  } catch (error) {
    throw verificationError(error instanceof Error ? error.message : String(error));
  }
  existing.decodedText = derived;
  if (movePath !== undefined) existing.movePath = movePath;
  return existing;
}

// planning phase — resolve paths, read source files, compute resulting
// contents. Any error here aborts before any disk mutation. Duplicate update
// sections for the same canonical path are coalesced so subsequent hunks
// derive from the planned buffer, not the on-disk original. Every canonical
// path touched by the patch must appear at most once across add / update /
// move / delete so apply can't race itself.
async function planPatch(hunks: Hunk[]): Promise<PlannedChange[]> {
  const changes: PlannedChange[] = [];
  const updateBuckets = new Map<string, UpdateBucket>();
  const addByPath = new Set<string>();
  const deleteByPath = new Set<string>();
  const updateInPlace = new Set<string>();
  const moveSources = new Set<string>();
  const moveDestinations = new Set<string>();

  const reserveWrite = (canonical: string, conflictLabel: string): void => {
    if (addByPath.has(canonical)) throw verificationError(`${conflictLabel} ${canonical}: already added by patch`);
    if (deleteByPath.has(canonical)) throw verificationError(`${conflictLabel} ${canonical}: already deleted by patch`);
    if (updateInPlace.has(canonical)) throw verificationError(`${conflictLabel} ${canonical}: already updated by patch`);
    if (moveDestinations.has(canonical)) throw verificationError(`${conflictLabel} ${canonical}: already a move destination`);
    if (moveSources.has(canonical)) throw verificationError(`${conflictLabel} ${canonical}: already a move source`);
  };

  for (const hunk of hunks) {
    if (hunk.type === "add") {
      const target = await resolveMutationTarget(hunk.path);
      const canonical = target.canonical;
      reserveWrite(canonical, "Patch adds");
      if (updateBuckets.has(canonical)) {
        throw verificationError(`Patch adds same path as a planned update: ${canonical}`);
      }
      if (target.existed) {
        let info;
        try {
          info = await stat(canonical);
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          throw verificationError(`Failed to read file for add: ${canonical} (${message})`);
        }
        if (info.isDirectory()) {
          throw verificationError(`Cannot write file over directory: ${canonical}`);
        }
      }
      addByPath.add(canonical);
      const content =
        hunk.contents.endsWith("\n") || hunk.contents === "" ? hunk.contents : `${hunk.contents}\n`;
      changes.push({ type: "add", path: hunk.path, target: canonical, content });
      continue;
    }

    if (hunk.type === "delete") {
      const target = await resolveMutationTarget(hunk.path);
      const canonical = target.canonical;
      if (addByPath.has(canonical)) {
        throw verificationError(`Patch adds and deletes same path: ${canonical}`);
      }
      if (deleteByPath.has(canonical)) {
        throw verificationError(`Patch deletes same path more than once: ${canonical}`);
      }
      if (updateBuckets.has(canonical)) {
        throw verificationError(`Patch updates and deletes same path: ${canonical}`);
      }
      if (moveSources.has(canonical)) {
        throw verificationError(`Patch moves and deletes same path: ${canonical}`);
      }
      if (moveDestinations.has(canonical)) {
        throw verificationError(`Patch deletes a path that is also a move destination: ${canonical}`);
      }
      if (!target.existed) {
        throw verificationError(`Failed to read file for deletion: ${canonical}`);
      }
      let info;
      try {
        info = await stat(canonical);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        throw verificationError(`Failed to read file for deletion: ${canonical} (${message})`);
      }
      if (info.isDirectory()) {
        throw verificationError(`Cannot delete directory with Delete File: ${canonical}`);
      }
      deleteByPath.add(canonical);
      changes.push({ type: "delete", path: hunk.path, target: canonical });
      continue;
    }

    // update / move
    const target = await resolveMutationTarget(hunk.path);
    const canonical = target.canonical;
    if (addByPath.has(canonical)) {
      throw verificationError(`Patch adds and updates same path: ${canonical}`);
    }
    if (deleteByPath.has(canonical)) {
      throw verificationError(`Patch updates and deletes same path: ${canonical}`);
    }
    if (moveSources.has(canonical) && !updateBuckets.has(canonical)) {
      throw verificationError(`Patch updates another move's source: ${canonical}`);
    }
    if (hunk.move_path && moveDestinations.has(canonical)) {
      throw verificationError(`Patch moves a path that is also a move destination: ${canonical}`);
    }
    if (!target.existed) {
      throw verificationError(`Failed to read file to update: ${canonical}`);
    }

    if (hunk.move_path) {
      const moveTarget = await resolveMutationTarget(hunk.move_path);
      if (moveTarget.canonical === canonical) {
        throw verificationError(`Move source and destination resolve to same path: ${canonical}`);
      }
      if (moveDestinations.has(moveTarget.canonical)) {
        throw verificationError(`Patch moves multiple sources to same destination: ${moveTarget.canonical}`);
      }
      if (moveSources.has(moveTarget.canonical)) {
        throw verificationError(`Patch move destination is also a move source: ${moveTarget.canonical}`);
      }
      if (addByPath.has(moveTarget.canonical)) {
        throw verificationError(`Patch move destination is also added: ${moveTarget.canonical}`);
      }
      if (updateInPlace.has(moveTarget.canonical)) {
        throw verificationError(`Patch move destination is also updated: ${moveTarget.canonical}`);
      }
      if (deleteByPath.has(moveTarget.canonical)) {
        throw verificationError(`Patch move destination is also deleted: ${moveTarget.canonical}`);
      }
      if (moveTarget.existed) {
        let info;
        try {
          info = await stat(moveTarget.canonical);
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          throw verificationError(`Failed to read move destination: ${moveTarget.canonical} (${message})`);
        }
        if (info.isDirectory()) {
          throw verificationError(`Cannot write file over directory: ${moveTarget.canonical}`);
        }
      }
      const existing = updateBuckets.get(canonical);
      if (existing && existing.movePath && existing.movePath !== hunk.move_path) {
        throw verificationError(`Patch moves same source to different destinations: ${canonical}`);
      }
      moveSources.add(canonical);
      moveDestinations.add(moveTarget.canonical);
      const bucket = await ensureUpdateBucket(
        existing,
        canonical,
        hunk.chunks,
        hunk.path,
        hunk.move_path,
      );
      updateBuckets.set(canonical, bucket);
      continue;
    }

    // in-place update: must not collide with move destinations.
    if (moveDestinations.has(canonical)) {
      throw verificationError(`Patch updates a path that is also a move destination: ${canonical}`);
    }
    updateInPlace.add(canonical);
    const bucket = await ensureUpdateBucket(
      updateBuckets.get(canonical),
      canonical,
      hunk.chunks,
      hunk.path,
      undefined,
    );
    updateBuckets.set(canonical, bucket);
  }

  // Materialize one planned update per canonical source path.
  for (const [canonical, bucket] of updateBuckets) {
    if (bucket.movePath) {
      const moveTarget = await resolveMutationTarget(bucket.movePath);
      if (moveTarget.canonical === canonical) {
        throw verificationError(`Move source and destination resolve to same path: ${canonical}`);
      }
      changes.push({
        type: "move",
        path: bucket.firstPath,
        target: canonical,
        movePath: bucket.movePath,
        moveTarget: moveTarget.canonical,
        sourceBytes: bucket.sourceBytes,
        content: bucket.decodedText,
        bom: bucket.bom,
      });
    } else {
      changes.push({
        type: "update",
        path: bucket.firstPath,
        target: canonical,
        sourceBytes: bucket.sourceBytes,
        content: bucket.decodedText,
        bom: bucket.bom,
      });
    }
  }

  return changes;
}

// apply phase — only invoked after planPatch succeeds. Write-time failures may
// leave partial state; caller surfaces the error. File writes go through
// per-file locks so concurrent write/edit calls cannot interleave.
async function applyPlannedChanges(changes: PlannedChange[]): Promise<void> {
  for (const change of changes) {
    if (change.type === "add") {
      await lockedWrite(change.target, change.content);
      invalidateFileCache(change.target);
      invalidateFileCache(change.path);
      recordEdit(change.target);
      continue;
    }

    if (change.type === "delete") {
      await rm(change.target);
      invalidateFileCache(change.target);
      invalidateFileCache(change.path);
      continue;
    }

    // update / move share BOM-preserving write semantics.
    const payload = joinBom(change.content, change.bom);

    if (change.type === "update") {
      await writeIfUnchanged(change.target, change.sourceBytes, payload);
      invalidateFileCache(change.target);
      invalidateFileCache(change.path);
      recordEdit(change.target);
      continue;
    }

    // move: re-check source unchanged before writing destination + removing
    // source. If the source moved/changed under us, abort to avoid clobbering
    // an unrelated file. Destination write goes through lockedWrite so
    // concurrent edit/write calls cannot interleave on the dest path.
    const current = await readFile(change.target);
    if (!sameBytes(new Uint8Array(current), change.sourceBytes)) {
      throw new Error(`File changed after it was read: ${change.target}`);
    }
    await lockedWrite(change.moveTarget, payload);
    await rm(change.target);
    invalidateFileCache(change.target);
    invalidateFileCache(change.path);
    invalidateFileCache(change.moveTarget);
    invalidateFileCache(change.movePath);
    recordEdit(change.moveTarget);
  }
}

function sameBytes(left: Uint8Array, right: Uint8Array): boolean {
  return left.length === right.length && left.every((byte, index) => byte === right[index]);
}

function relativize(absolute: string): string {
  const rel = path.relative(process.cwd(), absolute);
  return rel && !rel.startsWith("..") ? toPosixPath(rel) : toPosixPath(absolute);
}

export const applyPatchTool: Tool & { handler: (args: Record<string, unknown>) => Promise<ToolResult> } = {
  name: "cave__apply_patch",
  description:
    "Apply one patch containing add, update, delete, and move file operations. Every patch operation is verified before any disk write; if a write-time error occurs after verification, partial state may remain on disk.",
  inputSchema: {
    type: "object",
    properties: {
      patchText: {
        type: "string",
        description: "The full patch text describing add, update, delete, and move operations",
      },
    },
    required: ["patchText"],
  },
  handler: async (args) => {
    const patchText = String(args.patchText || "");
    if (!patchText.trim()) return err("patchText is required");

    let hunks: Hunk[];
    try {
      hunks = parsePatch(patchText).hunks;
    } catch (error) {
      return err(`apply_patch verification failed: ${error instanceof Error ? error.message : String(error)}`);
    }
    if (hunks.length === 0) {
      const normalized = patchText.replace(/\r\n/g, "\n").trim();
      if (normalized === "*** Begin Patch\n*** End Patch") return err("patch rejected: empty patch");
      return err("apply_patch verification failed: no hunks found");
    }

    let changes: PlannedChange[];
    try {
      changes = await planPatch(hunks);
    } catch (error) {
      return err(error instanceof Error ? error.message : String(error));
    }

    try {
      await applyPlannedChanges(changes);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return err(`apply_patch apply failed: ${message}`);
    }

    const summary = changes.map((change) => {
      if (change.type === "add") return `A ${relativize(change.target)}`;
      if (change.type === "delete") return `D ${relativize(change.target)}`;
      if (change.type === "move") return `M ${relativize(change.moveTarget)}`;
      return `M ${relativize(change.target)}`;
    });

    return ok(`Success. Updated the following files:\n${summary.join("\n")}`);
  },
};
