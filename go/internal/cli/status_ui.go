// Package cli is the command-line entry layer: argument dispatch, CLI-only
// tools and the status renderer.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// ansiPattern is the SGR-only stripper TS pad/visibleLen use for width
// calculations (src/status-ui.ts pad regex).
var ansiPattern = jsre.MustCompile(`\x1b\[[0-9;]*m`, "g")

// ToolBreakdown is one row of the by-tool table.
// port of src/status-ui.ts:ToolBreakdown
type ToolBreakdown struct {
	Name       string `json:"name"`
	Calls      int64  `json:"calls"`
	RawChars   int64  `json:"rawChars"`
	SavedChars int64  `json:"savedChars"`
}

// RtkGainSummary is `rtk gain -f json`'s summary block.
// port of src/status-ui.ts:RtkGainSummary
type RtkGainSummary struct {
	TotalCommands int64   `json:"totalCommands"`
	TotalInput    int64   `json:"totalInput"`
	TotalOutput   int64   `json:"totalOutput"`
	TotalSaved    int64   `json:"totalSaved"`
	AvgSavingsPct float64 `json:"avgSavingsPct"`
	TotalTimeMs   int64   `json:"totalTimeMs"`
	AvgTimeMs     int64   `json:"avgTimeMs"`
}

// Budget is the rendered part of one tool's line budget.
// port of src/status-ui.ts:StatusRenderInput budgets value
type Budget struct {
	MaxLines  int64 `json:"maxLines"`
	HeadLines int64 `json:"headLines"`
	TailLines int64 `json:"tailLines"`
}

// BudgetEntry keeps the insertion order TS's Object.entries(budgets) renders;
// a Go map would randomize the table.
type BudgetEntry struct {
	Name  string
	Value Budget
}

// StatusRenderInput is everything the status renderer displays.
// port of src/status-ui.ts:StatusRenderInput
type StatusRenderInput struct {
	RtkAvailable          bool            `json:"rtkAvailable"`
	ReductionPct          float64         `json:"reductionPct"`
	HitRatePct            float64         `json:"hitRatePct"`
	CacheHits             int64           `json:"cacheHits"`
	CacheMisses           int64           `json:"cacheMisses"`
	FilesTracked          int64           `json:"filesTracked"`
	RtkRewrites           int64           `json:"rtkRewrites"`
	RtkAlreadyWrapped     int64           `json:"rtkAlreadyWrapped"`
	RtkPassthrough        int64           `json:"rtkPassthrough"`
	TotalCalls            int64           `json:"totalCalls"`
	RawChars              int64           `json:"rawChars"`
	CompressedChars       int64           `json:"compressedChars"`
	CompressionSavedChars int64           `json:"compressionSavedChars"`
	DedupSavedChars       int64           `json:"dedupSavedChars"`
	SavedChars            int64           `json:"savedChars"`
	TokensSaved           int64           `json:"tokensSaved"`
	LiveSessions          int64           `json:"liveSessions"`
	EndedSessions         int64           `json:"endedSessions"`
	LastUpdate            int64           `json:"lastUpdate"`
	HasData               bool            `json:"hasData"`
	Budgets               []BudgetEntry   `json:"budgets"`
	ByTool                []ToolBreakdown `json:"byTool"`
	RtkGain               *RtkGainSummary `json:"rtkGain"`
	RtkGainSkipped        bool            `json:"rtkGainSkipped"`
	Verbose               bool            `json:"verbose"`
}

// ── color / style ──────────────────────────────────────────────────

// Style is one entry of TS's C palette.
type Style string

const (
	styleReset       Style = "\x1b[0m"
	styleBold        Style = "\x1b[1m"
	styleDim         Style = "\x1b[2m"
	styleAmber       Style = "\x1b[38;5;214m"
	styleGreen       Style = "\x1b[38;5;114m"
	styleBrightGreen Style = "\x1b[38;5;48m"
	styleCyan        Style = "\x1b[38;5;81m"
	styleRed         Style = "\x1b[38;5;203m"
	styleGray        Style = "\x1b[38;5;240m"
	styleWhite       Style = "\x1b[38;5;255m"
	styleYellow      Style = "\x1b[38;5;221m"
)

// port of src/status-ui.ts:envTruthy
func envTruthy(name string) bool {
	value, ok := os.LookupEnv(name)
	return ok && value != "" && value != "0" && strings.ToLower(value) != "false"
}

// SupportsColor reports whether the status UI should emit ANSI color.
//
// port of src/status-ui.ts:supportsColor
func SupportsColor() bool {
	if envTruthy("NO_COLOR") {
		return false
	}
	if os.Getenv("CAVE_TOOLS_ASCII") == "1" {
		return false
	}
	if envTruthy("FORCE_COLOR") {
		return true
	}
	return isTTY(os.Stdout)
}

// isTTY approximates Node's process.stdout.isTTY: pipes and regular files are
// not TTYs, character devices are.
func isTTY(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// UseASCII reports whether the UI must stick to ASCII glyphs.
//
// port of src/status-ui.ts:useAscii
func UseASCII() bool {
	return os.Getenv("CAVE_TOOLS_ASCII") == "1"
}

// port of src/status-ui.ts:paint
func paint(enabled bool, style Style, text string) string {
	if !enabled {
		return text
	}
	return string(style) + text + string(styleReset)
}

// port of src/status-ui.ts:multi
func multi(enabled bool, styles []Style, text string) string {
	if !enabled {
		return text
	}
	var builder strings.Builder
	for _, style := range styles {
		builder.WriteString(string(style))
	}
	builder.WriteString(text)
	builder.WriteString(string(styleReset))
	return builder.String()
}

// ── formatters ─────────────────────────────────────────────────────

// FormatCompact renders a compact number: 87525037 → 87.5M, 1200 → 1.2k.
//
// port of src/status-ui.ts:formatCompact
func FormatCompact(n int64, digits ...int64) string {
	precision := int64(1)
	if len(digits) > 0 {
		precision = digits[0]
	}
	abs := n
	sign := ""
	if n < 0 {
		sign = "-"
		abs = -n
	}
	if abs >= 1_000_000_000 {
		return sign + trimZeroFraction(jsToFixed(float64(abs)/1_000_000_000, int(precision))) + "B"
	}
	if abs >= 1_000_000 {
		return sign + trimZeroFraction(jsToFixed(float64(abs)/1_000_000, int(precision))) + "M"
	}
	if abs >= 10_000 {
		return sign + jsToFixed(float64(abs)/1_000, 0) + "k"
	}
	if abs >= 1_000 {
		return sign + trimZeroFraction(jsToFixed(float64(abs)/1_000, int(precision))) + "k"
	}
	return sign + strconv.FormatInt(abs, 10)
}

// trimZeroFraction mirrors `.replace(/\.0$/, "")`.
func trimZeroFraction(value string) string {
	return strings.TrimSuffix(value, ".0")
}

// FormatTokens renders a token count like FormatCompact.
//
// port of src/status-ui.ts:formatTokens
func FormatTokens(n int64) string {
	return FormatCompact(n)
}

// pad pads to width w after stripping ANSI, using UTF-16 length like JS.
//
// port of src/status-ui.ts:pad
func pad(s string, w int, right ...bool) string {
	bare := ansiPattern.ReplaceAllString(s, "")
	padLen := w - utf16Len(bare)
	if padLen < 0 {
		padLen = 0
	}
	spaces := strings.Repeat(" ", padLen)
	if len(right) > 0 && right[0] {
		return spaces + s
	}
	return s + spaces
}

// ageString renders the age of the last stats update.
//
// port of src/status-ui.ts:ageString
func ageString(lastUpdate int64) string {
	return ageStringAt(lastUpdate, time.Now().UnixMilli())
}

// ageStringAt is ageString with the clock pinned for tests.
func ageStringAt(lastUpdate, now int64) string {
	if lastUpdate <= 0 {
		return "—"
	}
	age := int64(math.Round(float64(now-lastUpdate) / 1000))
	if age < 60 {
		return strconv.FormatInt(age, 10) + "s ago"
	}
	if age < 3600 {
		return strconv.FormatInt(int64(math.Round(float64(age)/60)), 10) + "m ago"
	}
	return strconv.FormatInt(int64(math.Round(float64(age)/3600)), 10) + "h ago"
}

// ColoredMeter renders a percentage bar, colored by band when color is true.
//
// port of src/status-ui.ts:coloredMeter
func ColoredMeter(percent float64, width int, color bool) string {
	clamped := math.Max(0, math.Min(100, percent))
	filled := int(math.Round((clamped / 100) * float64(width)))
	empty := width - filled
	fillChar, emptyChar := "█", "░"
	if UseASCII() {
		fillChar, emptyChar = "#", "-"
	}
	bar := strings.Repeat(fillChar, filled) + strings.Repeat(emptyChar, empty)
	if !color {
		return bar
	}
	style := styleGreen
	if clamped >= 70 {
		style = styleBrightGreen
	} else if clamped >= 40 {
		style = styleAmber
	} else if clamped > 0 {
		style = styleYellow
	} else {
		style = styleGray
	}
	return paint(true, style, strings.Repeat(fillChar, filled)) +
		paint(true, styleGray, strings.Repeat(emptyChar, empty))
}

// ── RTK gain fetch ─────────────────────────────────────────────────

// FetchRtkGainSummary shells out to `rtk gain -f json`; every failure (missing
// binary, timeout, >2 MiB output, bad JSON) yields nil.
//
// port of src/status-ui.ts:fetchRtkGainSummary
func FetchRtkGainSummary() *RtkGainSummary {
	if os.Getenv("CAVE_TOOLS_STATUS_RTK") == "0" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rtk", "gain", "-f", "json")
	stdout := &limitedBuffer{max: 2 * 1024 * 1024}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return nil
	}
	var data struct {
		Summary *struct {
			TotalCommands *int64   `json:"total_commands"`
			TotalInput    *int64   `json:"total_input"`
			TotalOutput   *int64   `json:"total_output"`
			TotalSaved    *int64   `json:"total_saved"`
			AvgSavingsPct *float64 `json:"avg_savings_pct"`
			TotalTimeMs   *int64   `json:"total_time_ms"`
			AvgTimeMs     *int64   `json:"avg_time_ms"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
		return nil
	}
	summary := data.Summary
	if summary == nil {
		return nil
	}
	return &RtkGainSummary{
		TotalCommands: int64OrZero(summary.TotalCommands),
		TotalInput:    int64OrZero(summary.TotalInput),
		TotalOutput:   int64OrZero(summary.TotalOutput),
		TotalSaved:    int64OrZero(summary.TotalSaved),
		AvgSavingsPct: float64OrZero(summary.AvgSavingsPct),
		TotalTimeMs:   int64OrZero(summary.TotalTimeMs),
		AvgTimeMs:     int64OrZero(summary.AvgTimeMs),
	}
}

func int64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func float64OrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// limitedBuffer is Node's maxBuffer: writing past the cap fails the command.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		return 0, errors.New("maxBuffer exceeded")
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

// ── box drawing ────────────────────────────────────────────────────

type boxGlyphs struct {
	TL, TR, BL, BR, H, V, Mid string
}

// port of src/status-ui.ts:boxChars
func boxChars() boxGlyphs {
	if UseASCII() {
		return boxGlyphs{TL: "+", TR: "+", BL: "+", BR: "+", H: "-", V: "|", Mid: "+"}
	}
	return boxGlyphs{TL: "╭", TR: "╮", BL: "╰", BR: "╯", H: "─", V: "│", Mid: "├"}
}

// port of src/status-ui.ts:visibleLen
func visibleLen(s string) int {
	return utf16Len(ansiPattern.ReplaceAllString(s, ""))
}

// port of src/status-ui.ts:boxLine
func boxLine(content string, width int, color bool) string {
	glyphs := boxChars()
	inner := width - 2
	bare := visibleLen(content)
	padRight := inner - bare
	if padRight < 0 {
		padRight = 0
	}
	side := paint(color, styleGray, glyphs.V)
	return "  " + side + content + strings.Repeat(" ", padRight) + side
}

// port of src/status-ui.ts:boxTop
func boxTop(width int, color bool) string {
	glyphs := boxChars()
	return "  " + paint(color, styleGray, glyphs.TL+strings.Repeat(glyphs.H, width-2)+glyphs.TR)
}

// port of src/status-ui.ts:boxBottom
func boxBottom(width int, color bool) string {
	glyphs := boxChars()
	return "  " + paint(color, styleGray, glyphs.BL+strings.Repeat(glyphs.H, width-2)+glyphs.BR)
}

// port of src/status-ui.ts:rule
func rule(width int, color bool) string {
	ch := "─"
	if UseASCII() {
		ch = "-"
	}
	return paint(color, styleGray, strings.Repeat(ch, width))
}

// ── main renderer ──────────────────────────────────────────────────

// RenderStatusCLI renders the whole `cave-tools status` screen.
//
// port of src/status-ui.ts:renderStatusCli
func RenderStatusCLI(input StatusRenderInput) string {
	color := SupportsColor()
	w := 57
	icon := "⛏"
	if UseASCII() {
		icon = "*"
	}
	var lines []string

	// Header box
	lines = append(lines, "")
	lines = append(lines, boxTop(w, color))
	lines = append(lines, boxLine(
		"  "+paint(color, styleAmber, icon)+"  "+
			multi(color, []Style{styleBold, styleAmber}, "cave-tools")+
			paint(color, styleDim, " · status"),
		w, color,
	))
	lines = append(lines, boxLine("", w, color))

	if !input.HasData {
		lines = append(lines, boxLine(
			"  "+paint(color, styleDim, "No session data yet — start an MCP session first."),
			w, color,
		))
		lines = append(lines, boxLine("", w, color))
		lines = append(lines, boxLine(
			"  "+paint(color, styleDim, "RTK")+"  "+rtkStatusPill(input.RtkAvailable, color),
			w, color,
		))
		lines = append(lines, boxBottom(w, color))
		lines = append(lines, "")
		appendRtkGain(&lines, input, color)
		appendBudgets(&lines, input, color, true)
		lines = append(lines, "")
		return strings.Join(lines, "\n")
	}

	// Hero KPIs: tokens · compress% · calls · cache hit
	tokVal := FormatTokens(input.TokensSaved)
	pctVal := jsToFixed(input.ReductionPct, 0) + "%"
	callsVal := FormatCompact(input.TotalCalls, 0)
	hitVal := jsToFixed(input.HitRatePct, 0) + "%"

	col := 12
	v1 := pad(multi(color, []Style{styleBold, styleBrightGreen}, tokVal), col)
	v2 := pad(multi(color, []Style{styleBold, styleAmber}, pctVal), col)
	v3 := pad(multi(color, []Style{styleBold, styleCyan}, callsVal), col)
	v4 := pad(multi(color, []Style{styleBold, styleWhite}, hitVal), col)
	lines = append(lines, boxLine("  "+v1+v2+v3+v4, w, color))

	ul := "────"
	if UseASCII() {
		ul = "----"
	}
	u1 := pad(paint(color, styleGreen, jsSlice(ul, min(4, utf16Len(tokVal)))), col)
	u2 := pad(paint(color, styleAmber, jsSlice(ul, min(4, utf16Len(pctVal)))), col)
	u3 := pad(paint(color, styleCyan, jsSlice(ul, min(4, utf16Len(callsVal)))), col)
	u4 := pad(paint(color, styleGray, jsSlice(ul, min(4, utf16Len(hitVal)))), col)
	lines = append(lines, boxLine("  "+u1+u2+u3+u4, w, color))

	l1 := pad(paint(color, styleDim, "tokens"), col)
	l2 := pad(paint(color, styleDim, "compress"), col)
	l3 := pad(paint(color, styleDim, "calls"), col)
	l4 := pad(paint(color, styleDim, "cache hit"), col)
	lines = append(lines, boxLine("  "+l1+l2+l3+l4, w, color))
	lines = append(lines, boxLine("", w, color))

	// Meter
	meter := ColoredMeter(input.ReductionPct, 22, color)
	meterPct := multi(color, []Style{styleBold, styleAmber}, jsToFixed(input.ReductionPct, 1)+"%")
	lines = append(lines, boxLine("  "+meter+"  "+meterPct, w, color))
	lines = append(lines, boxLine("", w, color))

	// Session meta
	sessions := strconv.FormatInt(input.LiveSessions, 10) + " live · " +
		strconv.FormatInt(input.EndedSessions, 10) + " ended"
	meta := paint(color, styleDim, sessions+" · updated "+ageString(input.LastUpdate))
	lines = append(lines, boxLine("  "+meta, w, color))

	// RTK rewrite status pill
	lines = append(lines, boxLine("  "+paint(color, styleDim, "RTK")+"  "+rtkStatusPill(input.RtkAvailable, color), w, color))

	lines = append(lines, boxBottom(w, color))
	lines = append(lines, "")

	appendByTool(&lines, input, color)
	appendRtkGain(&lines, input, color)
	appendHealth(&lines, input, color)
	appendBudgets(&lines, input, color, input.Verbose)
	if input.Verbose {
		appendVerbose(&lines, input, color)
	}

	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

// rtkStatusPill is the shared "● online"/"○ offline" fragment.
func rtkStatusPill(available bool, color bool) string {
	if available {
		return paint(color, styleGreen, "● online")
	}
	return paint(color, styleRed, "○ offline")
}

// port of src/status-ui.ts:appendByTool
func appendByTool(lines *[]string, input StatusRenderInput, color bool) {
	tools := make([]ToolBreakdown, 0, len(input.ByTool))
	for _, tool := range input.ByTool {
		if tool.Calls > 0 || tool.SavedChars > 0 {
			tools = append(tools, tool)
		}
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return tools[i].SavedChars > tools[j].SavedChars
	})
	if len(tools) > 10 {
		tools = tools[:10]
	}
	if len(tools) == 0 {
		return
	}

	maxSaved := int64(1)
	for _, tool := range tools {
		if tool.SavedChars > maxSaved {
			maxSaved = tool.SavedChars
		}
	}
	*lines = append(*lines, "  "+multi(color, []Style{styleBold, styleAmber}, "by tool"))
	*lines = append(*lines, "  "+rule(52, color))
	*lines = append(*lines, "  "+paint(color, styleDim,
		pad("#", 3)+pad("tool", 10)+pad("calls", 8, true)+pad("saved", 9, true)+pad("avg%", 8, true)+"  impact"))
	*lines = append(*lines, "  "+rule(52, color))

	for i, tool := range tools {
		tok := int64(math.Round(float64(tool.SavedChars) / 4))
		avg := "0"
		if tool.RawChars > 0 {
			avg = jsToFixed(float64(tool.SavedChars)/float64(tool.RawChars)*100, 0)
		}
		impactW := 10
		filled := int64(math.Round(float64(tool.SavedChars) / float64(maxSaved) * float64(impactW)))
		impact := ColoredMeter(float64(filled)/float64(impactW)*100, impactW, color)
		s1 := pad(strconv.FormatInt(int64(i+1), 10), 3)
		s2 := pad(jsSlice(tool.Name, 9), 10)
		s3 := pad(FormatCompact(tool.Calls, 0), 8, true)
		s4Bare := FormatTokens(tok)
		s4 := strings.Repeat(" ", max(0, 9-utf16Len(s4Bare))) + paint(color, styleGreen, s4Bare)
		s5 := pad(avg+"%", 8, true)
		*lines = append(*lines, "  "+paint(color, styleDim, s1)+s2+s3+s4+s5+"  "+impact)
	}
	*lines = append(*lines, "  "+rule(52, color))
	*lines = append(*lines, "")
}

// port of src/status-ui.ts:appendRtkGain
func appendRtkGain(lines *[]string, input StatusRenderInput, color bool) {
	*lines = append(*lines, "  "+multi(color, []Style{styleBold, styleAmber}, "rtk gain")+
		paint(color, styleDim, "  · shell compression (separate layer)"))
	*lines = append(*lines, "  "+rule(52, color))

	if input.RtkGainSkipped {
		*lines = append(*lines, "  "+paint(color, styleDim, "skipped (CAVE_TOOLS_STATUS_RTK=0)"))
		*lines = append(*lines, "")
		return
	}

	if !input.RtkAvailable {
		*lines = append(*lines, "  "+paint(color, styleRed, "○")+" "+
			paint(color, styleDim, "rtk not installed — cave__bash will not rewrite"))
		*lines = append(*lines, "")
		return
	}

	if input.RtkGain == nil {
		*lines = append(*lines, "  "+paint(color, styleYellow, "●")+" "+
			paint(color, styleDim, "online, but gain data unavailable"))
		*lines = append(*lines, "")
		return
	}

	gain := input.RtkGain
	tok := FormatTokens(gain.TotalSaved)
	pct := jsToFixed(gain.AvgSavingsPct, 1) + "%"
	cmds := FormatCompact(gain.TotalCommands, 0)
	*lines = append(*lines,
		"  "+multi(color, []Style{styleBold, styleBrightGreen}, tok)+" "+paint(color, styleDim, "tok saved")+
			"  "+multi(color, []Style{styleBold, styleAmber}, pct)+
			"  "+paint(color, styleCyan, cmds)+" "+paint(color, styleDim, "cmds"))
	meter := ColoredMeter(gain.AvgSavingsPct, 22, color)
	*lines = append(*lines, "  "+meter+"  "+multi(color, []Style{styleBold, styleAmber}, pct))
	if input.Verbose && gain.TotalInput > 0 {
		*lines = append(*lines, "  "+paint(color, styleDim,
			"in "+FormatTokens(gain.TotalInput)+" → out "+FormatTokens(gain.TotalOutput)))
	}
	*lines = append(*lines, "  "+rule(52, color))
	*lines = append(*lines, "")
}

// port of src/status-ui.ts:appendHealth
func appendHealth(lines *[]string, input StatusRenderInput, color bool) {
	*lines = append(*lines, "  "+multi(color, []Style{styleBold, styleAmber}, "health"))
	*lines = append(*lines,
		"  "+paint(color, styleDim, "rewrites")+" "+strconv.FormatInt(input.RtkRewrites, 10)+
			"  "+paint(color, styleDim, "·")+"  "+paint(color, styleDim, "wrapped")+" "+strconv.FormatInt(input.RtkAlreadyWrapped, 10)+
			"  "+paint(color, styleDim, "·")+"  "+paint(color, styleDim, "pass")+" "+strconv.FormatInt(input.RtkPassthrough, 10))
	*lines = append(*lines,
		"  "+paint(color, styleDim, "dedup")+" "+FormatCompact(input.CacheHits, 0)+" hits"+
			"  "+paint(color, styleDim, "·")+"  "+
			paint(color, styleGreen, FormatTokens(int64(math.Round(float64(input.DedupSavedChars)/4))))+" tok avoided")
	if input.FilesTracked > 0 {
		*lines = append(*lines, "  "+paint(color, styleDim, "files tracked")+" "+strconv.FormatInt(input.FilesTracked, 10))
	}
	*lines = append(*lines, "")
}

// port of src/status-ui.ts:appendBudgets
func appendBudgets(lines *[]string, input StatusRenderInput, color bool, full bool) {
	entries := input.Budgets
	if len(entries) == 0 {
		return
	}

	*lines = append(*lines, "  "+multi(color, []Style{styleBold, styleAmber}, "budgets"))
	if full {
		for _, entry := range entries {
			b := entry.Value
			*lines = append(*lines, "  "+pad(entry.Name, 12)+" "+
				paint(color, styleDim, "max")+"="+strconv.FormatInt(b.MaxLines, 10)+"  "+
				paint(color, styleDim, "head")+"="+strconv.FormatInt(b.HeadLines, 10)+"  "+
				paint(color, styleDim, "tail")+"="+strconv.FormatInt(b.TailLines, 10))
		}
	} else {
		parts := make([]string, 0, len(entries))
		for _, entry := range entries {
			b := entry.Value
			parts = append(parts, entry.Name+" "+paint(color, styleDim,
				strconv.FormatInt(b.MaxLines, 10)+"/"+strconv.FormatInt(b.HeadLines, 10)+"/"+strconv.FormatInt(b.TailLines, 10)))
		}
		separator := paint(color, styleDim, "  ·  ")
		for i := 0; i < len(parts); i += 3 {
			end := min(i+3, len(parts))
			*lines = append(*lines, "  "+strings.Join(parts[i:end], separator))
		}
		*lines = append(*lines, "  "+paint(color, styleDim, "hint: cave-tools status --verbose"))
	}
	*lines = append(*lines, "")
}

// port of src/status-ui.ts:appendVerbose
func appendVerbose(lines *[]string, input StatusRenderInput, color bool) {
	*lines = append(*lines, "  "+multi(color, []Style{styleBold, styleAmber}, "detail"))
	*lines = append(*lines, "  "+paint(color, styleDim, "raw chars")+"      "+FormatCompact(input.RawChars))
	*lines = append(*lines, "  "+paint(color, styleDim, "trimmed chars")+"  "+FormatCompact(input.CompressedChars))
	*lines = append(*lines, "  "+paint(color, styleDim, "saved chars")+"    "+FormatCompact(input.CompressionSavedChars))
	*lines = append(*lines, "  "+paint(color, styleDim, "dedup chars")+"    "+FormatCompact(input.DedupSavedChars))
	*lines = append(*lines, "  "+paint(color, styleDim, "cache")+"          "+
		strconv.FormatInt(input.CacheHits, 10)+" hit / "+strconv.FormatInt(input.CacheMisses, 10)+" miss")
	*lines = append(*lines, "")
}

// ── JS number / string helpers ─────────────────────────────────────

// jsToFixed mirrors Number.prototype.toFixed, including its round-half-away
// rule: Go's strconv rounds half to even, which would turn 14.5k into "14k".
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

// utf16Len mirrors JavaScript String.prototype.length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// jsSlice mirrors String.prototype.slice(0, end) over UTF-16 code units.
func jsSlice(s string, end int) string {
	if end <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		if units >= end {
			return s[:i]
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return s
}
