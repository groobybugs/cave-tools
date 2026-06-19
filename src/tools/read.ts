import type { ToolResult } from "../types.js";
import { readFile, stat } from "fs/promises";
import { extname } from "path";
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

export const readTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__read",
  description:
    "Optimized drop-in replacement for the built-in Read. Returns the same file content, but sends a stub when the file is unchanged since the last read in this session (skips re-sending identical content). Image files (.png, .jpg, .jpeg, .gif, .webp, .bmp, .svg) are returned as image content blocks (base64); offset/limit are ignored for images, signatures, and aggressive modes. Use mode='signatures' to return only function/class/type signatures (TS/JS/Rust). Use mode='aggressive' to drop comments and blank lines.",
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

    if (shouldForceFull(filePath)) {
      try {
        const content = await readFile(filePath, "utf-8");
        const lines = content.split("\n");
        const start = Math.max(0, offset - 1);
        const end = Math.min(lines.length, start + limit);
        const selected = lines.slice(start, end).join("\n");
        await updateFileCache(filePath);
        recordRead(filePath, false, selected.length);
        return {
          content: [{ type: "text", text: selected }],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error reading file: ${error instanceof Error ? error.message : String(error)}`,
            },
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
      const start = Math.max(0, offset - 1);
      const end = Math.min(lines.length, start + limit);
      const selected = lines.slice(start, end).join("\n");

      await updateFileCache(filePath);
      addCodebookFile(filePath, content);

      const { text: codebookText, legend } = compressWithCodebook(selected);
      const withLegend = legend ? `${codebookText}\n\n${legend}` : codebookText;

      const compressed = applyBudget(withLegend, "read");
      const wasCompressed = compressed.length < selected.length;
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
          {
            type: "text",
            text: `Error reading file: ${error instanceof Error ? error.message : String(error)}`,
          },
        ],
        isError: true,
      };
    }
  },
};
