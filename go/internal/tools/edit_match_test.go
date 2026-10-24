package tools

import (
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// Ports scripts/test-edit-v2.mjs (sections B and D) and the helper-level
// assertions of test.mjs sections 6e2.

func TestNormalizeForMatch(t *testing.T) {
	value := "‘’‚‛“”„‟‐‑‒–—―−\u00a0\u2002\u200a\u202f\u205f\u3000"
	want := "''''\"\"\"\"-------      "
	if got := NormalizeForMatch(value); got != want {
		t.Fatalf("NormalizeForMatch = %q, want %q", got, want)
	}
	if got, want := NormalizeForMatch("plain ASCII"), "plain ASCII"; got != want {
		t.Fatalf("plain ASCII = %q", got)
	}
	// One character in, one character out, so offsets stay valid.
	if a, b := utf8.RuneCountInString(value), utf8.RuneCountInString(NormalizeForMatch(value)); a != b {
		t.Fatalf("normalization changed rune count: %d -> %d", a, b)
	}
}

func TestNormalizeForMatchOffsets(t *testing.T) {
	value := "‘a’\u00a0—“b”"
	norm, offsets := normalizeForMatchWithOffsets(value)
	if want := "'a' -\"b\""; norm != want {
		t.Fatalf("norm = %q, want %q", norm, want)
	}
	if len(offsets) != len(norm)+1 {
		t.Fatalf("offsets length = %d, want %d", len(offsets), len(norm)+1)
	}
	// Every normalized rune boundary maps back to the same rune boundary of
	// the source.
	normIndex := 0
	valueIndex := 0
	for normIndex < len(norm) {
		if offsets[normIndex] != valueIndex {
			t.Fatalf("offset[%d] = %d, want %d", normIndex, offsets[normIndex], valueIndex)
		}
		_, normSize := utf8.DecodeRuneInString(norm[normIndex:])
		_, valueSize := utf8.DecodeRuneInString(value[valueIndex:])
		normIndex += normSize
		valueIndex += valueSize
	}
	if offsets[len(norm)] != len(value) {
		t.Fatalf("sentinel offset = %d, want %d", offsets[len(norm)], len(value))
	}
}

func TestFindOccurrences(t *testing.T) {
	// Upstream stride: offset += search.length (overlaps are skipped).
	got := FindOccurrences("aaaa", "aa")
	if len(got) != 2 || got[0] != (EditSpan{0, 2}) || got[1] != (EditSpan{2, 4}) {
		t.Fatalf("FindOccurrences = %+v", got)
	}
	if got := FindOccurrences("abc", ""); len(got) != 0 {
		t.Fatalf("empty search = %+v", got)
	}
	if got := FindOccurrences("abc", "zzz"); len(got) != 0 {
		t.Fatalf("missing search = %+v", got)
	}
}

func TestFindLineOccurrences(t *testing.T) {
	// Tier 3 ignores trailing-whitespace drift and yields the original bytes.
	content := "alpha   \nbeta\t\n"
	spans := FindLineOccurrences(content, "alpha\nbeta")
	if len(spans) != 1 {
		t.Fatalf("spans = %+v", spans)
	}
	if got, want := content[spans[0].Start:spans[0].End], "alpha   \nbeta\t"; got != want {
		t.Fatalf("span text = %q, want %q", got, want)
	}
	// Non-overlap dedup counting: adjacent candidate windows are all kept.
	content = "foo \nfoo\t\n"
	if spans := FindLineOccurrences(content, "foo  "); len(spans) != 2 {
		t.Fatalf("dedup spans = %+v", spans)
	}
	// A trailing newline in the search requires the source window to end on a
	// line break; the trailing empty expected line is dropped.
	if spans := FindLineOccurrences("a\nb", "a\nb\n"); len(spans) != 0 {
		t.Fatalf("no-final-newline spans = %+v", spans)
	}
	spans = FindLineOccurrences("a\nb\n", "a\nb\n")
	if len(spans) != 1 {
		t.Fatalf("final-newline spans = %+v", spans)
	}
	if got, want := "a\nb\n"[spans[0].Start:spans[0].End], "a\nb\n"; got != want {
		t.Fatalf("final-newline span text = %q", got)
	}
}

func TestLineEnding(t *testing.T) {
	if got, want := LineEnding("a\r\nb\r\nc\n", "\n"), "\r\n"; got != want {
		t.Fatalf("crlf-majority = %q, want %q", got, want)
	}
	if got, want := LineEnding("a\nb\nc\r\n", "X"), "\n"; got != want {
		t.Fatalf("lf-majority = %q, want %q", got, want)
	}
	if got, want := LineEnding("a\r\nb\n", "F"), "F"; got != want {
		t.Fatalf("tie-fallback = %q, want %q", got, want)
	}
	if got, want := LineEnding("", "\n"), "\n"; got != want {
		t.Fatalf("empty = %q, want %q", got, want)
	}
}

func TestCountBefore(t *testing.T) {
	positions := []int{1, 4, 7}
	tests := []struct {
		offset int
		want   int
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{4, 1},
		{5, 2},
		{8, 3},
	}
	for _, tt := range tests {
		if got := CountBefore(positions, tt.offset); got != tt.want {
			t.Errorf("CountBefore(%d) = %d, want %d", tt.offset, got, tt.want)
		}
	}
}

func TestAlignLines(t *testing.T) {
	got := AlignLines([]string{"a", "b", "c"}, []string{"a", "B", "c", "d"})
	want := []int{0, 1, 2, -1}
	if len(got) != len(want) {
		t.Fatalf("AlignLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AlignLines = %v, want %v", got, want)
		}
	}
	if got := AlignLines([]string{"a"}, []string{"a"}); len(got) != 1 || got[0] != 0 {
		t.Fatalf("single line = %v", got)
	}
	if got := AlignLines(nil, nil); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}
}

func TestWithRegionEndings(t *testing.T) {
	// B8: kept lines reuse their own ending, changed-in-place reuses the
	// replaced line's ending, extras take the fallback.
	if got, want := WithRegionEndings("a\r\nb\nc", "a\nB\nc\nd", "\r\n"), "a\r\nB\nc\r\nd"; got != want {
		t.Fatalf("WithRegionEndings = %q, want %q", got, want)
	}
	if got, want := WithRegionEndings("a\nb", "", "\n"), ""; got != want {
		t.Fatalf("empty replacement = %q", got)
	}
}

func TestIsEndingsOnlyChange(t *testing.T) {
	if !IsEndingsOnlyChange("a\nb", "a\r\nb") {
		t.Fatal("expected endings-only change")
	}
	if IsEndingsOnlyChange("a\nb", "a\nc") {
		t.Fatal("content change reported as endings-only")
	}
}

func TestComputeEdit(t *testing.T) {
	tests := []struct {
		name             string
		source           string
		oldString        string
		newString        string
		replaceAll       bool
		wantOK           bool
		wantUpdated      string
		wantReplacements int
		wantApplied      int
		wantEndingsOnly  bool
		wantNonUnique    bool
		wantErrSubstr    string
	}{
		// B1.
		{"B1 basic", "hello world\n", "world", "there", false, true, "hello there\n", 1, 1, false, false, ""},
		// B2.
		{"B2 ambiguous", "a a a", "a", "b", false, false, "", 3, 0, false, true, "Found 3 matches"},
		{"B2 replace all", "a a a", "a", "b", true, true, "b b b", 3, 3, false, false, ""},
		// B4: multiple distinct unicode variants, replace_all changes every span.
		{"B4 unicode variants", "\u2018a\u2019 and \u201Aa\u201B\n", "'a'", "'b'", true, true, "'b' and 'b'\n", 2, 2, false, false, ""},
		// B5: trailing-whitespace tier with per-span region-aware replacements.
		{"B5 trailing all", "foo \nfoo\t\n", "foo  ", "bar", true, true, "bar\nbar\n", 2, 2, false, false, ""},
		// B5: exact tier short-circuits.
		{"B5 exact wins", "foo  \nfoo\t\nfoo\n", "foo", "bar", true, true, "bar  \nbar\t\nbar\n", 3, 3, false, false, ""},
		// B6: CRLF -> LF-view matching with source offset mapping.
		{"B6 crlf mapping", "a\r\nb\r\nc\n", "b\nc", "X", false, true, "a\r\nX\n", 1, 1, false, false, ""},
		// B7: single CRLF line -> multiline replacement uses region endings.
		{"B7 region crlf", "a\r\nb\r\n", "b", "x\ny", false, true, "a\r\nx\r\ny\r\n", 1, 1, false, false, ""},
		// B8: mixed-EOL region-aware edit.
		{"B8 mixed region", "a\r\nb\nc\r\n", "a\r\nb\nc", "a\nB\nc\nd", false, true, "a\r\nB\nc\r\nd\r\n", 1, 1, false, false, ""},
		// B9: endings-only changes apply to the exact bytes.
		{"B9 forward", "a\nb\n", "a\nb", "a\r\nb", false, true, "a\r\nb\n", 1, 1, true, false, ""},
		{"B9 reverse", "a\r\nb\r\n", "a\r\nb", "a\nb", false, true, "a\nb\r\n", 1, 1, true, false, ""},
		{"B9 raw miss", "a\r\nb", "a\nb", "a\r\nb", false, false, "", 0, 0, true, false, "Could not find oldString"},
		// B10: computed-unchanged rejects.
		{"B10 unicode unchanged", "\u2018hi\u2019", "'hi'", "\u2018hi\u2019", false, false, "", 1, 0, false, false, "unchanged"},
		{"B10 trailing unchanged", "foo\n", "foo   ", "foo", false, false, "", 1, 0, false, false, "unchanged"},
		// B11: identical / empty guards.
		{"B11 identical", "x", "x", "x", false, false, "", 0, 0, false, false, "identical"},
		{"B11 empty", "x", "", "y", false, false, "", 0, 0, false, false, "must not be empty"},
		// D1: mixed-EOL fixture round-trip.
		{"D1 mixed", "a\r\nb\nc\r\n", "a\r\nb\nc", "a\nB\nc\nd", false, true, "a\r\nB\nc\r\nd\r\n", 1, 1, false, false, ""},
		// Unicode tier before the match: normalized offsets are byte offsets in
		// the source, not in the normalized view.
		{"unicode tier before match", "\u2018x\u2019 hello", "'x' hello", "Z", false, true, "Z", 1, 1, false, false, ""},
		{"unicode tier after match", "a \u2018b\u2019 c", "a 'b' c", "X", false, true, "X", 1, 1, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeEdit(tt.source, tt.oldString, tt.newString, tt.replaceAll)
			if got.OK != tt.wantOK {
				t.Fatalf("ok = %v, want %v (%+v)", got.OK, tt.wantOK, got)
			}
			if !tt.wantOK {
				if !strings.Contains(got.Error, tt.wantErrSubstr) {
					t.Fatalf("error = %q, want substring %q", got.Error, tt.wantErrSubstr)
				}
				if got.NonUnique != tt.wantNonUnique {
					t.Fatalf("nonUnique = %v, want %v", got.NonUnique, tt.wantNonUnique)
				}
				if got.Replacements != tt.wantReplacements {
					t.Fatalf("replacements = %d, want %d", got.Replacements, tt.wantReplacements)
				}
				if got.EndingsOnly != tt.wantEndingsOnly {
					t.Fatalf("endingsOnly = %v, want %v", got.EndingsOnly, tt.wantEndingsOnly)
				}
				return
			}
			if got.Updated != tt.wantUpdated {
				t.Fatalf("updated = %q, want %q", got.Updated, tt.wantUpdated)
			}
			if got.Replacements != tt.wantReplacements {
				t.Fatalf("replacements = %d, want %d", got.Replacements, tt.wantReplacements)
			}
			if len(got.Applied) != tt.wantApplied {
				t.Fatalf("applied = %d, want %d", len(got.Applied), tt.wantApplied)
			}
			if got.EndingsOnly != tt.wantEndingsOnly {
				t.Fatalf("endingsOnly = %v, want %v", got.EndingsOnly, tt.wantEndingsOnly)
			}
		})
	}
}

func TestComputeEditMatchSpans(t *testing.T) {
	// B2 ambiguous carries all matches.
	got := ComputeEdit("a a a", "a", "b", false)
	if len(got.Matches) != 3 {
		t.Fatalf("matches = %+v", got.Matches)
	}
	// B3 exact match slice.
	src := "const s = \u201Chello\u201D;"
	r := ComputeEdit(src, "hello", "hi", false)
	if !r.OK || len(r.Matches) != 1 {
		t.Fatalf("B3 = %+v", r)
	}
	if slice := src[r.Matches[0].Start:r.Matches[0].End]; slice != "hello" {
		t.Fatalf("B3 slice = %q", slice)
	}
	if r.Updated != "const s = \u201Chi\u201D;" {
		t.Fatalf("B3 updated = %q", r.Updated)
	}
	// B4 distinct raw variants and per-span replacement text.
	src = "\u2018a\u2019 and \u201Aa\u201B\n"
	r = ComputeEdit(src, "'a'", "'b'", true)
	if !r.OK || r.Replacements != 2 || len(r.Applied) != 2 {
		t.Fatalf("B4 = %+v", r)
	}
	slices := make([]string, len(r.Matches))
	for i, m := range r.Matches {
		slices[i] = src[m.Start:m.End]
	}
	raws := append([]string{}, slices...)
	sort.Strings(raws)
	wantRaws := []string{"\u2018a\u2019", "\u201Aa\u201B"}
	sort.Strings(wantRaws)
	if !equalStrings(raws, wantRaws) {
		t.Fatalf("B4 distinct raws = %q, want %q", raws, wantRaws)
	}
	for _, a := range r.Applied {
		if a.Replacement != "'b'" {
			t.Fatalf("B4 per-span text = %q", a.Replacement)
		}
	}
	// B5 per-span text.
	r = ComputeEdit("foo \nfoo\t\n", "foo  ", "bar", true)
	if !r.OK {
		t.Fatalf("B5 = %+v", r)
	}
	for _, a := range r.Applied {
		if a.Replacement != "bar" {
			t.Fatalf("B5 per-span text = %q", a.Replacement)
		}
	}
	// B6 region slice and updated bytes.
	src = "a\r\nb\r\nc\n"
	r = ComputeEdit(src, "b\nc", "X", false)
	if !r.OK || len(r.Matches) != 1 {
		t.Fatalf("B6 = %+v", r)
	}
	if slice := src[r.Matches[0].Start:r.Matches[0].End]; slice != "b\r\nc" {
		t.Fatalf("B6 region = %q", slice)
	}
	if r.Updated != "a\r\nX\n" {
		t.Fatalf("B6 updated = %q", r.Updated)
	}
	// B7 no lone LF in the result.
	r = ComputeEdit("a\r\nb\r\n", "b", "x\ny", false)
	if !r.OK || r.Applied[0].Replacement != "x\r\ny" {
		t.Fatalf("B7 = %+v", r)
	}
	if strings.Contains(strings.ReplaceAll(r.Updated, "\r\n", ""), "\n") {
		t.Fatalf("B7 lone LF in %q", r.Updated)
	}
}

func TestMatchViewMiss(t *testing.T) {
	// C2 view-level sanity: a pure miss stays a miss.
	if got := MatchView("    foo\n    bar\n", "foo\nbar", "x\ny").Matches; len(got) != 0 {
		t.Fatalf("matches = %+v", got)
	}
}
