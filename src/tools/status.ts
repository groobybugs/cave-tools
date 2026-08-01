import type { ToolResult } from "../types.js";
import type { Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  getCacheStats,
  getAllBudgets,
  getSavingsStats,
  getRtkStats,
  getBounceStats,
  getRangeEditStats,
  reductionPercent,
  efficiencyMeter,
  isRtkAvailable,
} from "../compression/utils.js";
import { getArchiveStats } from "../compression/archive.js";
import { getCodebookSize } from "../compression/codebook.js";

export const statusTool: Tool & {
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
} = {
  name: "cave__status",
  description:
    "Show token-optimization stats for the current session: dedup cache hit rate, total tokens saved, per-tool output budgets, and RTK availability.",
  inputSchema: {
    type: "object",
    properties: {},
  },
  handler: async () => {
    const stats = getCacheStats();
    const savings = getSavingsStats();
    const rtk = getRtkStats();
    const bounces = getBounceStats();
    const rangeEdits = getRangeEditStats();
    const archives = await getArchiveStats();
    const budgets = getAllBudgets();
    const rtkAvailable = await isRtkAvailable();
    const compressionPct = reductionPercent(
      savings.rawChars,
      savings.compressionSavedChars,
    );

    const bounceExts = Object.entries(bounces.byExtension)
      .filter(([, s]) => s.bounces > 0)
      .sort((a, b) => b[1].bounces - a[1].bounces)
      .slice(0, 5);

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
      "RTK Rewrites:",
      `  Rewritten:       ${rtk.rewrites}`,
      `  Already wrapped: ${rtk.alreadyWrapped}`,
      `  Passthrough:     ${rtk.passthrough}`,
      "",
      "Output trimming:",
      `  Calls:            ${savings.totalCalls}`,
      `  Raw chars:        ${savings.rawChars}`,
      `  Trimmed chars:    ${savings.compressedChars}`,
      `  Saved chars:      ${savings.compressionSavedChars}`,
      `  Reduction:        ${compressionPct.toFixed(1)}%`,
      `  Meter: ${efficiencyMeter(compressionPct)} ${compressionPct.toFixed(1)}%`,
      "",
      "Dedup (avoided re-reads):",
      `  Cache hits:    ${stats.hits}`,
      `  Chars avoided: ${savings.dedupSavedChars} (budget-capped)`,
      "",
      "Range/move edits (no old_string echo):",
      `  Edits:              ${rangeEdits.edits}`,
      `  Echo chars saved:   ${rangeEdits.echoSavedChars}`,
      `  Est. tokens saved:  ${rangeEdits.estimatedTokensSaved}`,
      "",
      "Bounces (trimmed read → full re-read):",
      `  Total bounces:      ${bounces.totalBounces}`,
      `  Wasted chars:       ${bounces.totalWastedChars}`,
      ...bounceExts.map(
        ([ext, s]) =>
          `  ${ext}: ${s.bounces}/${s.totalReads} reads bounced (${
            s.totalReads > 0 ? ((s.bounces / s.totalReads) * 100).toFixed(0) : 0
          }%), ${s.wastedChars} chars wasted`,
      ),
      "",
      "Archives (large outputs on disk):",
      `  Count:       ${archives.count}`,
      `  Total chars: ${archives.totalChars}`,
      "",
      "Codebook (cross-file boilerplate dedup):",
      `  Entries: ${getCodebookSize()}`,
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
