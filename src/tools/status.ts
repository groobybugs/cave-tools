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
    const compressionPct = reductionPercent(
      savings.rawChars,
      savings.compressionSavedChars,
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
      "Compression (output trimming):",
      `  Calls:            ${savings.totalCalls}`,
      `  Raw chars:        ${savings.rawChars}`,
      `  Compressed chars: ${savings.compressedChars}`,
      `  Saved chars:      ${savings.compressionSavedChars}`,
      `  Reduction:        ${compressionPct.toFixed(1)}%`,
      `  Meter: ${efficiencyMeter(compressionPct)} ${compressionPct.toFixed(1)}%`,
      "",
      "Dedup (avoided re-reads):",
      `  Cache hits:    ${stats.hits}`,
      `  Chars avoided: ${savings.dedupSavedChars} (budget-capped)`,
      "",
      `Total est. tokens saved: ${savings.estimatedTokensSaved}`,
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
