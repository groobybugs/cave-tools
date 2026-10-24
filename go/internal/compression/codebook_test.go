package compression

import (
	"reflect"
	"strings"
	"testing"
)

// resetGlobalCodebook gives each test a fresh process-wide codebook.
func resetGlobalCodebook() {
	globalCodebook = &Codebook{patternToID: make(map[string]string)}
}

// TestCodebook ports the codebook assertions from test.mjs (2h).
func TestCodebook(t *testing.T) {
	resetGlobalCodebook()
	t.Cleanup(resetGlobalCodebook)

	AddCodebookFile("a.ts", "import { useState } from 'react';\nexport const THEME = 'dark';\n")
	AddCodebookFile("b.ts", "import { useState } from 'react';\nexport const THEME = 'dark';\n")
	AddCodebookFile("c.ts", "import { useState } from 'react';\nexport const OTHER = 'value';\n")
	if GetCodebookSize() <= 0 {
		t.Fatalf("GetCodebookSize() = %d after 3 files, want > 0", GetCodebookSize())
	}

	text, legend, refsUsed := CompressWithCodebook("import { useState } from 'react';\nexport const UNIQUE = 'x';")
	if !strings.Contains(text, "[§") {
		t.Errorf("compressed text = %q, want a codebook reference", text)
	}
	if !strings.Contains(legend, "§CODEBOOK") {
		t.Errorf("legend = %q, want §CODEBOOK", legend)
	}

	// Byte-exact TS output for the same fixture.
	if want := "[§0]\nexport const UNIQUE = 'x';"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if want := "§CODEBOOK:\n  §0=import { useState } from 'react';"; legend != want {
		t.Errorf("legend = %q, want %q", legend, want)
	}
	if want := []string{"§0"}; !reflect.DeepEqual(refsUsed, want) {
		t.Errorf("refsUsed = %v, want %v", refsUsed, want)
	}
	if got := GetCodebookSize(); got != 1 {
		t.Errorf("GetCodebookSize() = %d, want 1", got)
	}
}

// TestCodebookLegendTruncation pins the 57-code-unit shortening for long
// patterns, matching String.prototype.slice in the TS implementation.
func TestCodebookLegendTruncation(t *testing.T) {
	resetGlobalCodebook()
	t.Cleanup(resetGlobalCodebook)

	longLine := "const someVeryLongIdentifierNameThatGoesOnAndOnAndOnBeyondSixtyCharacters = 1;"
	AddCodebookFile("d.ts", longLine+"\n")
	AddCodebookFile("e.ts", longLine+"\n")
	AddCodebookFile("f.ts", longLine+"\n")

	text, legend, refsUsed := CompressWithCodebook(longLine + "\n")
	if want := "[§0]\n"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if want := "§CODEBOOK:\n  §0=const someVeryLongIdentifierNameThatGoesOnAndOnAndOnBeyon..."; legend != want {
		t.Errorf("legend = %q, want %q", legend, want)
	}
	if want := []string{"§0"}; !reflect.DeepEqual(refsUsed, want) {
		t.Errorf("refsUsed = %v, want %v", refsUsed, want)
	}
}

// TestNormalizeLineJSWhitespace is the Muse regression: normalizeLine is
// split(/\s+/) in TS, so NBSP and U+FEFF collapse to one space while U+0085
// (not JS whitespace) is kept. Expected values come from node.
func TestNormalizeLineJSWhitespace(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"a\ufeffb", "a b"},
		{"\ufeff", ""},
		{"  a  b  ", "a b"},
		{"a\u00a0b", "a b"},
		{"a\u0085b", "a\u0085b"},
		{"", ""},
		{"§0 x", "§0 x"},
	}

	for _, test := range tests {
		if got := normalizeLine(test.input); got != test.want {
			t.Errorf("normalizeLine(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

// TestCodebookNormalizesFEFF proves a U+FEFF-separated boilerplate line and
// its space-separated form share one entry, exactly like the TS reference.
func TestCodebookNormalizesFEFF(t *testing.T) {
	resetGlobalCodebook()
	t.Cleanup(resetGlobalCodebook)

	feffLine := "const A\ufeff=\ufeff1;"
	AddCodebookFile("a.ts", feffLine+"\n")
	AddCodebookFile("b.ts", feffLine+"\n")
	AddCodebookFile("c.ts", feffLine+"\n")

	text, legend, refsUsed := CompressWithCodebook("const A = 1;")
	if want := "[§0]"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if want := "§CODEBOOK:\n  §0=const A = 1;"; legend != want {
		t.Errorf("legend = %q, want %q", legend, want)
	}
	if want := []string{"§0"}; !reflect.DeepEqual(refsUsed, want) {
		t.Errorf("refsUsed = %v, want %v", refsUsed, want)
	}
}

// TestCodebookLegendLoneSurrogate covers slice(0, 57) cutting a surrogate
// pair: JS keeps a lone high surrogate in the string, and Node serialises it
// to UTF-8 as U+FFFD (EF BF BD), which is what the Go port produces.
func TestCodebookLegendLoneSurrogate(t *testing.T) {
	resetGlobalCodebook()
	t.Cleanup(resetGlobalCodebook)

	longLine := strings.Repeat("a", 56) + "\U0001F600" + "bcdefghij"
	AddCodebookFile("d.ts", longLine+"\n")
	AddCodebookFile("e.ts", longLine+"\n")
	AddCodebookFile("f.ts", longLine+"\n")

	text, legend, refsUsed := CompressWithCodebook(longLine + "\n")
	if want := "[§0]\n"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	want := "§CODEBOOK:\n  §0=" + strings.Repeat("a", 56) + "\uFFFD" + "..."
	if legend != want {
		t.Errorf("legend = %q, want %q", legend, want)
	}
	// node: Buffer.from(legend, "utf8").toString("hex") ends with efbfbd666666
	// (U+FFFD followed by the "..." suffix).
	if got := hexEncode(legend[len(legend)-6 : len(legend)-3]); got != "efbfbd" {
		t.Errorf("legend tail = %s, want efbfbd (the lone surrogate serialised as U+FFFD)", got)
	}
	if want := []string{"§0"}; !reflect.DeepEqual(refsUsed, want) {
		t.Errorf("refsUsed = %v, want %v", refsUsed, want)
	}
}

// hexEncode renders the UTF-8 bytes of s as lowercase hex.
func hexEncode(s string) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		out = append(out, digits[s[i]>>4], digits[s[i]&0xf])
	}
	return string(out)
}
