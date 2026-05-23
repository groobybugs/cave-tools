import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  getCacheStats,
  getAllBudgets,
  isRtkAvailable,
} from "../compression/utils.js";

export const statusTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__status",
  description:
    "Show compression statistics for the current session: dedup cache hit rate, total tokens saved, per-tool budget configuration, and RTK availability.",
  inputSchema: {
    type: "object",
    properties: {},
  },
  handler: async () => {
    const stats = getCacheStats();
    const budgets = getAllBudgets();
    const rtkAvailable = isRtkAvailable();

    const report = [
      "=== Cave Tools Status ===",
      "",
      `RTK Available: ${rtkAvailable ? "Yes" : "No"}`,
      "",
      "Cache Stats:",
      `  Files tracked: ${stats.total}`,
      `  Cache hits: ${stats.hits}`,
      `  Hit rate: ${(stats.hitRate * 100).toFixed(1)}%`,
      "",
      "Budget Configuration:",
      ...Object.entries(budgets).map(
        ([name, budget]) =>
          `  ${name}: max=${budget.maxLines}, head=${budget.headLines}, tail=${budget.tailLines}`,
      ),
    ].join("\n");

    return {
      content: [
        {
          type: "text",
          text: report,
        },
      ],
    };
  },
};
