package cli

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func statusSample() StatusRenderInput {
	return StatusRenderInput{
		RtkAvailable:          true,
		ReductionPct:          48.2,
		HitRatePct:            82.1,
		CacheHits:             410,
		CacheMisses:           90,
		FilesTracked:          12,
		RtkRewrites:           12,
		RtkAlreadyWrapped:     3,
		RtkPassthrough:        40,
		TotalCalls:            128,
		RawChars:              4_800_000,
		CompressedChars:       2_400_000,
		CompressionSavedChars: 2_400_000,
		DedupSavedChars:       900_000,
		SavedChars:            3_300_000,
		TokensSaved:           825_000,
		LiveSessions:          2,
		EndedSessions:         5,
		LastUpdate:            0,
		HasData:               true,
		Budgets: []BudgetEntry{
			{Name: "bash", Value: Budget{MaxLines: 200, HeadLines: 80, TailLines: 40}},
			{Name: "read", Value: Budget{MaxLines: 200, HeadLines: 80, TailLines: 40}},
		},
		ByTool: []ToolBreakdown{
			{Name: "bash", Calls: 40, RawChars: 1_000_000, SavedChars: 620_000},
			{Name: "read", Calls: 55, RawChars: 800_000, SavedChars: 300_000},
			{Name: "longtoolname", Calls: 3, RawChars: 100, SavedChars: 50},
		},
		RtkGain: &RtkGainSummary{
			TotalCommands: 10362,
			TotalInput:    99_500_000,
			TotalOutput:   12_000_000,
			TotalSaved:    87_500_000,
			AvgSavingsPct: 87.9,
			TotalTimeMs:   30_000_000,
			AvgTimeMs:     2900,
		},
		RtkGainSkipped: false,
		Verbose:        false,
	}
}

func emptyStatusSample() StatusRenderInput {
	return StatusRenderInput{
		RtkAvailable: false,
		LastUpdate:   0,
		HasData:      false,
		Budgets: []BudgetEntry{
			{Name: "bash", Value: Budget{MaxLines: 200, HeadLines: 80, TailLines: 40}},
		},
		RtkGainSkipped: false,
	}
}

// deterministic forces the plain ASCII renderer the way the TS test forces
// substance checks: no color, no unicode.
func deterministic(t *testing.T) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CAVE_TOOLS_ASCII", "1")
	t.Setenv("FORCE_COLOR", "")
}

// Ported from test.mjs "12. Testing status-ui renderer".
func TestFormatCompact(t *testing.T) {
	tests := []struct {
		n      int64
		digits []int64
		want   string
	}{
		{n: 87525037, want: "87.5M"},
		{n: 1200, want: "1.2k"},
		{n: 42, want: "42"},
		{n: 15000, want: "15k"},
		{n: 1_000_000, want: "1M"},
		{n: 1_500_000, want: "1.5M"},
		{n: 1_000_000_000, want: "1B"},
		{n: 999, want: "999"},
		{n: 1000, want: "1k"},
		// (abs/1000).toFixed(0) is half-up in JS; Go's strconv would say 14k.
		{n: 14500, want: "15k"},
		{n: 14999, want: "15k"},
		{n: -1200, want: "-1.2k"},
		{n: 0, want: "0"},
		{n: 87525037, digits: []int64{0}, want: "88M"},
		{n: 87525037, digits: []int64{2}, want: "87.53M"},
	}
	for _, test := range tests {
		if got := FormatCompact(test.n, test.digits...); got != test.want {
			t.Errorf("FormatCompact(%d, %v) = %q, want %q", test.n, test.digits, got, test.want)
		}
	}
	if got := FormatTokens(825_000); got != "825k" {
		t.Errorf("FormatTokens(825000) = %q, want 825k", got)
	}
}

func TestJSToFixedMatchesJavaScript(t *testing.T) {
	tests := []struct {
		x      float64
		digits int
		want   string
	}{
		{14.5, 0, "15"},
		{2.5, 0, "3"},
		{0.5, 0, "1"},
		{1.25, 1, "1.3"},
		{1.45, 1, "1.4"},   // 1.45 is below the decimal tie as a double
		{1.005, 2, "1.00"}, // same reason
		{-0.4, 0, "-0"},
		{0, 1, "0.0"},
		{48.2, 0, "48"},
		{87.9, 1, "87.9"},
	}
	for _, test := range tests {
		if got := jsToFixed(test.x, test.digits); got != test.want {
			t.Errorf("jsToFixed(%v, %d) = %q, want %q", test.x, test.digits, got, test.want)
		}
	}
}

func TestColoredMeter(t *testing.T) {
	if got := ColoredMeter(50, 10, false); utf16Len(got) != 10 {
		t.Fatalf("meter length = %d, want 10 (%q)", utf16Len(got), got)
	}
	deterministic(t)
	if got := ColoredMeter(50, 10, false); got != "#####-----" {
		t.Fatalf("ColoredMeter(50,10,false) = %q", got)
	}
	if got := ColoredMeter(0, 10, false); got != "----------" {
		t.Fatalf("ColoredMeter(0,10,false) = %q", got)
	}
	if got := ColoredMeter(100, 10, false); got != "##########" {
		t.Fatalf("ColoredMeter(100,10,false) = %q", got)
	}
	t.Setenv("CAVE_TOOLS_ASCII", "")
	if got := ColoredMeter(50, 10, false); got != "█████░░░░░" {
		t.Fatalf("unicode meter = %q", got)
	}
	colored := ColoredMeter(50, 10, true)
	if !strings.HasPrefix(colored, string(styleAmber)) {
		t.Fatalf("colored meter does not start with amber: %q", colored)
	}
	if !strings.HasSuffix(colored, string(styleReset)) {
		t.Fatalf("colored meter does not end with reset: %q", colored)
	}
}

func TestSupportsColorAndASCII(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAVE_TOOLS_ASCII", "")
	t.Setenv("FORCE_COLOR", "")
	if SupportsColor() {
		t.Fatal("supports color with no TTY and no FORCE_COLOR")
	}
	if UseASCII() {
		t.Fatal("useAscii with CAVE_TOOLS_ASCII unset")
	}

	t.Setenv("FORCE_COLOR", "1")
	if !SupportsColor() {
		t.Fatal("FORCE_COLOR=1 must enable color")
	}
	t.Setenv("NO_COLOR", "1")
	if SupportsColor() {
		t.Fatal("NO_COLOR wins over FORCE_COLOR")
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("CAVE_TOOLS_ASCII", "1")
	if SupportsColor() {
		t.Fatal("CAVE_TOOLS_ASCII=1 disables color")
	}
	if !UseASCII() {
		t.Fatal("CAVE_TOOLS_ASCII=1 must enable ASCII")
	}
}

func TestEnvTruthy(t *testing.T) {
	for _, value := range []string{"", "0", "false", "FALSE", "False"} {
		t.Setenv("CAVE_TEST_FLAG", value)
		if envTruthy("CAVE_TEST_FLAG") {
			t.Errorf("envTruthy(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"1", "true", "yes", "anything"} {
		t.Setenv("CAVE_TEST_FLAG", value)
		if !envTruthy("CAVE_TEST_FLAG") {
			t.Errorf("envTruthy(%q) = false, want true", value)
		}
	}
	if err := os.Unsetenv("CAVE_TEST_FLAG"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	if envTruthy("CAVE_TEST_FLAG") {
		t.Error("envTruthy(unset) = true, want false")
	}
}

func TestPadAndVisibleLen(t *testing.T) {
	if got := pad("abc", 5); got != "abc  " {
		t.Fatalf("pad right = %q", got)
	}
	if got := pad("abc", 5, true); got != "  abc" {
		t.Fatalf("pad left = %q", got)
	}
	if got := pad("abcdef", 3); got != "abcdef" {
		t.Fatalf("pad overflow = %q", got)
	}
	colored := string(styleRed) + "abc" + string(styleReset)
	if got := visibleLen(colored); got != 3 {
		t.Fatalf("visibleLen = %d, want 3", got)
	}
	if got := pad(colored, 5); got != colored+"  " {
		t.Fatalf("pad colored = %q", got)
	}
}

func TestAgeString(t *testing.T) {
	now := int64(1_700_000_000_000)
	tests := []struct {
		last int64
		want string
	}{
		{0, "—"},
		{-5, "—"},
		{now - 1_000, "1s ago"},
		{now - 30_000, "30s ago"},
		{now - 90_000, "2m ago"},
		{now - 7_200_000, "2h ago"},
	}
	for _, test := range tests {
		if got := ageStringAt(test.last, now); got != test.want {
			t.Errorf("ageStringAt(%d) = %q, want %q", test.last-now, got, test.want)
		}
	}
}

// port of test.mjs plain-mode assertions.
func TestRenderStatusCLIPlain(t *testing.T) {
	deterministic(t)
	out := RenderStatusCLI(statusSample())
	for _, pattern := range []string{
		"cave-tools", "by tool", "rtk gain", "bash", "health", "budgets", "825k", "87.9%",
	} {
		if !strings.Contains(out, pattern) {
			t.Errorf("plain status missing %q", pattern)
		}
	}
	if regexp.MustCompile(`(?i)tokens|825k|825\.0k|825000`).FindStringIndex(out) == nil {
		t.Error("plain status has no token line")
	}
}

// port of test.mjs empty-state assertions.
func TestRenderStatusCLIEmpty(t *testing.T) {
	deterministic(t)
	out := RenderStatusCLI(emptyStatusSample())
	if !strings.Contains(out, "No session data yet") {
		t.Error("empty status missing the no-data notice")
	}
	if !strings.Contains(out, "offline") || !strings.Contains(out, "RTK") {
		t.Error("empty status missing the RTK pill")
	}
}

// port of test.mjs verbose assertions.
func TestRenderStatusCLIVerbose(t *testing.T) {
	deterministic(t)
	input := statusSample()
	input.RtkAvailable = true
	input.ReductionPct = 10
	input.HitRatePct = 50
	input.CacheHits = 1
	input.CacheMisses = 1
	input.FilesTracked = 1
	input.RtkRewrites = 0
	input.RtkAlreadyWrapped = 0
	input.RtkPassthrough = 0
	input.TotalCalls = 1
	input.RawChars = 1000
	input.CompressedChars = 900
	input.CompressionSavedChars = 100
	input.DedupSavedChars = 0
	input.SavedChars = 100
	input.TokensSaved = 25
	input.LiveSessions = 1
	input.EndedSessions = 0
	input.Budgets = []BudgetEntry{{Name: "bash", Value: Budget{MaxLines: 10, HeadLines: 4, TailLines: 4}}}
	input.ByTool = nil
	input.RtkGain = nil
	input.RtkGainSkipped = true
	input.Verbose = true

	out := RenderStatusCLI(input)
	if !regexp.MustCompile(`(?i)detail|raw chars`).MatchString(out) {
		t.Error("verbose status missing the detail section")
	}
	if !regexp.MustCompile(`CAVE_TOOLS_STATUS_RTK=0|skipped`).MatchString(out) {
		t.Error("verbose status missing the rtk-skipped notice")
	}
}

func TestRenderStatusCLIColored(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAVE_TOOLS_ASCII", "")
	t.Setenv("FORCE_COLOR", "1")
	out := RenderStatusCLI(statusSample())
	if !strings.Contains(out, string(styleAmber)) {
		t.Fatal("colored status has no ANSI amber")
	}
	if !strings.Contains(out, "╭") {
		t.Fatal("colored status has no unicode box")
	}
}

// ── rtk gain ───────────────────────────────────────────────────────

func writeFakeRtk(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "rtk")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake rtk: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAVE_TOOLS_STATUS_RTK", "")
}

func TestFetchRtkGainSummarySkipped(t *testing.T) {
	t.Setenv("CAVE_TOOLS_STATUS_RTK", "0")
	if got := FetchRtkGainSummary(); got != nil {
		t.Fatalf("FetchRtkGainSummary() = %+v, want nil", got)
	}
}

func TestFetchRtkGainSummaryParses(t *testing.T) {
	if processPlatformForTest() == "windows" {
		t.Skip("shell script fake needs a POSIX shell")
	}
	writeFakeRtk(t, `echo '{"summary":{"total_commands":10,"total_input":100,"total_output":50,"total_saved":50,"avg_savings_pct":33.3,"total_time_ms":900,"avg_time_ms":90}}'`)
	got := FetchRtkGainSummary()
	if got == nil {
		t.Fatal("FetchRtkGainSummary() = nil, want a summary")
	}
	want := RtkGainSummary{TotalCommands: 10, TotalInput: 100, TotalOutput: 50, TotalSaved: 50, AvgSavingsPct: 33.3, TotalTimeMs: 900, AvgTimeMs: 90}
	if *got != want {
		t.Fatalf("summary = %+v, want %+v", *got, want)
	}
}

func TestFetchRtkGainSummaryDefaultsMissingFields(t *testing.T) {
	if processPlatformForTest() == "windows" {
		t.Skip("shell script fake needs a POSIX shell")
	}
	writeFakeRtk(t, `echo '{"summary":{"total_commands":7}}'`)
	got := FetchRtkGainSummary()
	if got == nil {
		t.Fatal("FetchRtkGainSummary() = nil, want a summary")
	}
	if got.TotalCommands != 7 || got.TotalInput != 0 || got.AvgSavingsPct != 0 {
		t.Fatalf("summary = %+v, want defaults for missing fields", *got)
	}
}

func TestFetchRtkGainSummaryFailureModes(t *testing.T) {
	if processPlatformForTest() == "windows" {
		t.Skip("shell script fake needs a POSIX shell")
	}
	cases := map[string]string{
		"no summary": `echo '{"other":1}'`,
		"bad json":   `echo 'not json'`,
		"exit 1":     `exit 1`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			writeFakeRtk(t, body)
			if got := FetchRtkGainSummary(); got != nil {
				t.Fatalf("FetchRtkGainSummary() = %+v, want nil", got)
			}
		})
	}
}

// processPlatformForTest is the platform flag without importing the daemon
// package (the CLI is an entry layer and must stay importable on its own).
func processPlatformForTest() string {
	if os.PathSeparator == '\\' {
		return "windows"
	}
	return "unix"
}

// verboseGolden is the byte-exact output of dist/status-ui.js for
// statusSample() with verbose=true, NO_COLOR=1 and CAVE_TOOLS_ASCII=1.
var verboseGolden = `
  +-------------------------------------------------------+
  |  *  cave-tools · status                               |
  |                                                       |
  |  825k        48%         128         82%              |
  |  ----        ---         ---         ---              |
  |  tokens      compress    calls       cache hit        |
  |                                                       |
  |  ###########-----------  48.2%                        |
  |                                                       |
  |  2 live · 5 ended · updated —                         |
  |  RTK  ● online                                        |
  +-------------------------------------------------------+

  by tool
  ----------------------------------------------------
  #  tool         calls    saved    avg%  impact
  ----------------------------------------------------
  1  bash            40     155k     62%  ##########
  2  read            55      75k     38%  #####-----
  3  longtooln        3       13     50%  ----------
  ----------------------------------------------------

  rtk gain  · shell compression (separate layer)
  ----------------------------------------------------
  87.5M tok saved  87.9%  10k cmds
  ###################---  87.9%
  in 99.5M → out 12M
  ----------------------------------------------------

  health
  rewrites 12  ·  wrapped 3  ·  pass 40
  dedup 410 hits  ·  225k tok avoided
  files tracked 12

  budgets
  bash         max=200  head=80  tail=40
  read         max=200  head=80  tail=40

  detail
  raw chars      4.8M
  trimmed chars  2.4M
  saved chars    2.4M
  dedup chars    900k
  cache          410 hit / 90 miss

`

func TestRenderStatusCLIVerboseGolden(t *testing.T) {
	deterministic(t)
	input := statusSample()
	input.Verbose = true
	if got := RenderStatusCLI(input); got != verboseGolden {
		t.Fatalf("verbose status differs from TS golden:\n--- got ---\n%s\n--- want ---\n%s", got, verboseGolden)
	}
}

// Expected values from dist/status-ui.js coloredMeter(NaN, 10, false|true).
func TestColoredMeterNaNMatchesTS(t *testing.T) {
	if got := ColoredMeter(math.NaN(), 10, false); got != "" {
		t.Fatalf("ColoredMeter(NaN, plain) = %q, want empty", got)
	}
	want := "\x1b[38;5;240m\x1b[0m\x1b[38;5;240m\x1b[0m"
	if got := ColoredMeter(math.NaN(), 10, true); got != want {
		t.Fatalf("ColoredMeter(NaN, color) = %q, want %q", got, want)
	}
}
