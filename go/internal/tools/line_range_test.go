package tools

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
)

// Ports scripts/test-line-range-reliability.mjs and the line-range assertions
// in test.mjs (section 6f).

// toolsTestCtx builds a call context with an explicit env map, so tests never
// read the real process environment.
func toolsTestCtx(env map[string]string) context.Context {
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{Env: env}, func(ctx context.Context) context.Context {
		return ctx
	})
}

func TestDetectEol(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    EOL
	}{
		{"empty", "", EOLLF},
		{"lf only", "a\nb\n", EOLLF},
		{"lone cr is not crlf", "a\rb", EOLLF},
		{"crlf", "a\r\nb", EOLCRLF},
		{"mixed with one crlf", "a\nb\r\nc\n", EOLCRLF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectEol(tt.content); got != tt.want {
				t.Fatalf("DetectEol(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestToLfLinesAndFromLfLines(t *testing.T) {
	if got, want := ToLfLines("a\r\nb\rc\n"), []string{"a", "b", "c", ""}; !equalStrings(got, want) {
		t.Fatalf("ToLfLines = %q, want %q", got, want)
	}
	if got, want := ToLfLines(""), []string{""}; !equalStrings(got, want) {
		t.Fatalf("ToLfLines(empty) = %q, want %q", got, want)
	}
	if got, want := FromLfLines([]string{"a", "b"}, EOLCRLF), "a\r\nb"; got != want {
		t.Fatalf("FromLfLines CRLF = %q, want %q", got, want)
	}
	if got, want := FromLfLines([]string{"a", "b"}, EOLLF), "a\nb"; got != want {
		t.Fatalf("FromLfLines LF = %q, want %q", got, want)
	}
}

func TestParseLineSegs(t *testing.T) {
	// The segment count must equal the ToLfLines model for every EOL shape,
	// and rendering the segments must reproduce the bytes exactly.
	fixtures := []string{
		"",
		"a",
		"a\n",
		"a\nb",
		"a\nb\n",
		"\n",
		"\n\n",
		"a\r\nb",
		"a\rb",
		"a\r",
		"a\r\n",
		"a\n\nb",
		"1\r\n2\n3\r\n4",
		"1\r\n2\n3\r\n4\n",
	}
	for _, fixture := range fixtures {
		segs := parseLineSegs(fixture)
		if got, want := len(segs), len(ToLfLines(fixture)); got != want {
			t.Errorf("parseLineSegs(%q) line count = %d, want %d", fixture, got, want)
		}
		if got, want := renderSegs(segs), normalizeLoneCR(fixture); got != want {
			t.Errorf("renderSegs(parseLineSegs(%q)) = %q, want %q", fixture, got, want)
		}
	}
}

// normalizeLoneCR mirrors parseLineSegs: a \r that is not part of \r\n
// becomes \n, while CRLF stays CRLF.
func normalizeLoneCR(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && (i+1 >= len(s) || s[i+1] != '\n') {
			out.WriteByte('\n')
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func TestLineTag(t *testing.T) {
	// test.mjs asserts the tag is two hex characters; pin the digest too.
	sum := sha1.Sum([]byte("alpha"))
	want := hex.EncodeToString(sum[:])[:2]
	if got := LineTag("alpha"); got != want {
		t.Fatalf("LineTag(alpha) = %q, want %q", got, want)
	}
	if got := LineTag("alpha"); len(got) != 2 || !regexp.MustCompile(`^[0-9a-f]{2}$`).MatchString(got) {
		t.Fatalf("LineTag shape = %q", got)
	}
}

func TestFormatNumberedLineAndWindow(t *testing.T) {
	tag := LineTag("alpha")
	if got, want := FormatNumberedLine(1, "alpha"), "00001:"+tag+"| alpha"; got != want {
		t.Fatalf("FormatNumberedLine = %q, want %q", got, want)
	}
	if got, want := FormatNumberedLine(1, "alpha", false), "00001| alpha"; got != want {
		t.Fatalf("FormatNumberedLine no tags = %q, want %q", got, want)
	}
	// padStart keeps numbers wider than the pad.
	if got, want := FormatNumberedLine(123456, "x", false), "123456| x"; got != want {
		t.Fatalf("wide line number = %q, want %q", got, want)
	}
	lines := []string{"a", "b", "c"}
	window := FormatNumberedWindow(lines, 2, 3, false)
	if want := "00002| b\n00003| c"; window != want {
		t.Fatalf("FormatNumberedWindow = %q, want %q", window, want)
	}
	// Bounds are clamped: start below 1 and end past the slice.
	clamped := FormatNumberedWindow(lines, 0, 9, false)
	if want := "00001| a\n00002| b\n00003| c"; clamped != want {
		t.Fatalf("clamped window = %q, want %q", clamped, want)
	}
	if got := FormatNumberedWindow(nil, 1, 3, false); got != "" {
		t.Fatalf("empty window = %q", got)
	}
}

func TestRangeChecksum(t *testing.T) {
	// B7 checksum shape.
	cs := RangeChecksum([]string{"a", "b", "c"}, 1, 2)
	if len(cs) != 8 || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(cs) {
		t.Fatalf("RangeChecksum shape = %q", cs)
	}
	// F2 checksum stable.
	if cs2 := RangeChecksum([]string{"a", "b", "c", ""}, 2, 2); cs2 != RangeChecksum([]string{"a", "b", "c", ""}, 2, 2) {
		t.Fatalf("RangeChecksum is not stable")
	}
	// F3 insert checksum differs from the single-line checksum.
	c1 := RangeChecksum([]string{"a", "b", "c", ""}, 2, 2)
	c3 := RangeChecksum([]string{"a", "b", "c", ""}, 2, 1)
	if c3 == c1 || len(c3) != 8 {
		t.Fatalf("insert checksum = %q, single-line = %q", c3, c1)
	}
	// B7 checksum round-trip.
	if got := VerifyRangeChecksum("a\nb\nc", 1, 2, c1For("a\nb\nc", 1, 2)); !got.OK {
		t.Fatalf("VerifyRangeChecksum round-trip failed: %+v", got)
	}
}

func c1For(content string, start, end int) string {
	return RangeChecksum(ToLfLines(content), start, end)
}

func TestVerifyRangeChecksum(t *testing.T) {
	actual := RangeChecksum(ToLfLines("a\nb\nc"), 1, 2)
	if got := VerifyRangeChecksum("a\nb\nc", 1, 2, actual); !got.OK {
		t.Fatalf("exact match: %+v", got)
	}
	// The footer may shorten later: expected is accepted as a prefix.
	if got := VerifyRangeChecksum("a\nb\nc", 1, 2, actual[:4]); !got.OK {
		t.Fatalf("prefix match: %+v", got)
	}
	// Trimmed and case-insensitive.
	if got := VerifyRangeChecksum("a\nb\nc", 1, 2, "  "+strings.ToUpper(actual)+" "); !got.OK {
		t.Fatalf("trim/lower match: %+v", got)
	}
	if got := VerifyRangeChecksum("a\nb\nc", 1, 2, "abc"); got.OK || !strings.Contains(got.Error, "at least 4 hex characters") {
		t.Fatalf("short expected: %+v", got)
	}
	// B7 checksum mismatch shape.
	bad := VerifyRangeChecksum("a\nb\nc", 1, 2, "ffffffff")
	if bad.OK || bad.Actual == "" || bad.Window == "" || bad.Error == "" {
		t.Fatalf("mismatch shape = %+v", bad)
	}
	if !strings.Contains(bad.Error, "expected_range_checksum mismatch for lines 1-2") ||
		!strings.Contains(bad.Error, "cave__read offset=1 limit=2 line_numbers=true") {
		t.Fatalf("mismatch error = %q", bad.Error)
	}
	// Insert mismatch window: offset/lines shown around the insert point.
	ins := VerifyRangeChecksum("a\nb\nc", 2, 1, "ffffffff")
	if ins.OK || !strings.Contains(ins.Error, "offset=2 limit=5") {
		t.Fatalf("insert mismatch = %+v", ins)
	}
	if !strings.Contains(ins.Window, "00001:") || !strings.Contains(ins.Window, "00003:") {
		t.Fatalf("insert window = %q", ins.Window)
	}
}

func TestApplyRangeEdit(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		startLine     int
		endLine       int
		replacement   string
		wantOK        bool
		wantContent   string
		wantReplaced  int
		wantErrSubstr string
	}{
		// B. existing test.mjs semantics.
		{"B1 replace line", "a\nb\nc\n", 2, 2, "B", true, "a\nB\nc\n", 1, ""},
		{"B2 insert before", "a\nb\n", 2, 1, "x", true, "a\nx\nb\n", 0, ""},
		{"B3 delete lines", "a\nb\nc\n", 2, 3, "", true, "a\n", 3, ""},
		{"B4 out of range", "a\nb\n", 5, 5, "z", false, "", 0, "start_line 5 out of range"},
		{"B5 crlf replace", "a\r\nb\r\nc\r\n", 2, 2, "B", true, "a\r\nB\r\nc\r\n", 1, ""},

		// C. trailing-newline append/move defects.
		{"C1 model+1 append", "a\nb\n", 4, 3, "c", true, "a\nb\nc\n", 0, ""},
		{"C2 sentinel append", "a\nb\n", 3, 2, "c", true, "a\nb\nc\n", 0, ""},
		{"C3 no-trailing append", "a\nb", 3, 2, "c", true, "a\nb\nc", 0, ""},
		{"C4 empty model+1", "", 2, 1, "c", true, "c\n", 0, ""},
		{"C5 empty insert", "", 1, 0, "c", true, "c\n", 0, ""},
		{"C6 lone-NL model+1", "\n", 3, 2, "c", true, "\nc\n", 0, ""},
		{"C13 crlf insert", "a\r\nb\r\n", 2, 1, "x", true, "a\r\nx\r\nb\r\n", 0, ""},
		{"C16 count parity in error", "a\nb\n", 5, 5, "z", false, "", 0, "file has 3 lines"},
		{"C17 sentinel replace out of range", "a\nb\n", 4, 4, "c", false, "", 0, "out of range"},

		// D. mixed-EOL preservation.
		{"D1 mixed replace", "1\r\n2\n3\r\n4\n", 2, 2, "TWO", true, "1\r\nTWO\n3\r\n4\n", 1, ""},
		{"D2 insert before crlf", "1\r\n2\n3\r\n4\n", 3, 2, "x", true, "1\r\n2\nx\r\n3\r\n4\n", 0, ""},
		{"D3 insert before lf", "1\r\n2\n3\r\n4\n", 2, 1, "x", true, "1\r\nx\n2\n3\r\n4\n", 0, ""},
		{"D4 multi replace", "1\r\n2\n3\r\n", 2, 3, "A\nB\nC", true, "1\r\nA\nB\r\nC\r\n", 3, ""},
		{"D7 mixed delete", "1\r\n2\n3\r\n", 2, 2, "", true, "1\r\n3\r\n", 1, ""},
		{"D8 mixed delete first", "1\r\n2\n3\r\n", 1, 1, "", true, "2\n3\r\n", 1, ""},
		{"D9 final-no-NL replace", "1\r\n2", 2, 2, "TWO", true, "1\r\nTWO", 1, ""},
		{"D10 extend first", "1\r\n2", 1, 1, "A\nB", true, "A\r\nB\r\n2", 1, ""},
		{"D11 insert trailing-NL", "a\nb\n", 2, 1, "x\n", true, "a\nx\n\nb\n", 0, ""},
		{"D12 replace trailing-NL", "a\nb\nc\n", 2, 2, "B\n", true, "a\nB\n\nc\n", 1, ""},
		{"D13 same-text noop", "a\nb\nc\n", 2, 2, "b", true, "a\nb\nc\n", 1, ""},
		{"D14 empty insert", "a\nb\n", 2, 1, "", true, "a\nb\n", 0, ""},
		{"D15 delete through sentinel", "a\nb\nc\n", 2, 4, "", true, "a", 4, ""},
		{"D16 delete all content", "a\nb\nc\n", 1, 3, "", true, "", 5, ""},

		// E. error bounds (the TS non-integer cases are type-level in Go).
		{"E2 start < 1", "a\nb\n", 0, 0, "x", false, "", 0, "start_line must be >= 1"},
		{"E3 end < start-1", "a\nb\n", 3, 1, "x", false, "", 0, "must be >= start_line-1"},
		{"E4 end > count", "a\nb\n", 1, 5, "x", false, "", 0, "end_line 5 out of range"},
		{"E5 insert > max", "a\nb\n", 6, 5, "x", false, "", 0, "max insert is 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyRangeEdit(tt.content, tt.startLine, tt.endLine, tt.replacement)
			if got.OK != tt.wantOK {
				t.Fatalf("ok = %v, want %v (%+v)", got.OK, tt.wantOK, got)
			}
			if !tt.wantOK {
				if !strings.Contains(got.Error, tt.wantErrSubstr) {
					t.Fatalf("error = %q, want substring %q", got.Error, tt.wantErrSubstr)
				}
				return
			}
			if got.Content != tt.wantContent {
				t.Fatalf("content = %q, want %q", got.Content, tt.wantContent)
			}
			if got.ReplacedChars != tt.wantReplaced {
				t.Fatalf("replacedChars = %d, want %d", got.ReplacedChars, tt.wantReplaced)
			}
		})
	}
}

func TestApplyMoveLines(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		startLine     int
		endLine       int
		insertBefore  int
		wantOK        bool
		wantContent   string
		wantMoved     int
		wantErrSubstr string
	}{
		// B. existing test.mjs semantics.
		{"B6 move middle to end", "a\nb\nc\nd\n", 2, 3, 5, true, "a\nd\nb\nc\n", 3, ""},

		// C. trailing-newline move defects.
		{"C7 move first to end", "a\nb\nc\n", 1, 1, 5, true, "b\nc\na\n", 1, ""},
		{"C8 move last to first", "a\nb\nc\n", 3, 3, 1, true, "c\na\nb\n", 1, ""},
		{"C9 no-trailing first to end", "a\nb\nc", 1, 1, 4, true, "b\nc\na", 1, ""},
		{"C10 no-trailing last to first", "a\nb\nc", 3, 3, 1, true, "c\na\nb", 1, ""},
		{"C11 crlf first to end", "a\r\nb\r\nc\r\n", 1, 1, 5, true, "b\r\nc\r\na\r\n", 1, ""},
		{"C12 crlf no-trailing first to end", "a\r\nb\r\nc", 1, 1, 4, true, "b\r\nc\r\na", 1, ""},
		{"C14 sentinel move rejected", "a\nb\n", 3, 3, 1, false, "", 0, "trailing newline"},
		{"C15 empty move rejected", "", 1, 1, 1, false, "", 0, "trailing newline"},
		{"C18 noop same-spot", "a\nb\nc\n", 2, 2, 2, true, "a\nb\nc\n", 1, ""},
		{"C19 noop end-plus-one", "a\nb\nc\n", 2, 2, 3, true, "a\nb\nc\n", 1, ""},

		// D. mixed-EOL moves.
		{"D5 mixed move", "1\r\n2\n3\r\n4\n", 2, 2, 4, true, "1\r\n3\r\n2\n4\n", 1, ""},
		{"D6 mixed first to end", "1\r\n2\n3\r\n", 1, 1, 5, true, "2\n3\r\n1\r\n", 1, ""},

		// E. error bounds (the TS non-integer cases are type-level in Go).
		{"E7 move start < 1", "a\nb\n", 0, 1, 1, false, "", 0, "end_line >= start_line >= 1"},
		{"E8 move end < start", "a\nb\n", 2, 1, 1, false, "", 0, "end_line >= start_line >= 1"},
		{"E9 move end > count", "a\nb\n", 1, 4, 1, false, "", 0, "end_line 4 out of range"},
		{"E10 move dest < 1", "a\nb\n", 1, 1, 0, false, "", 0, "insert_before 0 out of range"},
		{"E11 move dest > max", "a\nb\n", 1, 1, 5, false, "", 0, "insert_before 5 out of range"},
		{"E12 move inside", "a\nb\nc\nd\n", 2, 3, 3, false, "", 0, "lies inside"},
		{"E13 move span incl sentinel", "a\nb\nc\n", 2, 4, 1, false, "", 0, "trailing newline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyMoveLines(tt.content, tt.startLine, tt.endLine, tt.insertBefore)
			if got.OK != tt.wantOK {
				t.Fatalf("ok = %v, want %v (%+v)", got.OK, tt.wantOK, got)
			}
			if !tt.wantOK {
				if !strings.Contains(got.Error, tt.wantErrSubstr) {
					t.Fatalf("error = %q, want substring %q", got.Error, tt.wantErrSubstr)
				}
				return
			}
			if got.Content != tt.wantContent {
				t.Fatalf("content = %q, want %q", got.Content, tt.wantContent)
			}
			if got.MovedChars != tt.wantMoved {
				t.Fatalf("movedChars = %d, want %d", got.MovedChars, tt.wantMoved)
			}
		})
	}
}

func TestApplyRangeEditLineCountParity(t *testing.T) {
	// F1: segment line count matches the ToLfLines model for every EOL shape,
	// pinned via the reported counts.
	fixtures := []string{
		"",
		"a",
		"a\n",
		"a\nb",
		"a\nb\n",
		"\n",
		"\n\n",
		"a\r\nb",
		"a\rb",
		"a\r",
		"a\r\n",
		"a\n\nb",
		"1\r\n2\n3\r\n4",
		"1\r\n2\n3\r\n4\n",
	}
	for _, fixture := range fixtures {
		want := len(ToLfLines(fixture))
		got := ApplyRangeEdit(fixture, 999, 999, "x")
		if got.OK || !strings.Contains(got.Error, "file has "+strconv.Itoa(want)+" lines") {
			t.Errorf("fixture %q: got %+v, want count %d", fixture, got, want)
		}
	}
}

func TestActiveEditMode(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default when unset", map[string]string{}, "enforce"},
		{"empty value", map[string]string{"CAVE_TOOLS_MODE": ""}, "enforce"},
		{"lowercase", map[string]string{"CAVE_TOOLS_MODE": "off"}, "off"},
		{"uppercase", map[string]string{"CAVE_TOOLS_MODE": "OFF"}, "off"},
		{"mixed and padded", map[string]string{"CAVE_TOOLS_MODE": "  Hint "}, "hint"},
		{"strict", map[string]string{"CAVE_TOOLS_MODE": "Strict"}, "strict"},
		{"unknown falls back", map[string]string{"CAVE_TOOLS_MODE": "banana"}, "enforce"},
		{"whitespace only falls back", map[string]string{"CAVE_TOOLS_MODE": "   "}, "enforce"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActiveEditMode(toolsTestCtx(tt.env)); got != tt.want {
				t.Fatalf("ActiveEditMode = %q, want %q", got, tt.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TS counts chars in UTF-16 units (String.length); expected values from
// dist/tools/line-range.js on the same inputs.
func TestLineRangeCountsUTF16Units(t *testing.T) {
	c := "añb\nçd\ne\n"
	if r := ApplyRangeEdit(c, 1, 2, "x"); r.ReplacedChars != 6 {
		t.Fatalf("ReplacedChars = %d, want 6", r.ReplacedChars)
	}
	if m := ApplyMoveLines(c, 1, 1, 3); m.MovedChars != 3 {
		t.Fatalf("MovedChars = %d, want 3", m.MovedChars)
	}
	if v := VerifyRangeChecksum(c, 1, 1, "ñññ"); v.Error != "expected_range_checksum must be at least 4 hex characters" {
		t.Fatalf("VerifyRangeChecksum(ñññ) error = %q", v.Error)
	}
}
