import type { ToolResult } from "../types.js";
import { readFile, stat, readdir, open } from "fs/promises";
import { extname, dirname, basename, join } from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  isFileUnchanged,
  updateFileCache,
  applyBudget,
  recordRead,
  shouldForceFull,
  READ_STUB,
} from "../compression/utils.js";

import { formatSignatures } from "../compression/signatures.js";
import { aggressiveCompress } from "../compression/aggressive.js";
import { addCodebookFile, compressWithCodebook } from "../compression/codebook.js";

const IMAGE_MIME: Record<string, string> = {
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".bmp": "image/bmp",
  ".svg": "image/svg+xml",
};

// Hard cap to avoid blowing up the MCP transport with multi-MB base64 payloads.
// Anthropic image input cap is 5 MB; we mirror that.
const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
// Same cap reused for PDFs returned as embedded resources.
const MAX_PDF_BYTES = 5 * 1024 * 1024;

// Per-line and per-read byte caps, ported from opencode. These defend the
// budget/codebook compression against pathological inputs (minified lines,
// generated blobs) so a single 2 MB line can't blow the token budget.
const MAX_LINE_LENGTH = 2000;
const MAX_BYTES = 50 * 1024;

// Extensions that are always binary; reject before attempting a utf-8 decode.
const BINARY_EXTENSIONS = new Set([
  ".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar",
  ".exe", ".dll", ".so", ".dylib", ".bin", ".dat", ".obj", ".o", ".a", ".lib",
  ".class", ".jar", ".war", ".wasm", ".pyc", ".pyo",
  ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp",
  ".woff", ".woff2", ".ttf", ".otf", ".eot",
  ".mp3", ".mp4", ".avi", ".mov", ".mkv", ".wav", ".flac", ".ogg",
  ".ico", ".tiff", ".tif", ".heic", ".psd",
]);

// Detect binary content. Known binary extensions short-circuit; otherwise sniff
// the first 4 KB for a NUL byte or a high ratio of non-printable bytes.
async function isBinaryFile(filePath: string, ext: string): Promise<boolean> {
  if (BINARY_EXTENSIONS.has(ext)) return true;

  let handle;
  try {
    handle = await open(filePath, "r");
    const buffer = Buffer.alloc(4096);
    const { bytesRead } = await handle.read(buffer, 0, 4096, 0);
    if (bytesRead === 0) return false;

    let nonPrintable = 0;
    for (let i = 0; i < bytesRead; i++) {
      const byte = buffer[i];
      if (byte === 0) return true; // NUL byte => binary
      // Printable: tab(9) newline(10) cr(13) and >= 32. Count the rest.
      if (byte < 9 || (byte > 13 && byte < 32)) nonPrintable++;
    }
    return nonPrintable / bytesRead > 0.3;
  } catch {
    return false; // let the normal read path surface the real error
  } finally {
    await handle?.close();
  }
}

// Build a "did you mean" hint for a missing path, mirroring opencode.
async function suggestPaths(filePath: string): Promise<string> {
  try {
    const dir = dirname(filePath);
    const base = basename(filePath).toLowerCase();
    const entries = await readdir(dir);
    const matches = entries
      .filter(
        (e) =>
          e.toLowerCase().includes(base) || base.includes(e.toLowerCase()),
      )
      .slice(0, 3)
      .map((e) => join(dir, e));
    return matches.length > 0
      ? `\n\nDid you mean one of these?\n${matches.join("\n")}`
      : "";
  } catch {
    return "";
  }
}

async function formatReadError(error: unknown, filePath: string): Promise<string> {
  const code = (error as NodeJS.ErrnoException)?.code;
  const message = error instanceof Error ? error.message : String(error);
  if (code === "ENOENT") {
    return `File not found: ${filePath}${await suggestPaths(filePath)}`;
  }
  return `Error reading file: ${message}`;
}

interface SelectResult {
  body: string;
  lastReadLine: number;
  truncated: boolean;
  truncatedByBytes: boolean;
}

// Slice lines with offset/limit, capping long lines and total bytes. Optionally
// prefixes opencode-style line numbers (`00001| ...`).
function selectLines(
  lines: string[],
  offset: number,
  limit: number,
  lineNumbers: boolean,
): SelectResult {
  const start = Math.max(0, offset - 1);
  const end = Math.min(lines.length, start + limit);
  const raw: string[] = [];
  let bytes = 0;
  let truncatedByBytes = false;

  for (let i = start; i < end; i++) {
    let line = lines[i];
    if (line.length > MAX_LINE_LENGTH) {
      line = line.slice(0, MAX_LINE_LENGTH) + "...";
    }
    const size = Buffer.byteLength(line, "utf-8") + (raw.length > 0 ? 1 : 0);
    if (bytes + size > MAX_BYTES) {
      truncatedByBytes = true;
      break;
    }
    bytes += size;
    raw.push(line);
  }

  const lastReadLine = start + raw.length;
  const body = lineNumbers
    ? raw
        .map(
          (l, idx) =>
            `${(start + idx + 1).toString().padStart(5, "0")}| ${l}`,
        )
        .join("\n")
    : raw.join("\n");

  return {
    body,
    lastReadLine,
    truncated: lastReadLine < lines.length || truncatedByBytes,
    truncatedByBytes,
  };
}

function truncationHint(sel: SelectResult): string {
  if (!sel.truncated) return "";
  return sel.truncatedByBytes
    ? `\n\n(Output truncated at ${MAX_BYTES} bytes. Use 'offset' to read beyond line ${sel.lastReadLine})`
    : `\n\n(File has more lines. Use 'offset' to read beyond line ${sel.lastReadLine})`;
}

export const readTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__read",
  description:
    "Optimized drop-in replacement for the built-in Read. Returns the same file content, but sends a stub when the file is unchanged since the last read in this session (skips re-sending identical content). Image files (.png, .jpg, .jpeg, .gif, .webp, .bmp, .svg) are returned as image content blocks (base64); PDFs are returned as embedded resources; offset/limit are ignored for images, signatures, and aggressive modes. Binary files are rejected. Use mode='signatures' to return only function/class/type signatures (TS/JS/Rust). Use mode='aggressive' to drop comments and blank lines. Set line_numbers=true to prefix output with line numbers for precise edits (disables codebook compression).",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path to the file to read",
      },
      offset: {
        type: "number",
        description: "Line number to start reading from (1-indexed). Ignored for images.",
        default: 1,
      },
      limit: {
        type: "number",
        description: "Maximum number of lines to read. Ignored for images and signatures mode.",
        default: 200,
      },
      mode: {
        type: "string",
        enum: ["full", "signatures", "aggressive"],
        description: "Read mode: full text (default), signatures only, or aggressive comment stripping.",
        default: "full",
      },
      line_numbers: {
        type: "boolean",
        description: "Prefix each line with a zero-padded line number for precise edits. Disables codebook compression. Default false.",
        default: false,
      },
      force: {
        type: "boolean",
        description: "Bypass dedup cache and always return full content",
        default: false,
      },
    },
    required: ["file_path"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path);
    const offset = Number(args.offset) || 1;
    const limit = Number(args.limit) || 200;
    const mode = String(args.mode || "full");
    const lineNumbers = args.line_numbers === true;
    const force = args.force === true;

    const ext = extname(filePath).toLowerCase();
    const imageMime = IMAGE_MIME[ext];

    if (imageMime) {
      try {
        if (!force && (await isFileUnchanged(filePath))) {
          return {
            content: [
              { type: "text", text: "<image unchanged since last read>" },
            ],
          };
        }

        const fileStat = await stat(filePath);
        if (fileStat.size > MAX_IMAGE_BYTES) {
          return {
            content: [
              {
                type: "text",
                text: `Image too large (${fileStat.size} bytes, max ${MAX_IMAGE_BYTES}). Resize or crop before reading.`,
              },
            ],
            isError: true,
          };
        }

        // SVG: send as text (it's just XML), keeps the image route for raster only.
        if (ext === ".svg") {
          const svg = await readFile(filePath, "utf-8");
          await updateFileCache(filePath);
          return {
            content: [{ type: "text", text: svg }],
          };
        }

        const buf = await readFile(filePath);
        await updateFileCache(filePath);

        return {
          content: [
            {
              type: "image",
              data: buf.toString("base64"),
              mimeType: imageMime,
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error reading image: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }

    if (ext === ".pdf") {
      try {
        if (!force && (await isFileUnchanged(filePath))) {
          return {
            content: [
              { type: "text", text: "<pdf unchanged since last read>" },
            ],
          };
        }

        const fileStat = await stat(filePath);
        if (fileStat.size > MAX_PDF_BYTES) {
          return {
            content: [
              {
                type: "text",
                text: `PDF too large (${fileStat.size} bytes, max ${MAX_PDF_BYTES}).`,
              },
            ],
            isError: true,
          };
        }

        const buf = await readFile(filePath);
        await updateFileCache(filePath);
        return {
          content: [
            {
              type: "resource",
              resource: {
                uri: `file://${filePath}`,
                mimeType: "application/pdf",
                blob: buf.toString("base64"),
              },
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error reading PDF: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }

    // Guard every text path against binary content.
    if (await isBinaryFile(filePath, ext)) {
      return {
        content: [
          { type: "text", text: `Cannot read binary file: ${filePath}` },
        ],
        isError: true,
      };
    }

    if (shouldForceFull(filePath)) {
      try {
        const content = await readFile(filePath, "utf-8");
        const lines = content.split("\n");
        const sel = selectLines(lines, offset, limit, lineNumbers);
        const text = sel.body + truncationHint(sel);
        await updateFileCache(filePath);
        recordRead(filePath, false, text.length);
        return {
          content: [{ type: "text", text }],
        };
      } catch (error) {
        return {
          content: [
            { type: "text", text: await formatReadError(error, filePath) },
          ],
          isError: true,
        };
      }
    }

    if (!force && (await isFileUnchanged(filePath))) {
      recordRead(filePath, false, READ_STUB.length);
      return {
        content: [
          {
            type: "text",
            text: "<file unchanged since last read>",
          },
        ],
      };
    }

    try {
      const content = await readFile(filePath, "utf-8");

      if (mode === "aggressive") {
        await updateFileCache(filePath);
        const compressedContent = aggressiveCompress(content, ext);
        const output =
          compressedContent.length < content.length ? compressedContent : content;
        const compressed = applyBudget(output, "read");
        const wasCompressed = compressed.length < output.length;
        recordRead(filePath, wasCompressed, compressed.length);
        return {
          content: [{ type: "text", text: compressed }],
        };
      }

      if (mode === "signatures") {
        await updateFileCache(filePath);
        const sigs = formatSignatures(content, ext);
        const output = sigs || "(no signatures extracted for this file type)";
        const compressed = applyBudget(output, "read");
        const wasCompressed = compressed.length < output.length;
        recordRead(filePath, wasCompressed, compressed.length);
        return {
          content: [{ type: "text", text: compressed }],
        };
      }

      const lines = content.split("\n");
      const sel = selectLines(lines, offset, limit, lineNumbers);
      await updateFileCache(filePath);

      let outText: string;
      if (lineNumbers) {
        // Numbered lines defeat cross-file codebook matching; skip it.
        outText = sel.body;
      } else {
        addCodebookFile(filePath, content);
        const { text: codebookText, legend } = compressWithCodebook(sel.body);
        outText = legend ? `${codebookText}\n\n${legend}` : codebookText;
      }
      outText += truncationHint(sel);

      const compressed = applyBudget(outText, "read");
      const wasCompressed = compressed.length < sel.body.length;
      recordRead(filePath, wasCompressed, compressed.length);

      return {
        content: [
          {
            type: "text",
            text: compressed,
          },
        ],
      };
    } catch (error) {
      return {
        content: [
          { type: "text", text: await formatReadError(error, filePath) },
        ],
        isError: true,
      };
    }
  },
};
