import type { ToolResult } from "../types.js";
import { readFile, stat, readdir, open, realpath } from "fs/promises";
import { extname, dirname, basename, join } from "path";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  isFileUnchanged,
  updateFileCache,
  applyBudget,
  recordRead,
  shouldForceFull,
  readStubFor,
  hashContent,
  fitsBudget,
  isSubagentContext,
} from "../compression/utils.js";
import { getSnapshot, rememberSnapshot } from "../compression/snapshots.js";
import { unifiedDiff } from "../compression/diff.js";
import { formatNumberedLine, rangeChecksum } from "./line-range.js";

import { formatSignatures } from "../compression/signatures.js";
import { aggressiveCompress } from "../compression/aggressive.js";
import { addCodebookFile, compressWithCodebook } from "../compression/codebook.js";
import { containsPath } from "../runtime/path.js";

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
// Matches opencode 2.0 MAX_MEDIA_INGEST_BYTES (20 MB).
const MAX_IMAGE_BYTES = 20 * 1024 * 1024;
// Same cap reused for PDFs returned as embedded resources.
const MAX_PDF_BYTES = 20 * 1024 * 1024;

// Per-line and per-read byte caps, ported from opencode. These defend the
// budget/codebook compression against pathological inputs (minified lines,
// generated blobs) so a single 2 MB line can't blow the token budget.
const MAX_LINE_LENGTH = 2000;
const MAX_BYTES = 50 * 1024;
// Re-read of a changed file returns a unified diff only when it is smaller
// than this fraction of the full content.
const DIFF_MAX_RATIO = 0.4;
const DIFF_HEADER = "[cave: changed since last read in this session — unified diff; no prior copy? re-read with force=true]";

function startsWith(bytes: Uint8Array, prefix: number[]): boolean {
  return prefix.every((value, index) => bytes[index] === value);
}

async function sniffImageMime(filePath: string, ext: string): Promise<string | undefined> {
  if (ext === ".svg") return IMAGE_MIME[ext];
  const extensionMime = IMAGE_MIME[ext];
  let handle;
  try {
    handle = await open(filePath, "r");
    const buffer = Buffer.alloc(16);
    const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
    const bytes = buffer.subarray(0, bytesRead);
    if (startsWith(bytes, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) return "image/png";
    if (startsWith(bytes, [0xff, 0xd8, 0xff])) return "image/jpeg";
    if (startsWith(bytes, [0x47, 0x49, 0x46, 0x38])) return "image/gif";
    if (startsWith(bytes, [0x52, 0x49, 0x46, 0x46]) && startsWith(bytes.subarray(8), [0x57, 0x45, 0x42, 0x50])) return "image/webp";
    return extensionMime;
  } catch {
    return extensionMime;
  } finally {
    await handle?.close();
  }
}

async function readUtf8Strict(filePath: string): Promise<string> {
  const bytes = await readFile(filePath);
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw new Error(`File is not valid UTF-8: ${filePath}`);
  }
}

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
  if (raw.length === 0 && offset !== 1) {
    return { body: "", lastReadLine, truncated: false, truncatedByBytes };
  }
  const body = lineNumbers
    ? raw
        .map((l, idx) => formatNumberedLine(start + idx + 1, l, true))
        .join("\n")
    : raw.join("\n");

  return {
    body,
    lastReadLine,
    truncated: lastReadLine < lines.length || truncatedByBytes,
    truncatedByBytes,
  };
}

// Snapshot content for later re-read diffs, but only when this read handed the
// session the entire file verbatim.
function rememberIfFull(
  sessionId: string,
  filePath: string,
  hash: string,
  content: string,
  sel: SelectResult,
  offset: number,
  lineNumbers: boolean,
): void {
  if (offset !== 1 || lineNumbers || sel.truncated || sel.body !== content) return;
  rememberSnapshot(sessionId, filePath, hash, content);
}

function truncationHint(sel: SelectResult): string {
  if (!sel.truncated) return "";
  const next = sel.lastReadLine + 1;
  return sel.truncatedByBytes
    ? `\n\n(Output truncated at ${MAX_BYTES} bytes. Use offset=${next} and limit, or force=true to re-read.)`
    : `\n\n(File has more lines. Use offset=${next} and limit, or force=true to re-read.)`;
}

export const readTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__read",
  annotations: { readOnlyHint: true },
  description:
    "Use instead of Read. Returns a stub if the file is unchanged since the last read. Images return as image blocks, PDFs as resources; binary files are rejected.",
  inputSchema: {
    type: "object",
    properties: {
      file_path: {
        type: "string",
        description: "Absolute path",
      },
      offset: {
        type: "number",
        description: "1-indexed start line",
        default: 1,
      },
      limit: {
        type: "number",
        default: 200,
      },
      mode: {
        type: "string",
        enum: ["full", "signatures", "aggressive"],
        description: "signatures: TS/JS/Rust signatures only; aggressive: drop comments/blank lines. Both ignore offset/limit.",
        default: "full",
      },
      line_numbers: {
        type: "boolean",
        description:
          "Tag lines and append a file_hash/range_checksum footer for cave__edit range mode. Disables codebook compression.",
        default: false,
      },
      force: {
        type: "boolean",
        description: "Bypass dedup cache",
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
    const textDeliveryKey = `text:${mode}:offset=${offset}:limit=${limit}:numbered=${lineNumbers}`;
    // Session id injected by index.ts from MCP _meta.sessionID (opencode patch).
    // Falls back to "default" for clients that don't send _meta — preserving
    // prior single-session dedup behavior.
    const sessionId = String(args.__sessionId ?? "default");

    const ext = extname(filePath).toLowerCase();
    const mediaDeliveryKey = `media:${ext || "unknown"}`;
    let fileStat;
    try {
      fileStat = await stat(filePath);
      if (fileStat.isDirectory()) {
        const entries = await readdir(filePath);
        const rows: Array<{ name: string; type: "directory" | "file" }> = [];
        for (const entry of entries) {
          const fullPath = join(filePath, entry);
          try {
            const target = await realpath(fullPath);
            if (!containsPath(filePath, target)) continue;
            const info = await stat(fullPath);
            if (info.isDirectory()) rows.push({ name: `${entry}/`, type: "directory" });
            else if (info.isFile()) rows.push({ name: entry, type: "file" });
          } catch {
            // Skip entries that disappeared or cannot be statted.
          }
        }
        rows.sort((a, b) => (a.type === b.type ? a.name.localeCompare(b.name) : a.type === "directory" ? -1 : 1));
        const selected = rows.slice(offset - 1, offset - 1 + limit).map((entry) => entry.name);
        if (selected.length === 0 && offset !== 1) {
          return { content: [{ type: "text", text: `Offset ${offset} is out of range` }], isError: true };
        }
        let text = selected.length ? selected.join("\n") : "(empty directory)";
        if (offset - 1 + selected.length < rows.length) text += `\n\n(Directory has more entries. Use 'offset'=${offset + selected.length})`;
        return { content: [{ type: "text", text: applyBudget(text, "read") }] };
      }
    } catch {
      // Let the normal file read path surface the detailed missing-path error.
    }

    const imageMime = await sniffImageMime(filePath, ext);

    if (imageMime) {
      try {
        if (!force && (await isFileUnchanged(filePath, sessionId, mediaDeliveryKey))) {
          return {
            content: [
              { type: "text", text: readStubFor(filePath, sessionId) },
            ],
          };
        }

        const imageStat = fileStat ?? (await stat(filePath));
        if (imageStat.size > MAX_IMAGE_BYTES) {
          return {
            content: [
              {
                type: "text",
                text: `Image too large (${imageStat.size} bytes, max ${MAX_IMAGE_BYTES}). Resize or crop before reading.`,
              },
            ],
            isError: true,
          };
        }

        // SVG: send as text (it's just XML), keeps the image route for raster only.
        if (ext === ".svg") {
          const svg = await readUtf8Strict(filePath);
          await updateFileCache(filePath, sessionId, {
            deliveryKey: mediaDeliveryKey,
            content: svg,
            hash: hashContent(svg),
            mtimeMs: imageStat.mtimeMs,
            emittedChars: svg.length,
          });
          return {
            content: [{ type: "text", text: svg }],
          };
        }

        const buf = await readFile(filePath);
        const data = buf.toString("base64");
        await updateFileCache(filePath, sessionId, {
          deliveryKey: mediaDeliveryKey,
          content: buf,
          hash: hashContent(buf),
          mtimeMs: imageStat.mtimeMs,
          emittedChars: data.length,
        });

        return {
          content: [
            {
              type: "image",
              data,
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
        if (!force && (await isFileUnchanged(filePath, sessionId, mediaDeliveryKey))) {
          return {
            content: [
              { type: "text", text: readStubFor(filePath, sessionId) },
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
        const blob = buf.toString("base64");
        await updateFileCache(filePath, sessionId, {
          deliveryKey: mediaDeliveryKey,
          content: buf,
          hash: hashContent(buf),
          mtimeMs: fileStat.mtimeMs,
          emittedChars: blob.length,
        });
        return {
          content: [
            {
              type: "resource",
              resource: {
                uri: `file://${filePath}`,
                mimeType: "application/pdf",
                blob,
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

    // Plain full re-read of a file whose last full content this session
    // received is snapshotted: answer with a diff when it changed. Checked
    // before the recently-edited bypass on purpose — a diff is lossless, and
    // re-reading right after an edit is where it saves the most.
    if (mode === "full" && offset === 1 && !lineNumbers && !force && !isSubagentContext()) {
      const prev = getSnapshot(sessionId, filePath);
      if (prev) {
        try {
          const content = await readUtf8Strict(filePath);
          const contentHash = hashContent(content);
          if (contentHash !== prev.hash) {
            const sel = selectLines(content.split("\n"), 1, limit, false);
            const diff = !sel.truncated && sel.body === content
              ? unifiedDiff(prev.content, content)
              : null;
            if (diff && diff.length < DIFF_MAX_RATIO * content.length) {
              const text = `${DIFF_HEADER}\n${diff}`;
              rememberSnapshot(sessionId, filePath, contentHash, content);
              await updateFileCache(filePath, sessionId, {
                deliveryKey: textDeliveryKey,
                content,
                hash: contentHash,
                mtimeMs: fileStat?.mtimeMs,
                emittedChars: text.length,
              });
              recordRead(filePath, false, text.length);
              return { content: [{ type: "text", text }] };
            }
          }
        } catch {
          // Fall through; the normal path reports read errors.
        }
      }
    }

    if (shouldForceFull(filePath)) {
      try {
          const content = await readUtf8Strict(filePath);
          const contentHash = hashContent(content);
          const lines = content.split("\n");
          const sel = selectLines(lines, offset, limit, lineNumbers);
          if (!sel.body && offset !== 1) return { content: [{ type: "text", text: `Offset ${offset} is out of range` }], isError: true };
          const text = sel.body + truncationHint(sel);
          rememberIfFull(sessionId, filePath, contentHash, content, sel, offset, lineNumbers);
        await updateFileCache(filePath, sessionId, {
          deliveryKey: textDeliveryKey,
          content,
          hash: contentHash,
          mtimeMs: fileStat?.mtimeMs,
          emittedChars: text.length,
        });
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

    if (!force && (await isFileUnchanged(filePath, sessionId, textDeliveryKey))) {
      const stub = readStubFor(filePath, sessionId);
      recordRead(filePath, false, stub.length);
      return {
        content: [
          {
            type: "text",
            text: stub,
          },
        ],
      };
    }

    try {
      const content = await readUtf8Strict(filePath);
      const contentHash = hashContent(content);

      if (mode === "aggressive") {
        const compressedContent = aggressiveCompress(content, ext);
        const output =
          compressedContent.length < content.length ? compressedContent : content;
        const compressed = applyBudget(output, "read");
        const wasCompressed = compressed.length < output.length;
        await updateFileCache(filePath, sessionId, {
          deliveryKey: textDeliveryKey,
          content,
          hash: contentHash,
          mtimeMs: fileStat?.mtimeMs,
          emittedChars: compressed.length,
        });
        recordRead(filePath, wasCompressed, compressed.length);
        return {
          content: [{ type: "text", text: compressed }],
        };
      }

      if (mode === "signatures") {
        const sigs = formatSignatures(content, ext);
        const output = sigs || "(no signatures extracted for this file type)";
        const compressed = applyBudget(output, "read");
        const wasCompressed = compressed.length < output.length;
        await updateFileCache(filePath, sessionId, {
          deliveryKey: textDeliveryKey,
          content,
          hash: contentHash,
          mtimeMs: fileStat?.mtimeMs,
          emittedChars: compressed.length,
        });
        recordRead(filePath, wasCompressed, compressed.length);
        return {
          content: [{ type: "text", text: compressed }],
        };
      }

      const lines = content.split("\n");
      const sel = selectLines(lines, offset, limit, lineNumbers);
      if (!sel.body && offset !== 1) {
        return { content: [{ type: "text", text: `Offset ${offset} is out of range` }], isError: true };
      }
      const skipPipeline = fitsBudget(sel.body, "read", MAX_BYTES);

      let outText: string;
      if (lineNumbers) {
        // Numbered lines defeat cross-file codebook matching; skip it.
        outText = sel.body;
        // Footer for line-range cave__edit: file_hash + range_checksum + line tags above.
        const hash = contentHash;
        const totalLines = lines.length;
        const first = Math.max(1, offset);
        const last = sel.lastReadLine;
        const windowCs =
          last >= first ? rangeChecksum(lines, first, last) : "";
        outText +=
          `\n--- cave_edit_meta ---\n` +
          `path: ${filePath}\n` +
          `lines: ${first}-${last} of ${totalLines}\n` +
          (hash ? `file_hash: ${hash.slice(0, 16)}\n` : "") +
          (windowCs ? `range_checksum: ${first}-${last}:${windowCs}\n` : "") +
          `// cave__edit: start_line/end_line/content + expected_hash and/or expected_range_checksum\n`;
      } else if (skipPipeline) {
        outText = sel.body;
      } else {
        addCodebookFile(filePath, content);
        const { text: codebookText, legend } = compressWithCodebook(sel.body);
        outText = legend ? `${codebookText}\n\n${legend}` : codebookText;
      }
      outText += truncationHint(sel);

      if (skipPipeline) {
        rememberIfFull(sessionId, filePath, contentHash, content, sel, offset, lineNumbers);
        await updateFileCache(filePath, sessionId, {
          deliveryKey: textDeliveryKey,
          content,
          hash: contentHash,
          mtimeMs: fileStat?.mtimeMs,
          emittedChars: outText.length,
        });
        recordRead(filePath, false, outText.length);
        return { content: [{ type: "text", text: outText }] };
      }

      const compressed = applyBudget(outText, "read");
      const wasCompressed = compressed.length < sel.body.length;
      await updateFileCache(filePath, sessionId, {
        deliveryKey: textDeliveryKey,
        content,
        hash: contentHash,
        mtimeMs: fileStat?.mtimeMs,
        emittedChars: compressed.length,
      });
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
