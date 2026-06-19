import { readFile, writeFile } from "fs/promises";
import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";

function countOccurrences(content: string, needle: string, max = 2): number {
  let count = 0, pos = 0;
  while (count <= max) {
    const idx = content.indexOf(needle, pos);
    if (idx === -1) break;
    count++;
    pos = idx + needle.length;
  }
  return count;
}

function findOriginalSpan(content: string, normalizedNeedle: string): string | null {
  const needleLines = normalizedNeedle.split("\n");
  const contentLines = content.split("\n");
  const sep = content.includes("\r\n") ? "\r\n" : "\n";

  outer: for (let start = 0; start < contentLines.length; start++) {
    if (start + needleLines.length > contentLines.length) break;
    for (let i = 0; i < needleLines.length; i++) {
      if (contentLines[start + i].trimEnd() !== needleLines[i]) continue outer;
    }
    return contentLines.slice(start, start + needleLines.length).join(sep);
  }
  return null;
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
    const keywords = firstLine.split(/[^a-zA-Z0-9_]/).filter(w => w.length >= 4);
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
  const preview = bestLine.text.trim().length > 100
    ? bestLine.text.trim().slice(0, 97) + "..."
    : bestLine.text.trim();
  return `\nClosest match at line ${bestLine.num}: \`${preview}\`\nHint: check indentation/whitespace differences.`;
}

interface SingleEdit {
  old_string: string;
  new_string: string;
  replace_all?: boolean;
}

interface MatchedEdit {
  matchIndex: number;
  matchLength: number;
  new_string: string;
}

export const editTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__edit",
  description:
    "Exact string replacement in a file (like the built-in Edit). Replaces old_string with new_string; fails if old_string is missing or non-unique (unless replace_all). Supports batch edits via `edits[]` array for multiple replacements in one call. Includes CRLF/LF normalization and trailing whitespace tolerance fallbacks. Invalidates the read dedup cache automatically.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to modify",
      },
      old_string: {
        type: "string",
        description: "Text to replace (must match exactly, including indentation)",
      },
      new_string: {
        type: "string",
        description: "Replacement text (must differ from old_string)",
      },
      replace_all: {
        type: "boolean",
        description: "Replace every occurrence instead of requiring uniqueness (default false)",
      },
      edits: {
        type: "array",
        items: {
          type: "object",
          properties: {
            old_string: { type: "string" },
            new_string: { type: "string" },
            replace_all: { type: "boolean" },
          },
          required: ["old_string", "new_string"],
        },
        description: "Multiple replacements in one call. Applied in reverse positional order. Alternative to single old_string/new_string.",
      },
    },
    required: ["file_path"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path);

    let content: string;
    try {
      content = await readFile(filePath, "utf-8");
    } catch {
      return err(`Cannot read file: ${filePath}`);
    }

    const usesCRLF = content.includes("\r\n");

    if (Array.isArray(args.edits)) {
      return handleBatchEdits(filePath, content, args.edits as SingleEdit[], usesCRLF);
    }

    const oldString = String(args.old_string ?? "");
    const newString = String(args.new_string ?? "");
    const replaceAll = args.replace_all === true;

    if (oldString === newString) {
      return err("old_string and new_string are identical");
    }
    if (oldString.length === 0) {
      return err("old_string cannot be empty");
    }

    const result = tryReplace(content, oldString, newString, replaceAll, usesCRLF);
    if (result.error) {
      return err(result.error);
    }

    try {
      await writeFile(filePath, result.updated!, "utf-8");
    } catch {
      return err(`Cannot write file: ${filePath}`);
    }
    invalidateFileCache(filePath);
    recordEdit(filePath);

    return ok(`Edited ${filePath} (${result.count} replacement${result.count === 1 ? "" : "s"})`);
  },
};

function tryReplace(
  content: string,
  oldString: string,
  newString: string,
  replaceAll: boolean,
  usesCRLF: boolean,
): { updated?: string; count?: number; error?: string } {
  let occurrences = countOccurrences(content, oldString);

  if (occurrences > 0) {
    if (occurrences > 1 && !replaceAll) {
      return { error: `old_string is not unique (${occurrences}+ matches). Add more context or set replace_all.` };
    }
    const updated = replaceAll
      ? content.split(oldString).join(newString)
      : content.replace(oldString, newString);
    const count = replaceAll ? content.split(oldString).length - 1 : 1;
    return { updated, count };
  }

  if (usesCRLF && !oldString.includes("\r")) {
    const oldCRLF = oldString.replace(/\n/g, "\r\n");
    const occ = countOccurrences(content, oldCRLF);
    if (occ > 0) {
      if (occ > 1 && !replaceAll) {
        return { error: `old_string is not unique (${occ}+ matches with CRLF). Add more context or set replace_all.` };
      }
      const newCRLF = newString.replace(/\n/g, "\r\n");
      const updated = replaceAll
        ? content.split(oldCRLF).join(newCRLF)
        : content.replace(oldCRLF, newCRLF);
      const count = replaceAll ? content.split(oldCRLF).length - 1 : 1;
      return { updated, count };
    }
  } else if (!usesCRLF && oldString.includes("\r\n")) {
    const oldLF = oldString.replace(/\r\n/g, "\n");
    const occ = countOccurrences(content, oldLF);
    if (occ > 0) {
      if (occ > 1 && !replaceAll) {
        return { error: `old_string is not unique (${occ}+ matches with LF). Add more context or set replace_all.` };
      }
      const newLF = newString.replace(/\r\n/g, "\n");
      const updated = replaceAll
        ? content.split(oldLF).join(newLF)
        : content.replace(oldLF, newLF);
      const count = replaceAll ? content.split(oldLF).length - 1 : 1;
      return { updated, count };
    }
  }

  const normalizedContent = content.split("\n").map(l => l.trimEnd()).join("\n");
  const normalizedOld = oldString.split("\n").map(l => l.trimEnd()).join("\n");
  if (normalizedOld.length > 0 && normalizedContent.includes(normalizedOld)) {
    const originalSpan = findOriginalSpan(content, normalizedOld);
    if (originalSpan) {
      const occ = countOccurrences(content, originalSpan);
      if (occ > 1 && !replaceAll) {
        return { error: `old_string is not unique (${occ}+ matches with whitespace tolerance). Add more context or set replace_all.` };
      }
      const updated = replaceAll
        ? content.split(originalSpan).join(newString)
        : content.replace(originalSpan, newString);
      const count = replaceAll ? content.split(originalSpan).length - 1 : 1;
      return { updated, count };
    }
  }

  if (content.includes(newString)) {
    return { error: `old_string not found, but new_string already exists in the file. The edit was likely already applied.` };
  }

  const hint = findClosestLineHint(content, oldString);
  return { error: `old_string not found in file.${hint}` };
}

async function handleBatchEdits(
  filePath: string,
  content: string,
  edits: SingleEdit[],
  usesCRLF: boolean,
): Promise<ToolResult> {
  if (edits.length === 0) {
    return err("edits array is empty");
  }

  const matched: MatchedEdit[] = [];

  for (let i = 0; i < edits.length; i++) {
    const edit = edits[i];
    if (edit.old_string.length === 0) {
      return err(`edits[${i}].old_string cannot be empty`);
    }
    if (edit.old_string === edit.new_string) {
      return err(`edits[${i}]: old_string and new_string are identical`);
    }

    const result = findMatch(content, edit.old_string, edit.replace_all ?? false, usesCRLF);
    if (result.error) {
      return err(`edits[${i}]: ${result.error}`);
    }

    matched.push({
      matchIndex: result.matchIndex!,
      matchLength: result.matchLength!,
      new_string: edit.new_string,
    });
  }

  matched.sort((a, b) => a.matchIndex - b.matchIndex);
  for (let i = 1; i < matched.length; i++) {
    const prev = matched[i - 1];
    const curr = matched[i];
    if (prev.matchIndex + prev.matchLength > curr.matchIndex) {
      return err(`edits overlap. Merge them into one edit or target disjoint regions.`);
    }
  }

  let updated = content;
  for (let i = matched.length - 1; i >= 0; i--) {
    const m = matched[i];
    updated = updated.substring(0, m.matchIndex) + m.new_string + updated.substring(m.matchIndex + m.matchLength);
  }

  try {
    await writeFile(filePath, updated, "utf-8");
  } catch {
    return err(`Cannot write file: ${filePath}`);
  }
  invalidateFileCache(filePath);
  recordEdit(filePath);

  return ok(`Edited ${filePath} (${matched.length} replacement${matched.length === 1 ? "" : "s"})`);
}

function findMatch(
  content: string,
  oldString: string,
  replaceAll: boolean,
  usesCRLF: boolean,
): { matchIndex?: number; matchLength?: number; error?: string } {
  let idx = content.indexOf(oldString);
  if (idx !== -1) {
    if (!replaceAll) {
      const secondIdx = content.indexOf(oldString, idx + oldString.length);
      if (secondIdx !== -1) {
        return { error: `old_string is not unique (2+ matches). Add more context or set replace_all.` };
      }
    }
    return { matchIndex: idx, matchLength: oldString.length };
  }

  if (usesCRLF && !oldString.includes("\r")) {
    const oldCRLF = oldString.replace(/\n/g, "\r\n");
    idx = content.indexOf(oldCRLF);
    if (idx !== -1) {
      if (!replaceAll) {
        const secondIdx = content.indexOf(oldCRLF, idx + oldCRLF.length);
        if (secondIdx !== -1) {
          return { error: `old_string is not unique (2+ matches with CRLF). Add more context or set replace_all.` };
        }
      }
      return { matchIndex: idx, matchLength: oldCRLF.length };
    }
  } else if (!usesCRLF && oldString.includes("\r\n")) {
    const oldLF = oldString.replace(/\r\n/g, "\n");
    idx = content.indexOf(oldLF);
    if (idx !== -1) {
      if (!replaceAll) {
        const secondIdx = content.indexOf(oldLF, idx + oldLF.length);
        if (secondIdx !== -1) {
          return { error: `old_string is not unique (2+ matches with LF). Add more context or set replace_all.` };
        }
      }
      return { matchIndex: idx, matchLength: oldLF.length };
    }
  }

  const normalizedContent = content.split("\n").map(l => l.trimEnd()).join("\n");
  const normalizedOld = oldString.split("\n").map(l => l.trimEnd()).join("\n");
  if (normalizedOld.length > 0) {
    idx = normalizedContent.indexOf(normalizedOld);
    if (idx !== -1) {
      const originalSpan = findOriginalSpan(content, normalizedOld);
      if (originalSpan) {
        const origIdx = content.indexOf(originalSpan);
        if (origIdx !== -1) {
          if (!replaceAll) {
            const secondIdx = content.indexOf(originalSpan, origIdx + originalSpan.length);
            if (secondIdx !== -1) {
              return { error: `old_string is not unique (2+ matches with whitespace tolerance). Add more context or set replace_all.` };
            }
          }
          return { matchIndex: origIdx, matchLength: originalSpan.length };
        }
      }
    }
  }

  if (content.includes(oldString.replace(/\s+$/gm, ""))) {
    return { error: `old_string not found (trailing whitespace mismatch?).` };
  }

  return { error: `old_string not found in file.` };
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
