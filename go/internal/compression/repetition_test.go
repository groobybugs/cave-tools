package compression

import (
	"strconv"
	"strings"
	"testing"
)

// TestCollapseRepetition ports the repetition assertions from test.mjs (the
// read fast-path block).
func TestCollapseRepetition(t *testing.T) {
	line := "the same log line repeats a lot and must shrink"
	raw := strings.Join(repeatString(line, 10), "\n")

	once := CollapseRepetition(raw)
	twice := CollapseRepetition(once)
	if once != twice {
		t.Errorf("repetition collapse is not idempotent: %q != %q", once, twice)
	}
	if want := line + "\n… 9 identical lines elided …"; once != want {
		t.Errorf("CollapseRepetition() = %q, want %q", once, want)
	}
	if got := strings.Count(once, "identical lines elided"); got != 1 {
		t.Errorf("once has %d markers, want 1", got)
	}
	if got := strings.Count(twice, "identical lines elided"); got != 1 {
		t.Errorf("twice has %d markers, want 1", got)
	}

	nested := strings.Join([]string{
		RepetitionMarker(2),
		RepetitionMarker(2),
		RepetitionMarker(2),
	}, "\n")
	if got := CollapseRepetition(nested); got != nested {
		t.Errorf("marker lines re-collapsed: %q, want %q", got, nested)
	}

	ids := make([]string, 10)
	for i := range ids {
		ids[i] = "GET /x req=" + strconv.Itoa(i) + " ok payload=hello"
	}
	idText := strings.Join(ids, "\n")
	if got := CollapseRepetition(idText); got != idText {
		t.Errorf("digit-mask off by default collapsed ids: %q", got)
	}

	plain := CollapseRepetitionOptions{}
	masked := CollapseRepetition(idText, plain)
	if masked != idText {
		t.Errorf("zero options changed behaviour: %q", masked)
	}

	masked = CollapseRepetition(idText, CollapseRepetitionOptions{MaskDigits: true})
	if !strings.Contains(masked, "identical lines elided") {
		t.Errorf("digit-mask opt-in did not collapse id runs: %q", masked)
	}
	if want := "GET /x req=0 ok payload=hello\n… 9 identical lines elided …"; masked != want {
		t.Errorf("masked = %q, want %q", masked, want)
	}
	if again := CollapseRepetition(masked, CollapseRepetitionOptions{MaskDigits: true}); again != masked {
		t.Errorf("masked collapse is not idempotent: %q != %q", again, masked)
	}

	// minRun is an explicit option; a run that would not shrink the text is
	// left alone, matching the TS length guard.
	minRun := 2
	if got := CollapseRepetition("a\na\nb", CollapseRepetitionOptions{MinRun: &minRun}); got != "a\na\nb" {
		t.Errorf("minRun=2 = %q, want %q", got, "a\na\nb")
	}
	if got := RepetitionMarker(9); got != "… 9 identical lines elided …" {
		t.Errorf("RepetitionMarker(9) = %q", got)
	}
}

// repeatString builds n copies for fixtures.
func repeatString(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestCollapseRepetitionJSWhitespace is the Muse regression: the guard is
// current.trim().length > 0 with JS trim, so a U+FEFF-only run is blank (not
// collapsed) while a U+0085-only run is not blank (collapsed). Go's
// strings.TrimSpace gets both backwards. Expected results come from node.
func TestCollapseRepetitionJSWhitespace(t *testing.T) {
	feffLines := append(repeatString("\ufeff", 20), "tail")
	feffText := strings.Join(feffLines, "\n")
	if got := CollapseRepetition(feffText); got != feffText {
		t.Errorf("FEFF-only run collapsed: %q, want unchanged %q", got, feffText)
	}

	spaceLines := append(repeatString(" ", 20), "tail")
	spaceText := strings.Join(spaceLines, "\n")
	if got := CollapseRepetition(spaceText); got != spaceText {
		t.Errorf("space-only run collapsed: %q, want unchanged %q", got, spaceText)
	}

	nelLines := append(repeatString("\u0085", 20), "tail")
	nelText := strings.Join(nelLines, "\n")
	want := "\u0085\n… 19 identical lines elided …\ntail"
	if got := CollapseRepetition(nelText); got != want {
		t.Errorf("NEL-only run = %q, want %q", got, want)
	}
}
