import { readFile } from "fs/promises";
import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { invalidateFileCache, recordEdit } from "../compression/utils.js";
import { findReplacement } from "./replacers.js";
import { writeIfUnchanged } from "../runtime/file-mutation.js";

// Adapt new_string line endings to match the matched span's style so an edit
// against a CRLF file doesn't inject lone LFs (and vice-versa).
function adaptLineEndings(search: string, newString: string): string {
  const searchCRLF = search.includes("\r\n");
  const newCRLF = newString.includes("\r\n");
  if (searchCRLF && !newCRLF) return newString.replace(/\n/g, "\r\n");
  if (!searchCRLF && newCRLF) return newString.replace(/\r\n/g, "\n");
  return newString;
}

function decodeUtf8PreserveBom(content: Uint8Array): { text: string; bom: boolean } {
  const bom = content[0] === 0xef && content[1] === 0xbb && content[2] === 0xbf;
  const text = new TextDecoder("utf-8", { fatal: true }).decode(bom ? content.slice(3) : content);
  return { text, bom };
}

function joinBom(text: string, bom: boolean): string {
  const stripped = text.replace(/^\uFEFF+/, "");
  return bom ? `\uFEFF${stripped}` : stripped;
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
    "Optimized drop-in replacement for the built-in Edit. Replaces old_string with new_string; fails if old_string is missing or non-unique (unless replace_all). Uses a fuzzy replacer chain (exact → line-trimmed → block-anchor → whitespace-normalized → indentation-flexible → escape-normalized → trimmed-boundary → context-aware → multi-occurrence), so small indentation/whitespace/escape drift in old_string still matches. Supports batch edits via `edits[]`. Adapts CRLF/LF automatically and invalidates the read dedup cache.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to modify",
      },
      old_string: {
        type: "string",
        description: "Text to replace (matched with whitespace/indentation tolerance)",
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
        description:
          "Multiple replacements in one call. Applied in reverse positional order. Alternative to single old_string/new_string.",
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
      return err(error instanceof Error && error.message.includes("encoded data") ? `File is not valid UTF-8: ${filePath}` : `Cannot read file: ${filePath}`);
    }

    if (Array.isArray(args.edits)) {
      return handleBatchEdits(filePath, content, sourceBytes, bom, args.edits as SingleEdit[]);
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

    const match = findReplacement(content, oldString, replaceAll);
    if (match.error || match.search === undefined) {
      if (content.includes(newString)) {
        return err("old_string not found — new_string already present, edit already applied?");
      }
      if (match.nonUnique) {
        return err("old_string is not unique (2+ matches). Add more context or set replace_all.");
      }
      return err(`${match.error ?? "old_string not found in file"}.${findClosestLineHint(content, oldString)}`);
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

    try {
      const result = await writeIfUnchanged(filePath, sourceBytes, joinBom(updated, bom));
      invalidateFileCache(result.canonical);
      recordEdit(result.canonical);
    } catch (error) {
      return err(error instanceof Error ? error.message : `Cannot write file: ${filePath}`);
    }
    invalidateFileCache(filePath);

    return ok(`Edited ${filePath} (${count} replacement${count === 1 ? "" : "s"})`);
  },
};

async function handleBatchEdits(
  filePath: string,
  content: string,
  sourceBytes: Uint8Array,
  bom: boolean,
  edits: SingleEdit[],
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

    const match = findReplacement(content, edit.old_string, edit.replace_all ?? false);
    if (match.error || match.search === undefined) {
      if (match.nonUnique) {
        return err(`edits[${i}]: old_string is not unique (2+ matches). Add more context or set replace_all.`);
      }
      return err(`edits[${i}]: ${match.error ?? "old_string not found in file."}`);
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

  let updated = content;
  for (let i = matched.length - 1; i >= 0; i--) {
    const m = matched[i];
    updated =
      updated.substring(0, m.matchIndex) +
      m.new_string +
      updated.substring(m.matchIndex + m.matchLength);
  }

  try {
    const result = await writeIfUnchanged(filePath, sourceBytes, joinBom(updated, bom));
    invalidateFileCache(result.canonical);
    recordEdit(result.canonical);
  } catch (error) {
    return err(error instanceof Error ? error.message : `Cannot write file: ${filePath}`);
  }
  invalidateFileCache(filePath);

  return ok(`Edited ${filePath} (${matched.length} replacement${matched.length === 1 ? "" : "s"})`);
}

function ok(text: string): ToolResult {
  return { content: [{ type: "text", text }] };
}
function err(text: string): ToolResult {
  return { content: [{ type: "text", text: `Error: ${text}` }], isError: true };
}
