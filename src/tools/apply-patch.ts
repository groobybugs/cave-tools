import { readFile, rm, writeFile } from "fs/promises";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import type { ToolResult } from "../types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";
import { ensureParentDirectory, resolveMutationTarget } from "../runtime/path.js";
import { writeIfUnchanged } from "../runtime/file-mutation.js";

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

// Normalize Unicode punctuation to ASCII equivalents (like Rust's normalize_unicode)
function normalizeUnicode(str: string): string {
  return str
    .replace(/[‘’‚‛]/g, "'")
    .replace(/[“”„‟]/g, '"')
    .replace(/[‐‑‒–—―]/g, "-")
    .replace(/…/g, "...")
    .replace(/ /g, " ");
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

  // Backward-compatible whole-file blob: no @@ context headers.
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

function parsePatch(patchText: string): { hunks: Hunk[] } {
  const cleaned = patchText.replace(/\r\n/g, "\n").trim();
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

export const applyPatchTool: Tool & { handler: (args: Record<string, unknown>) => Promise<ToolResult> } = {
  name: "cave__apply_patch",
  description:
    "Apply one patch containing add, update, delete, and move file operations. Operations apply sequentially; if a later operation fails, earlier operations remain applied and are reported.",
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
    if (hunks.length === 0) return err("patch rejected: empty patch");

    const applied: string[] = [];
    const fail = (path: string) =>
      err(
        applied.length === 0
          ? `Unable to apply patch at ${path}`
          : `Patch partially applied before failing at ${path}. Applied: ${applied.join(", ")}`,
      );

    for (const hunk of hunks) {
      try {
        const target = await resolveMutationTarget(hunk.path);

        if (hunk.type === "add") {
          await ensureParentDirectory(target.canonical);
          const contents =
            hunk.contents.endsWith("\n") || hunk.contents === "" ? hunk.contents : `${hunk.contents}\n`;
          await writeFile(target.canonical, contents, { encoding: "utf-8", flag: "wx" });
          invalidateFileCache(target.canonical);
          invalidateFileCache(hunk.path);
          recordEdit(target.canonical);
          applied.push(`A ${hunk.path}`);
        } else if (hunk.type === "delete") {
          await rm(target.canonical);
          invalidateFileCache(target.canonical);
          invalidateFileCache(hunk.path);
          applied.push(`D ${hunk.path}`);
        } else {
          const source = await readFile(target.canonical);
          const content = new TextDecoder("utf-8", { fatal: true }).decode(source);
          const next = deriveNewContents(hunk.path, hunk.chunks, content);

          if (hunk.move_path) {
            const moveTarget = await resolveMutationTarget(hunk.move_path);
            await ensureParentDirectory(moveTarget.canonical);
            await writeFile(moveTarget.canonical, next, { encoding: "utf-8" });
            await rm(target.canonical);
            invalidateFileCache(target.canonical);
            invalidateFileCache(hunk.path);
            invalidateFileCache(moveTarget.canonical);
            invalidateFileCache(hunk.move_path);
            recordEdit(moveTarget.canonical);
            applied.push(`R ${hunk.path} -> ${hunk.move_path}`);
          } else {
            await writeIfUnchanged(target.canonical, source, next);
            invalidateFileCache(target.canonical);
            invalidateFileCache(hunk.path);
            recordEdit(target.canonical);
            applied.push(`M ${hunk.path}`);
          }
        }
      } catch {
        return fail(hunk.path);
      }
    }

    return ok(["Applied patch sequentially:", ...applied].join("\n"));
  },
};
