import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  getCacheStats,
  getAllBudgets,
  getSavingsStats,
  reductionPercent,
  efficiencyMeter,
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
    const savings = getSavingsStats();
    const budgets = getAllBudgets();
    const rtkAvailable = isRtkAvailable();
    const overallPct = reductionPercent(
      savings.rawChars,
      savings.savedChars,
      savings.dedupSavedChars,
    );

    const report = [
      "=== Cave Tools Status ===",
      "",
      `RTK Available: ${rtkAvailable ? "Yes" : "No"}`,
      "",
      "Cache Stats:",
      `  Files tracked: ${stats.filesTracked}`,
      `  Cache hits:    ${stats.hits}`,
      `  Cache misses:  ${stats.misses}`,
      `  Hit rate:      ${(stats.hitRate * 100).toFixed(1)}%`,
      "",
      "Token Savings:",
      `  Calls:                  ${savings.totalCalls}`,
      `  Raw chars:              ${savings.rawChars}`,
      `  Compressed chars:       ${savings.compressedChars}`,
      `  Compression saved chars: ${savings.compressionSavedChars}`,
      `  Dedup saved chars:      ${savings.dedupSavedChars}`,
      `  Saved chars:            ${savings.savedChars}`,
      `  Estimated tokens saved: ${savings.estimatedTokensSaved}`,
      `  Reduction:              ${overallPct.toFixed(1)}%`,
      `  Efficiency meter: ${efficiencyMeter(overallPct)} ${overallPct.toFixed(1)}%`,
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
