// Status summary and statusline suffix wrappers.
//
// The aggregation, badge rendering and atomic write live in
// internal/statusline so the MCP server can refresh the badge in-process
// instead of spawning `cave-tools status --emit-statusline` per tool call.
// Port of src/cli.ts and src/statusline.ts.
package cli

import (
	"github.com/groobybugs/cave-tools/go/internal/statusline"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

type statusSummary = statusline.Summary
type ToolBreakdown = statusline.ToolBreakdown
type Budget = statusline.Budget
type BudgetEntry = statusline.BudgetEntry

// buildStatusSummary aggregates lifetime + visible-session stats.
// port of src/cli.ts:buildStatusSummary
func buildStatusSummary() statusSummary { return statusline.Build() }

// formatStatuslineSuffix renders the compact statusline badge.
// port of src/cli.ts:formatStatuslineSuffix
func formatStatuslineSuffix(summary statusSummary) string { return statusline.Format(summary) }

// buildStatuslineSuffix aggregates only what the badge needs (no rtk probe).
// port of src/statusline.ts:buildStatuslineSuffix
func buildStatuslineSuffix() string { return statusline.BuildSuffix() }

// writeStatuslineSuffix writes the badge file atomically.
// port of src/cli.ts:writeStatuslineSuffix
func writeStatuslineSuffix(suffix string) { statusline.Write(suffix) }

// budgetEntries flattens the budget table into render order.
func budgetEntries(budgets map[string]storage.BudgetConfig) []BudgetEntry {
	return statusline.BudgetEntries(budgets)
}

// jsToFixed mirrors Number.prototype.toFixed.
func jsToFixed(x float64, digits int) string { return statusline.JsToFixed(x, digits) }

// jsMathRound mirrors Math.round.
func jsMathRound(value float64) int64 { return statusline.JsMathRound(value) }
