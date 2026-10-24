package read

import (
	"math"
	"testing"
)

// TestNodePathHelpers pins nodeDirname/nodeBasename/nodeExtname against values
// captured from node:path on the TS side (posix; see the path matrix probe in
// the task report).
func TestNodePathHelpers(t *testing.T) {
	cases := []struct {
		path     string
		dirname  string
		basename string
		extname  string
	}{
		{"", ".", "", ""},
		{".", ".", ".", ""},
		{"..", ".", "..", ""},
		{"/", "/", "", ""},
		{"//", "/", "", ""},
		{"///", "/", "", ""},
		{"a", ".", "a", ""},
		{"a/", ".", "a", ""},
		{"a/b", "a", "b", ""},
		{"a/b/", "a", "b", ""},
		{"/a", "/", "a", ""},
		{"/a/", "/", "a", ""},
		{"/a/b", "/a", "b", ""},
		{"/a/b/", "/a", "b", ""},
		{"//a", "//", "a", ""},
		{"//a/b", "//a", "b", ""},
		{"a//b", "a/", "b", ""},
		{"/a//b//", "/a/", "b", ""},
		{"a.", ".", "a.", "."},
		{"a..", ".", "a..", "."},
		{".a", ".", ".a", ""},
		{".a.b", ".", ".a.b", ".b"},
		{"..a", ".", "..a", ".a"},
		{"...", ".", "...", "."},
		{"....", ".", "....", "."},
		{"a/.", "a", ".", ""},
		{"a/..", "a", "..", ""},
		{"/a/b.", "/a", "b.", "."},
		{"x.tar.gz", ".", "x.tar.gz", ".gz"},
		{".hidden.txt", ".", ".hidden.txt", ".txt"},
		{"a/b/.c", "a/b", ".c", ""},
	}
	for _, testCase := range cases {
		if got := nodeDirname(testCase.path); got != testCase.dirname {
			t.Errorf("dirname(%q) = %q, want %q", testCase.path, got, testCase.dirname)
		}
		if got := nodeBasename(testCase.path); got != testCase.basename {
			t.Errorf("basename(%q) = %q, want %q", testCase.path, got, testCase.basename)
		}
		if got := nodeExtname(testCase.path); got != testCase.extname {
			t.Errorf("extname(%q) = %q, want %q", testCase.path, got, testCase.extname)
		}
	}
}

// TestJsLocaleCompare pins the ICU-compatible ordering the directory listing
// sort relies on, using values from Node's localeCompare.
func TestJsLocaleCompare(t *testing.T) {
	ordered := []string{
		"a.txt", "A.txt", "B", "b.txt", "c.md",
	}
	for i := 0; i+1 < len(ordered); i++ {
		if got := jsLocaleCompare(ordered[i], ordered[i+1]); got >= 0 {
			t.Fatalf("%q should sort before %q (got %d)", ordered[i], ordered[i+1], got)
		}
	}
	// Primary order is case-folded; case only breaks ties.
	if jsLocaleCompare("A", "ab") >= 0 {
		t.Fatal("\"A\" must sort before \"ab\" (primary prefix rule)")
	}
	if jsLocaleCompare("_x", "1a") >= 0 {
		t.Fatal("punctuation sorts before digits in ICU order")
	}
	for _, pair := range [][2]string{
		{"a", "A"}, {"aA", "Aa"}, {"aa", "aA"}, {"Ab", "aB"},
		{"z10", "z2"}, {"Z1", "z10"}, {"b.txt", "A.txt"}, {"", "a"},
	} {
		if jsLocaleCompare(pair[0], pair[1]) != -jsLocaleCompare(pair[1], pair[0]) {
			t.Fatalf("comparator is not antisymmetric for %q/%q", pair[0], pair[1])
		}
	}
	if jsLocaleCompare("same", "same") != 0 {
		t.Fatal("identical strings must compare equal")
	}
}

// TestJsNumberString pins ECMAScript Number::toString(10) formatting.
func TestJsNumberString(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{1, "1"},
		{-1, "-1"},
		{1.5, "1.5"},
		{200, "200"},
		{1e21, "1e+21"},
		{1e20, "100000000000000000000"},
		{1e-7, "1e-7"},
		{0.000001, "0.000001"},
		{0.0000001, "1e-7"},
		{123456789012345680000, "123456789012345680000"},
		{1.2345678901234567, "1.2345678901234567"},
		{1e300, "1e+300"},
		{9007199254740992, "9007199254740992"},
		{-9007199254740992, "-9007199254740992"},
		{1.0 / 3.0, "0.3333333333333333"},
		{math.NaN(), "NaN"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
	}
	for _, testCase := range cases {
		if got := jsNumberString(testCase.value); got != testCase.want {
			t.Errorf("jsNumberString(%v) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

// TestJsCoercions covers String(value) / Number(value) for JSON shapes.
func TestJsCoercions(t *testing.T) {
	if got := jsString(nil); got != "undefined" {
		t.Errorf("String(nil) = %q", got)
	}
	if got := jsString(true); got != "true" {
		t.Errorf("String(true) = %q", got)
	}
	if got := jsString(1.5); got != "1.5" {
		t.Errorf("String(1.5) = %q", got)
	}
	if got := jsString([]any{"a", 1.0, nil}); got != "a,1," {
		t.Errorf("String(array) = %q", got)
	}
	if got := jsString(map[string]any{"a": 1.0}); got != "[object Object]" {
		t.Errorf("String(object) = %q", got)
	}

	numberCases := []struct {
		value any
		want  float64
	}{
		{nil, math.NaN()},
		{true, 1},
		{false, 0},
		{"", 0},
		{"  ", 0},
		{"3", 3},
		{" 3.5 ", 3.5},
		{"abc", math.NaN()},
		{"0x10", 16},
		{"0b101", 5},
		{"0o17", 15},
		{"Infinity", math.Inf(1)},
		{"-Infinity", math.Inf(-1)},
		{"1e3", 1000},
		{"1_000", math.NaN()},
		{[]any{}, 0},
		{[]any{"7"}, 7},
		{map[string]any{}, math.NaN()},
	}
	for _, testCase := range numberCases {
		got := jsNumber(testCase.value)
		if math.IsNaN(testCase.want) {
			if !math.IsNaN(got) {
				t.Errorf("Number(%v) = %v, want NaN", testCase.value, got)
			}
			continue
		}
		if got != testCase.want {
			t.Errorf("Number(%v) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
}

// TestJsFalsyAndSlice covers `value || fallback` and Array.prototype.slice.
func TestJsFalsyAndSlice(t *testing.T) {
	for _, value := range []any{nil, false, 0.0, math.NaN(), ""} {
		if !isFalsy(value) {
			t.Errorf("isFalsy(%v) = false", value)
		}
	}
	for _, value := range []any{true, 1.0, "0", []any{}, map[string]any{}} {
		if isFalsy(value) {
			t.Errorf("isFalsy(%v) = true", value)
		}
	}

	cases := []struct {
		start float64
		end   float64
		from  int
		to    int
	}{
		{0, 2, 0, 2},
		{-3, 197, 2, 5}, // slice(-3, ...) of 5 rows
		{3, 197, 3, 5},  // clamped to length
		{9, 197, 5, 5},  // fully out of range
		{4, 2, 4, 4},    // end < start -> empty
		{math.NaN(), 2, 0, 2},
	}
	for _, testCase := range cases {
		from, to := jsSliceBounds(testCase.start, testCase.end, 5)
		if from != testCase.from || to != testCase.to {
			t.Errorf("jsSliceBounds(%v, %v, 5) = (%d, %d), want (%d, %d)",
				testCase.start, testCase.end, from, to, testCase.from, testCase.to)
		}
	}
}

// TestUtf16Helpers covers the UTF-16 length/prefix model.
func TestUtf16Helpers(t *testing.T) {
	if got := utf16Len("abc"); got != 3 {
		t.Errorf("utf16Len(abc) = %d", got)
	}
	if got := utf16Len("a😀b"); got != 4 {
		t.Errorf("utf16Len(astral) = %d", got)
	}
	if got := utf16Prefix("a😀b", 2); got != "a" {
		t.Errorf("utf16Prefix should drop a split surrogate pair: %q", got)
	}
	if got := utf16Prefix("a😀b", 3); got != "a😀" {
		t.Errorf("utf16Prefix(3) = %q", got)
	}
	if got := utf16Prefix("abc", 10); got != "abc" {
		t.Errorf("utf16Prefix(10) = %q", got)
	}
}
