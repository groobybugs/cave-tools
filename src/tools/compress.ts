import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget, extractStructuredData } from "../compression/utils.js";
import { expandArchive } from "../compression/archive.js";

export const compressTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__compress",
  annotations: { readOnlyHint: true },
  description:
    "Compress text to fewer tokens (ANSI strip, line budget, JSON/XML extraction). action='expand' + archive_id retrieves archived output.",
  inputSchema: {
    type: "object",
    properties: {
      text: {
        type: "string",
      },
      command_hint: {
        type: "string",
        description: "Command that produced the text",
      },
      structured: {
        type: "boolean",
        description: "JSON/XML extraction",
        default: true,
      },
      tool_name: {
        type: "string",
        description: "Budget to apply (bash, read, grep)",
        default: "bash",
      },
      action: {
        type: "string",
        enum: ["compress", "expand"],
        default: "compress",
      },
      archive_id: {
        type: "string",
        description: "For action='expand'",
      },
    },
    required: ["text"],
  },
  handler: async (args) => {
    const action = String(args.action || "compress");

    if (action === "expand") {
      const archiveId = String(args.archive_id || "");
      if (!archiveId) {
        return {
          content: [{ type: "text", text: "Error: archive_id required" }],
          isError: true,
        };
      }
      const expanded = await expandArchive(archiveId);
      if (expanded === null) {
        return {
          content: [{ type: "text", text: `Archive not found: ${archiveId}` }],
          isError: true,
        };
      }
      return {
        content: [{ type: "text", text: expanded }],
      };
    }

    const text = String(args.text);
    const commandHint = args.command_hint
      ? String(args.command_hint)
      : undefined;
    const structured = args.structured !== false;
    const toolName = String(args.tool_name || "bash");

    let result = text;

    if (structured) {
      result = extractStructuredData(result, commandHint);
    }

    result = applyBudget(result, toolName);

    return {
      content: [
        {
          type: "text",
          text: result,
        },
      ],
    };
  },
};
