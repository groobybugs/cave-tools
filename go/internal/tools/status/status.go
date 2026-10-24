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

	"github.com/groobybugs/cave-tools/go/internal/compression"
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

// handleStatus builds the status report.
// port of src/tools/status.ts:statusTool.handler
func handleStatus(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	_ = args
	stats := compression.GetCacheStats(ctx)
	savings := compression.GetSavingsStats(ctx)
	rtk := compression.GetRtkStats(ctx)
	bounces := compression.GetBounceStatsView(ctx)
	rangeEdits := compression.GetRangeEditStats(ctx)
	archives := compression.GetArchiveStats()
	budgets := compression.GetAllBudgetEntries()
	rtkAvailable := compression.IsRtkAvailable()
	compressionPct := compression.ReductionPercent(
		savings.RawChars,
		savings.CompressionSavedChars,
	)

	bounceExts := make([]string, 0, len(bounces.Order))
	for _, ext := range bounces.Order {
		entry := bounces.Summary.ByExtension[ext]
		if entry.Bounces > 0 {
			bounceExts = append(bounceExts, ext)
		}
	}
	sort.SliceStable(bounceExts, func(i, j int) bool {
		left := bounces.Summary.ByExtension[bounceExts[i]]
		right := bounces.Summary.ByExtension[bounceExts[j]]
		return left.Bounces > right.Bounces
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
		"  Total bounces:      " + strconv.FormatInt(bounces.Summary.TotalBounces, 10),
		"  Wasted chars:       " + strconv.FormatInt(bounces.Summary.TotalWastedChars, 10),
	}
	for _, ext := range bounceExts {
		entry := bounces.Summary.ByExtension[ext]
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
	for _, entry := range budgets {
		budget := entry.Budget
		line := "  " + entry.Name + ": max=" + jsNumberToString(budget.MaxLines) +
			", head=" + jsNumberToString(budget.HeadLines) +
			", tail=" + jsNumberToString(budget.TailLines)
		if budget.MaxChars != nil && *budget.MaxChars != 0 && !math.IsNaN(*budget.MaxChars) {
			line += ", chars=" + jsNumberToString(*budget.MaxChars)
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

// jsNumberToString mirrors ECMAScript Number::toString(10).
func jsNumberToString(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	repr := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentPart, _ := strings.Cut(repr, "e")
	exponent, _ := strconv.Atoi(exponentPart)
	digits := strings.Replace(mantissa, ".", "", 1)
	n := len(digits)
	k := exponent + 1
	var out string
	switch {
	case k > 0 && k <= 21:
		if n <= k {
			out = digits + strings.Repeat("0", k-n)
		} else {
			out = digits[:k] + "." + digits[k:]
		}
	case k <= 0 && k > -6:
		out = "0." + strings.Repeat("0", -k) + digits
	default:
		if n == 1 {
			out = digits
		} else {
			out = digits[:1] + "." + digits[1:]
		}
		exponentOut := k - 1
		if exponentOut >= 0 {
			out += "e+" + strconv.Itoa(exponentOut)
		} else {
			out += "e-" + strconv.Itoa(-exponentOut)
		}
	}
	if negative {
		return "-" + out
	}
	return out
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
