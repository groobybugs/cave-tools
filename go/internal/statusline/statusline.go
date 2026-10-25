// Package statusline aggregates the savings stats, renders the compact
// statusline badge and writes it atomically for the Claude Code statusline
// script. Both the `status --emit-statusline` CLI path and the MCP server's
// in-process refresh use it, so the badge bytes stay identical.
//
// Port of the status/stats half of src/cli.ts and src/statusline.ts. The
// renderer itself lives in internal/cli (status_ui.go).
package statusline

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// ToolBreakdown is one row of the by-tool table.
// port of src/status-ui.ts:ToolBreakdown
type ToolBreakdown struct {
	Name       string `json:"name"`
	Calls      int64  `json:"calls"`
	RawChars   int64  `json:"rawChars"`
	SavedChars int64  `json:"savedChars"`
}

// Budget is the rendered part of one tool's line budget.
// port of src/status-ui.ts:StatusRenderInput budgets value
type Budget struct {
	MaxLines  float64 `json:"maxLines"`
	HeadLines float64 `json:"headLines"`
	TailLines float64 `json:"tailLines"`
}

// BudgetEntry keeps the insertion order TS's Object.entries(budgets) renders;
// a Go map would randomize the table.
type BudgetEntry struct {
	Name  string
	Value Budget
}

// Summary is the aggregated view the status command renders.
// port of src/cli.ts:StatusSummary (+ src/statusline.ts)
type Summary struct {
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

// compressionSavedOf mirrors TS
// `s.savings.compressionSavedChars ?? s.savings.savedChars ?? 0`: the fallback
// applies only when the row did not carry the field at all (undefined), not
// when it explicitly holds 0. DB rows always carry it, so this is the same
// value they already had.
func compressionSavedOf(savings storage.SavingsStats) int64 {
	if !savings.CompressionSavedCharsSet {
		return savings.SavedChars
	}
	return savings.CompressionSavedChars
}

// BuildCore aggregates lifetime + visible-session stats without the rtk probe
// or the by-tool table (neither is part of the statusline badge).
// port of src/statusline.ts:buildStatusCore
func BuildCore() Summary {
	compression.PruneDeadSessions()
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
		compressionSaved := compressionSavedOf(session.Savings)
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

	return Summary{
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
		TokensSaved:           JsMathRound(float64(savedChars) / 4),
		LiveSessions:          int64(len(liveSessions)),
		EndedSessions:         lifetime.BankedSessions,
		VisibleSessions:       int64(len(visibleSessions)),
		LastUpdate:            lastUpdate,
		HasData:               len(visibleSessions) > 0 || lifetime.BankedSessions > 0,
		Budgets:               BudgetEntries(budgetSource),
	}
}

// Build returns the full summary: core aggregates plus the rtk probe and the
// by-tool table the status renderer needs.
// port of src/cli.ts:buildStatusSummary
func Build() Summary {
	summary := BuildCore()
	summary.RtkAvailable = compression.IsRtkAvailable()

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
	summary.ByTool = byTool
	return summary
}

// Format renders the compact statusline badge.
// port of src/cli.ts:formatStatuslineSuffix
func Format(summary Summary) string {
	if !summary.HasData {
		return ""
	}
	pct := JsToFixed(summary.ReductionPct, 0)
	tokens := summary.TokensSaved
	var amount string
	switch {
	case tokens >= 1_000_000:
		amount = JsToFixed(float64(tokens)/1_000_000, 1) + "M tok"
	case tokens >= 1000:
		amount = JsToFixed(float64(tokens)/1000, 0) + "k tok"
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

// BuildSuffix is the badge text the CLI would write for the current DB state.
// port of src/statusline.ts:buildStatuslineSuffix
func BuildSuffix() string {
	return Format(BuildCore())
}

// SuffixPath resolves the badge path with the same env/config-dir rules as the
// status command.
func SuffixPath() string {
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeDir == "" {
		home, _ := os.UserHomeDir()
		claudeDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(claudeDir, ".cave-tools-statusline-suffix")
}

// Write writes the badge file atomically (temp file + rename), refusing to
// follow a symlinked target and verifying ownership when the Claude config dir
// itself is a symlink. Best-effort: every failure is silent.
// port of src/statusline.ts:writeStatuslineSuffix
func Write(suffix string) {
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
		if !dirOwnerOK(stat) {
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

	temp, err := os.CreateTemp(realDir, ".cave-tools-statusline-suffix.*.tmp")
	if err != nil {
		return
	}
	tempPath := temp.Name()
	if _, err := temp.WriteString(suffix); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return
	}
	if err := os.Rename(tempPath, realPath); err != nil {
		_ = os.Remove(tempPath)
	}
}

// BudgetEntries flattens the budget table into render order.
func BudgetEntries(budgets map[string]storage.BudgetConfig) []BudgetEntry {
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

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// JsMathRound mirrors Math.round: floor(x + 0.5), including negative halves
// rounding toward positive infinity.
func JsMathRound(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int64(math.Floor(value + 0.5))
}
