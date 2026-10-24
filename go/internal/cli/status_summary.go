// Status summary and statusline suffix.
//
// Port of the status/stats half of src/cli.ts (buildStatusSummary,
// formatStatuslineSuffix, writeStatuslineSuffix). The renderer itself lives in
// status_ui.go.
package cli

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// statusSummary is the aggregated view the status command renders.
// port of src/cli.ts:StatusSummary
type statusSummary struct {
	RtkAvailable          bool
	ReductionPct          float64
	HitRatePct            float64
	CacheHits             int64
	CacheMisses           int64
	FilesTracked          int64
	RtkRewrites           int64
	RtkAlreadyWrapped     int64
	RtkPassthrough        int64
	TotalCalls            int64
	RawChars              int64
	CompressedChars       int64
	CompressionSavedChars int64
	DedupSavedChars       int64
	SavedChars            int64
	TokensSaved           int64
	LiveSessions          int64
	EndedSessions         int64
	VisibleSessions       int64
	LastUpdate            int64
	HasData               bool
	Budgets               []BudgetEntry
	ByTool                []ToolBreakdown
}

// knownBudgetOrder is the declaration order of the default budget table in
// src/compression/utils.ts. Status renders Object.entries(budgets), so the
// defaults must keep that order; unknown (custom) tools sort after them.
var knownBudgetOrder = []string{"bash", "read", "grep", "web", "find", "ls"}

// buildStatusSummary aggregates lifetime + visible-session stats.
// port of src/cli.ts:buildStatusSummary
func buildStatusSummary() statusSummary {
	compression.PruneDeadSessions()
	rtkAvailable := compression.IsRtkAvailable()
	lifetime := compression.GetLifetimeStats()
	sessions := compression.ListSessionStats(false)

	liveSessions := make([]storage.SessionStatsRow, 0, len(sessions))
	for _, session := range sessions {
		if rt.IsProcessAlive(int(session.Pid)) {
			liveSessions = append(liveSessions, session)
		}
	}
	visibleSessions := sessions
	if len(liveSessions) > 0 {
		visibleSessions = liveSessions
	}

	var latest *storage.SessionStatsRow
	for i := range visibleSessions {
		session := &visibleSessions[i]
		if latest == nil || session.UpdatedAt > latest.UpdatedAt {
			latest = session
		}
	}
	var budgetSource map[string]storage.BudgetConfig
	if latest != nil && latest.Budgets != nil {
		budgetSource = latest.Budgets
	} else {
		budgetSource = compression.GetAllBudgets()
	}

	filesTracked := int64(0)
	hits := lifetime.Hits
	misses := lifetime.Misses
	rawChars := lifetime.RawChars
	compressedChars := lifetime.CompressedChars
	compressionSavedChars := lifetime.CompressionSavedChars
	dedupSavedChars := lifetime.DedupSavedChars
	savedChars := lifetime.SavedChars
	calls := lifetime.Calls
	rtkRewrites := lifetime.RtkRewrites
	rtkAlreadyWrapped := lifetime.RtkAlreadyWrapped
	rtkPassthrough := lifetime.RtkPassthrough

	for _, session := range visibleSessions {
		compressionSaved := session.Savings.CompressionSavedChars
		dedup := session.Savings.DedupSavedChars
		filesTracked += session.Cache.FilesTracked
		hits += session.Cache.Hits
		misses += session.Cache.Misses
		rawChars += session.Savings.RawChars
		compressedChars += session.Savings.CompressedChars
		compressionSavedChars += compressionSaved
		dedupSavedChars += dedup
		savedChars += compressionSaved + dedup
		calls += session.Savings.TotalCalls
		rtkRewrites += session.Rtk.Rewrites
		rtkAlreadyWrapped += session.Rtk.AlreadyWrapped
		rtkPassthrough += session.Rtk.Passthrough
	}

	cacheTotal := hits + misses
	hitRate := 0.0
	if cacheTotal > 0 {
		hitRate = float64(hits) / float64(cacheTotal)
	}
	compressionPct := compression.ReductionPercent(rawChars, compressionSavedChars)

	lastUpdate := int64(0)
	for _, session := range visibleSessions {
		if session.UpdatedAt > lastUpdate {
			lastUpdate = session.UpdatedAt
		}
	}
	if lifetime.UpdatedAt > 0 && lifetime.UpdatedAt > lastUpdate {
		lastUpdate = lifetime.UpdatedAt
	}

	// Aggregate by-tool from all sessions (live + ended) for the breakdown
	// table. TS relies on object insertion order; Go maps are unordered, so
	// each session's names are visited alphabetically. The renderer sorts by
	// saved chars, leaving only exact ties implementation-dependent.
	byToolOrder := []string{}
	byToolMap := map[string]*ToolBreakdown{}
	for _, session := range compression.ListSessionStats(true) {
		names := make([]string, 0, len(session.Savings.ByTool))
		for name := range session.Savings.ByTool {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			tool := session.Savings.ByTool[name]
			current := byToolMap[name]
			if current == nil {
				current = &ToolBreakdown{Name: name}
				byToolMap[name] = current
				byToolOrder = append(byToolOrder, name)
			}
			current.Calls += tool.Calls
			current.RawChars += tool.RawChars
			current.SavedChars += tool.SavedChars
		}
	}
	byTool := make([]ToolBreakdown, 0, len(byToolOrder))
	for _, name := range byToolOrder {
		byTool = append(byTool, *byToolMap[name])
	}

	return statusSummary{
		RtkAvailable:          rtkAvailable,
		ReductionPct:          compressionPct,
		HitRatePct:            hitRate * 100,
		CacheHits:             hits,
		CacheMisses:           misses,
		FilesTracked:          filesTracked,
		RtkRewrites:           rtkRewrites,
		RtkAlreadyWrapped:     rtkAlreadyWrapped,
		RtkPassthrough:        rtkPassthrough,
		TotalCalls:            calls,
		RawChars:              rawChars,
		CompressedChars:       compressedChars,
		CompressionSavedChars: compressionSavedChars,
		DedupSavedChars:       dedupSavedChars,
		SavedChars:            savedChars,
		TokensSaved:           jsMathRound(float64(savedChars) / 4),
		LiveSessions:          int64(len(liveSessions)),
		EndedSessions:         lifetime.BankedSessions,
		VisibleSessions:       int64(len(visibleSessions)),
		LastUpdate:            lastUpdate,
		HasData:               len(visibleSessions) > 0 || lifetime.BankedSessions > 0,
		Budgets:               budgetEntries(budgetSource),
		ByTool:                byTool,
	}
}

// jsMathRound mirrors Math.round: floor(x + 0.5), including negative halves
// rounding toward positive infinity.
func jsMathRound(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int64(math.Floor(value + 0.5))
}

// budgetEntries flattens the budget table into render order.
func budgetEntries(budgets map[string]storage.BudgetConfig) []BudgetEntry {
	if len(budgets) == 0 {
		return nil
	}
	order := make([]string, 0, len(budgets))
	for _, name := range knownBudgetOrder {
		if _, ok := budgets[name]; ok {
			order = append(order, name)
		}
	}
	extra := make([]string, 0, len(budgets))
	for name := range budgets {
		if !containsString(knownBudgetOrder, name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)

	entries := make([]BudgetEntry, 0, len(order))
	for _, name := range order {
		budget := budgets[name]
		entries = append(entries, BudgetEntry{
			Name: name,
			Value: Budget{
				MaxLines:  budget.MaxLines,
				HeadLines: budget.HeadLines,
				TailLines: budget.TailLines,
			},
		})
	}
	return entries
}

// formatStatuslineSuffix renders the compact statusline badge.
// port of src/cli.ts:formatStatuslineSuffix
func formatStatuslineSuffix(summary statusSummary) string {
	if !summary.HasData {
		return ""
	}
	pct := jsToFixed(summary.ReductionPct, 0)
	tokens := summary.TokensSaved
	var amount string
	switch {
	case tokens >= 1_000_000:
		amount = jsToFixed(float64(tokens)/1_000_000, 1) + "M tok"
	case tokens >= 1000:
		amount = jsToFixed(float64(tokens)/1000, 0) + "k tok"
	case tokens > 0:
		amount = strconv.FormatInt(tokens, 10) + " tok"
	default:
		amount = strconv.FormatInt(summary.CacheHits, 10) + "cache"
	}
	arrow := "↓"
	bullet := "•"
	if os.Getenv("CAVE_TOOLS_ASCII") == "1" {
		arrow = "v"
		bullet = "|"
	}
	return arrow + pct + "% " + bullet + " " + amount
}

// writeStatuslineSuffix writes the badge file, refusing to follow a symlinked
// target and verifying ownership when the Claude config dir itself is a
// symlink. Best-effort: every failure is silent.
// port of src/cli.ts:writeStatuslineSuffix
func writeStatuslineSuffix(suffix string) {
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeDir == "" {
		home, _ := os.UserHomeDir()
		claudeDir = filepath.Join(home, ".claude")
	}
	suffixPath := filepath.Join(claudeDir, ".cave-tools-statusline-suffix")
	if err := os.MkdirAll(claudeDir, 0o777); err != nil {
		return
	}
	realDir := claudeDir
	info, err := os.Lstat(claudeDir)
	if err != nil {
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(claudeDir)
		if err != nil {
			return
		}
		stat, err := os.Stat(resolved)
		if err != nil || !stat.IsDir() {
			return
		}
		if !statuslineDirOwnerOK(stat) {
			return
		}
		realDir = resolved
	}
	realPath := filepath.Join(realDir, filepath.Base(suffixPath))
	if existing, err := os.Lstat(realPath); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			return
		}
	} else if !os.IsNotExist(err) {
		return
	}
	file, err := os.OpenFile(realPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = io.WriteString(file, suffix)
}
