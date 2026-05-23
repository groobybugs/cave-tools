import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { applyBudget, extractStructuredData } from "../compression/utils.js";

export const compressTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__compress",
  description:
    "Compress any text through the full pipeline: ANSI stripping, blank line collapse, Flint Chipper, and optional Stone Tablet structured extraction.",
  inputSchema: {
    type: "object",
    properties: {
      text: {
        type: "string",
        description: "The text to compress",
      },
      command_hint: {
        type: "string",
        description:
          "The command that produced this output (for structured extraction)",
      },
      structured: {
        type: "boolean",
        description: "Whether to apply Stone Tablet JSON/XML extraction",
        default: true,
      },
      tool_name: {
        type: "string",
        description: "Tool name for budget selection (bash, read, grep)",
        default: "bash",
      },
    },
    required: ["text"],
  },
  handler: async (args) => {
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
