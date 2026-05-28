import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import { resetCache, resetStats, setBudget } from "../compression/utils.js";

type ConfigAction = "set_budget" | "reset_cache" | "reset_stats";

export const configureTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__configure",
  description:
    "Configure Cave Tools settings: set per-tool budgets, reset cache, or reset statistics.",
  inputSchema: {
    type: "object",
    properties: {
      action: {
        type: "string",
        enum: ["set_budget", "reset_cache", "reset_stats"],
        description: "Configuration action to perform",
      },
      tool_name: {
        type: "string",
        description: "Tool name for set_budget action (e.g., bash, read, grep)",
      },
      max_lines: {
        type: "number",
        description: "Maximum lines for set_budget",
      },
      head_lines: {
        type: "number",
        description: "Head lines for set_budget",
      },
      tail_lines: {
        type: "number",
        description: "Tail lines for set_budget",
      },
    },
    required: ["action"],
  },
  handler: async (args) => {
    const action = String(args.action) as ConfigAction;

    switch (action) {
      case "set_budget": {
        const toolName = String(args.tool_name || "bash");
        const maxLines = Number(args.max_lines) || 100;
        const headLines = Number(args.head_lines) || 50;
        const tailLines = Number(args.tail_lines) || 50;

        setBudget(toolName, maxLines, headLines, tailLines);

        return {
          content: [
            {
              type: "text",
              text: `Set budget for ${toolName}: max=${maxLines}, head=${headLines}, tail=${tailLines}`,
            },
          ],
        };
      }

      case "reset_cache": {
        resetCache();
        return {
          content: [
            {
              type: "text",
              text: "Cache reset",
            },
          ],
        };
      }

      case "reset_stats": {
        resetStats();
        return {
          content: [
            {
              type: "text",
              text: "Statistics reset",
            },
          ],
        };
      }

      default:
        return {
          content: [
            {
              type: "text",
              text: `Unknown action: ${action}`,
            },
          ],
          isError: true,
        };
    }
  },
};
