// cave__status: session stats, budgets and savings report.
//
// port of src/tools/status.ts
package status

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Tool is the MCP definition of cave__status.
// port of src/tools/status.ts:statusTool
var Tool = types.ToolDefinition{
	Name:        "cave__status",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Session stats: cache hit rate, tokens saved, budgets, RTK availability.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	Handler:     handleStatus,
}

// defaultBudgetOrder is the TS `budgets` object's insertion order.
// port of src/tools/status.ts:statusTool.handler
var defaultBudgetOrder = []string{"bash", "read", "grep", "web", "find", "ls"}

var (
	budgetOrderMu    sync.Mutex
	extraBudgetNames []string
)

// RecordBudgetSet preserves the insertion order of budgets added through
// cave__configure. TS keeps it in the module's object; Go maps lose it, and
// the status report iterates Object.entries(budgets).
func RecordBudgetSet(name string) {
	budgetOrderMu.Lock()
	defer budgetOrderMu.Unlock()
	for _, known := range defaultBudgetOrder {
		if known == name {
			return
		}
	}
	for _, known := range extraBudgetNames {
		if known == name {
			return
		}
	}
	extraBudgetNames = append(extraBudgetNames, name)
}

// orderedBudgetNames returns TS's Object.entries(budgets) key order.
func orderedBudgetNames(budgets map[string]storage.BudgetConfig) []string {
	budgetOrderMu.Lock()
	extras := append([]string(nil), extraBudgetNames...)
	budgetOrderMu.Unlock()

	names := make([]string, 0, len(budgets))
	seen := make(map[string]bool, len(budgets))
	for _, name := range defaultBudgetOrder {
		if _, ok := budgets[name]; ok {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, name := range extras {
		if _, ok := budgets[name]; ok && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	// Budgets set outside configure (tests) are not ordered; sort them so the
	// report stays deterministic.
	remaining := make([]string, 0)
	for name := range budgets {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return append(names, remaining...)
}

// handleStatus builds the status report.
// port of src/tools/status.ts:statusTool.handler
func handleStatus(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	_ = args
	stats := compression.GetCacheStats(ctx)
	savings := compression.GetSavingsStats(ctx)
	rtk := compression.GetRtkStats(ctx)
	bounces := compression.GetBounceStats(ctx)
	rangeEdits := compression.GetRangeEditStats(ctx)
	archives := compression.GetArchiveStats()
	budgets := compression.GetAllBudgets()
	rtkAvailable := compression.IsRtkAvailable()
	compressionPct := compression.ReductionPercent(
		savings.RawChars,
		savings.CompressionSavedChars,
	)

	bounceExts := make([]string, 0, len(bounces.ByExtension))
	for ext, entry := range bounces.ByExtension {
		if entry.Bounces > 0 {
			bounceExts = append(bounceExts, ext)
		}
	}
	sort.SliceStable(bounceExts, func(i, j int) bool {
		left := bounces.ByExtension[bounceExts[i]]
		right := bounces.ByExtension[bounceExts[j]]
		if left.Bounces != right.Bounces {
			return left.Bounces > right.Bounces
		}
		return bounceExts[i] < bounceExts[j]
	})
	if len(bounceExts) > 5 {
		bounceExts = bounceExts[:5]
	}

	lines := []string{
		"=== Cave Tools Status ===",
		"",
		"RTK Available: " + yesNo(rtkAvailable),
		"",
		"Cache Stats:",
		"  Files tracked: " + strconv.FormatInt(stats.FilesTracked, 10),
		"  Cache hits:    " + strconv.FormatInt(stats.Hits, 10),
		"  Cache misses:  " + strconv.FormatInt(stats.Misses, 10),
		"  Hit rate:      " + jsToFixed(stats.HitRate*100, 1) + "%",
		"",
		"RTK Rewrites:",
		"  Rewritten:       " + strconv.FormatInt(rtk.Rewrites, 10),
		"  Already wrapped: " + strconv.FormatInt(rtk.AlreadyWrapped, 10),
		"  Passthrough:     " + strconv.FormatInt(rtk.Passthrough, 10),
		"",
		"Output trimming:",
		"  Calls:            " + strconv.FormatInt(savings.TotalCalls, 10),
		"  Raw chars:        " + strconv.FormatInt(savings.RawChars, 10),
		"  Trimmed chars:    " + strconv.FormatInt(savings.CompressedChars, 10),
		"  Saved chars:      " + strconv.FormatInt(savings.CompressionSavedChars, 10),
		"  Reduction:        " + jsToFixed(compressionPct, 1) + "%",
		"  Meter: " + compression.EfficiencyMeter(compressionPct) + " " + jsToFixed(compressionPct, 1) + "%",
		"",
		"Dedup (avoided re-reads):",
		"  Cache hits:    " + strconv.FormatInt(stats.Hits, 10),
		"  Chars avoided: " + strconv.FormatInt(savings.DedupSavedChars, 10) + " (budget-capped)",
		"",
		"Range/move edits (no old_string echo):",
		"  Edits:              " + strconv.FormatInt(rangeEdits.Edits, 10),
		"  Echo chars saved:   " + strconv.FormatInt(rangeEdits.EchoSavedChars, 10),
		"  Est. tokens saved:  " + strconv.FormatInt(rangeEdits.EstimatedTokensSaved, 10),
		"",
		"Bounces (trimmed read → full re-read):",
		"  Total bounces:      " + strconv.FormatInt(bounces.TotalBounces, 10),
		"  Wasted chars:       " + strconv.FormatInt(bounces.TotalWastedChars, 10),
	}
	for _, ext := range bounceExts {
		entry := bounces.ByExtension[ext]
		percent := "0"
		if entry.TotalReads > 0 {
			percent = jsToFixed((float64(entry.Bounces)/float64(entry.TotalReads))*100, 0)
		}
		lines = append(lines,
			"  "+ext+": "+strconv.FormatInt(entry.Bounces, 10)+"/"+strconv.FormatInt(entry.TotalReads, 10)+
				" reads bounced ("+percent+"%), "+strconv.FormatInt(entry.WastedChars, 10)+" chars wasted")
	}
	lines = append(lines,
		"",
		"Archives (large outputs on disk):",
		"  Count:       "+strconv.FormatInt(archives.Count, 10),
		"  Total chars: "+strconv.FormatInt(archives.TotalChars, 10),
		"",
		"Codebook (cross-file boilerplate dedup):",
		"  Entries: "+strconv.Itoa(compression.GetCodebookSize()),
		"",
		"Total est. tokens saved: "+strconv.FormatInt(savings.EstimatedTokensSaved, 10),
		"",
		"Budget Configuration:",
	)
	for _, name := range orderedBudgetNames(budgets) {
		budget := budgets[name]
		line := "  " + name + ": max=" + strconv.FormatInt(budget.MaxLines, 10) +
			", head=" + strconv.FormatInt(budget.HeadLines, 10) +
			", tail=" + strconv.FormatInt(budget.TailLines, 10)
		if budget.MaxChars != nil && *budget.MaxChars != 0 {
			line += ", chars=" + strconv.FormatInt(*budget.MaxChars, 10)
		}
		lines = append(lines, line)
	}

	return types.ToolResult{Content: []types.Content{types.Text(strings.Join(lines, "\n"))}}, nil
}

// yesNo renders JS's `cond ? "Yes" : "No"`.
func yesNo(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

// jsToFixed mirrors Number.prototype.toFixed, including its round-half-away
// rule: Go's strconv rounds half to even, which would disagree on exact ties.
// (Same helper as go/internal/cli/status_ui.go; tools cannot import the CLI.)
func jsToFixed(x float64, digits int) string {
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.IsInf(x, 1) {
		return "Infinity"
	}
	if math.IsInf(x, -1) {
		return "-Infinity"
	}
	if math.Abs(x) >= 1e21 {
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	neg := x < 0
	abs := math.Abs(x)
	rat := new(big.Rat).SetFloat64(abs)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	rat.Mul(rat, new(big.Rat).SetInt(scale))
	q, rem := new(big.Int).QuoRem(rat.Num(), rat.Denom(), new(big.Int))
	rem.Abs(rem)
	rem.Lsh(rem, 1)
	if rem.Cmp(rat.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	out := q.String()
	if digits > 0 {
		for len(out) <= digits {
			out = "0" + out
		}
		out = out[:len(out)-digits] + "." + out[len(out)-digits:]
	}
	if neg {
		return "-" + out
	}
	return out
}
