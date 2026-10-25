//go:build parity

// Black-box parity for locale collation: directory listings (ls, read) and the
// fallback file finder sort names with String.prototype.localeCompare in the
// TypeScript implementation. Both sides execute against a fixture whose names
// differ only by case, accents, Turkish dotless/dotted i, width and script, so a
// byte-order sort would produce a different transcript.
package parity

import (
	"path/filepath"
	"sort"
	"testing"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
)

// unicodeCollationNames are the fixture entries. They stay grouped by the
// collation class they exercise so a residual difference is easy to read.
var unicodeCollationNames = map[string]string{
	// case-only twins: primary-equal, resolved by the tertiary level
	"uni/apple.txt": "apple\n",
	"uni/Apple.txt": "Apple\n",
	"uni/APPLE.txt": "APPLE\n",
	"uni/zebra.txt": "zebra\n",
	"uni/Zebra.txt": "Zebra\n",
	// accents, including an NFC/NFD pair that must stay distinct
	"uni/arger.txt":           "arger\n",
	"uni/\u00c4rger.txt":      "\u00c4rger\n",
	"uni/eclair.md":           "eclair\n",
	"uni/\u00e9clair.md":      "\u00e9clair\n",
	"uni/cafe\u0301.txt":      "decomposed cafe\n",
	"uni/caf\u00e9.txt":       "composed cafe\n",
	"uni/nandu.txt":           "nandu\n",
	"uni/\u00f1and\u00fa.txt": "\u00f1and\u00fa\n",
	// Turkish: I -> \u0131 and \u0130 -> i, so these three are all different words
	"uni/istanbul.txt":      "istanbul\n",
	"uni/\u0131stanbul.txt": "dotless istanbul\n",
	"uni/\u0130stanbul.txt": "dotted istanbul\n",
	// ligature and its expansion
	"uni/strasse.txt":     "strasse\n",
	"uni/stra\u00dfe.txt": "strasse sharp s\n",
	"uni/oeuvre.txt":      "oeuvre\n",
	"uni/\u0153uvre.txt":  "oeuvre ligature\n",
	// digits: localeCompare is not numeric, so 10 sorts before 2
	"uni/file1.txt":  "one\n",
	"uni/file2.txt":  "two\n",
	"uni/file10.txt": "ten\n",
	// scripts
	"uni/\u4e2d\u6587.txt":       "chinese\n",
	"uni/\u65e5\u672c\u8a9e.md":  "japanese\n",
	"uni/\ud55c\uad6d\uc5b4.txt": "korean\n",
	"uni/emoji\U0001F600.txt":    "emoji\n",
	// punctuation, dotfiles and separators
	"uni/.hidden":         "hidden\n",
	"uni/_underscore.txt": "underscore\n",
	"uni/-dash.txt":       "dash\n",
}

// unicodeCollationFixture is the isolated tree shared by the three cases.
func unicodeCollationFixture() fixtureSpec {
	fixture := baseFixture()
	for name, content := range unicodeCollationNames {
		fixture.Files[name] = fixtureFile{Content: content}
	}
	fixture.Dirs = append(fixture.Dirs,
		filepath.ToSlash("uni/adir"),
		filepath.ToSlash("uni/\u00c4dir"),
		filepath.ToSlash("uni/\u4e2d\u6587dir"),
	)
	return fixture
}

// TestCLIUnicodeCollationDifferential covers the three TS sort sites that order
// user-visible names: ls (src/tools/ls.ts:66), read of a directory
// (src/tools/read.ts:311) and the fallback finder (src/runtime/ripgrep.ts:84).
// The find step hides the system fd by pointing PATH at an empty directory, so
// both implementations take the fallback walker this task ports.
func TestCLIUnicodeCollationDifferential(t *testing.T) {
	tsBinary, tsPrefix := typescriptCLI(t)
	goBinary := sharedGoBinary(t)
	// An empty directory in PATH makes resolveFd fail on both sides.
	emptyBin := t.TempDir()

	cases := []cliCase{
		{
			name:    "ls.unicode-collation",
			fixture: unicodeCollationFixture(),
			steps:   []cliStep{{args: []string{"ls", "uni", "--limit", "100"}}},
		},
		{
			name:    "ls.unicode-collation-paged",
			fixture: unicodeCollationFixture(),
			steps:   []cliStep{{args: []string{"ls", "uni", "--offset", "5", "--limit", "7"}}},
		},
		{
			name:    "read.unicode-collation",
			fixture: unicodeCollationFixture(),
			steps:   []cliStep{{args: []string{"read", "uni", "--limit", "100"}}},
		},
		{
			name:    "find.unicode-collation",
			fixture: unicodeCollationFixture(),
			steps: []cliStep{{
				args: []string{"find", "*.txt", "uni", "--limit", "100"},
				env:  map[string]string{"PATH": emptyBin},
			}},
		},
		{
			name:    "find.unicode-collation-md",
			fixture: unicodeCollationFixture(),
			steps: []cliStep{{
				args: []string{"find", "*.md", "uni", "--limit", "100"},
				env:  map[string]string{"PATH": emptyBin},
			}},
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			runCLICase(t, testCase, tsBinary, tsPrefix, goBinary)
		})
	}
}

// TestCLIUnicodeCollationFixtureIsSensitive proves the fixture exercises
// collation: locale order must differ from byte order, otherwise both sides
// could agree while ignoring localeCompare entirely.
func TestCLIUnicodeCollationFixtureIsSensitive(t *testing.T) {
	names := make([]string, 0, len(unicodeCollationNames))
	for name := range unicodeCollationNames {
		names = append(names, filepath.Base(name))
	}
	byteSorted := append([]string{}, names...)
	sort.Strings(byteSorted)
	localeSorted := append([]string{}, names...)
	rt.SortStringsLocale(localeSorted)
	different := false
	for index := range byteSorted {
		if byteSorted[index] != localeSorted[index] {
			different = true
			break
		}
	}
	if !different {
		t.Fatalf("fixture names sort identically Bytewise and in locale order: %q", byteSorted)
	}
}
