import type { ToolResult } from "../types.js";
import { readFileSync, statSync } from "fs";
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
    "Read a file with dedup + Flint Chipper compression. Returns a stub if the file hasn't changed since the last read in the same session. Image files (.png, .jpg, .jpeg, .gif, .webp, .bmp, .svg) are returned as image content blocks (base64); offset/limit are ignored for images.",
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
        description: "Maximum number of lines to read. Ignored for images.",
        default: 200,
      },
    },
    required: ["file_path"],
  },
  handler: async (args) => {
    const filePath = String(args.file_path);
    const offset = Number(args.offset) || 1;
    const limit = Number(args.limit) || 200;

    const ext = extname(filePath).toLowerCase();
    const imageMime = IMAGE_MIME[ext];

    // --- Image branch -----------------------------------------------------
    if (imageMime) {
      try {
        if (isFileUnchanged(filePath)) {
          return {
            content: [
              { type: "text", text: "<image unchanged since last read>" },
            ],
          };
        }

        const size = statSync(filePath).size;
        if (size > MAX_IMAGE_BYTES) {
          return {
            content: [
              {
                type: "text",
                text: `Image too large (${size} bytes, max ${MAX_IMAGE_BYTES}). Resize or crop before reading.`,
              },
            ],
            isError: true,
          };
        }

        // SVG: send as text (it's just XML), keeps the image route for raster only.
        if (ext === ".svg") {
          const svg = readFileSync(filePath, "utf-8");
          updateFileCache(filePath);
          return {
            content: [{ type: "text", text: svg }],
          };
        }

        const buf = readFileSync(filePath);
        updateFileCache(filePath);

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

    // --- Text branch (original behavior) ---------------------------------
    if (isFileUnchanged(filePath)) {
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
      const content = readFileSync(filePath, "utf-8");
      const lines = content.split("\n");
      const start = Math.max(0, offset - 1);
      const end = Math.min(lines.length, start + limit);
      const selected = lines.slice(start, end).join("\n");

      updateFileCache(filePath);

      if (shouldForceFull(filePath)) {
        recordRead(filePath, false, selected.length);
        return {
          content: [
            {
              type: "text",
              text: selected,
            },
          ],
        };
      }

      const compressed = applyBudget(selected, "read");
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
