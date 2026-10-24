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
