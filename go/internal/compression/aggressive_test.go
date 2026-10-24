package compression

import (
	"strings"
	"testing"
)

// TestAggressiveCompress ports the aggressive read-mode assertions from
// test.mjs (2f and the cave__read edge cases) plus the byte-exact outputs the
// TypeScript reference produces for those fixtures.
func TestAggressiveCompress(t *testing.T) {
	pythonSource := "#!/usr/bin/env python3\n# This is a comment\n\ndef hello():\n    # inline comment\n    return 'world'\n\n\nclass Foo:\n    pass\n"
	jsSource := "// header comment\nconst x = 1;\n\n\n\nconst y = 2;\n"
	tsSource := strings.Join([]string{
		`const url = "https://example.com//x"; // trailing comment`,
		"/** doc */",
		"/**",
		" * block doc",
		" */",
		"/* normal */",
		"const t = `template // not comment`;",
		"//! directive",
		"const s = 'a // b';",
		"export function f() {",
		"  /* inline */ const z = 1;",
		"}",
	}, "\n")
	htmlSource := "<html>\n<!-- comment -->\n<body>hi</body>\n</html>\n"

	tests := []struct {
		name    string
		content string
		ext     string
		want    string
	}{
		{
			name:    "python shebang and comments",
			content: pythonSource,
			ext:     ".py",
			want:    "#!/usr/bin/env python3\n\ndef hello():\n\n    return 'world'\n\nclass Foo:\n    pass",
		},
		{
			name:    "js header and blank lines",
			content: jsSource,
			ext:     ".js",
			want:    "const x = 1;\n\nconst y = 2;",
		},
		{
			name:    "doc comments survive and strings are not comments",
			content: tsSource,
			ext:     ".TS",
			want: strings.Join([]string{
				`const url = "https://example.com//x";`,
				"/** doc */",
				"/**",
				" * block doc",
				" */",
				"const t = `template // not comment`;",
				"//! directive",
				"const s = 'a // b';",
				"export function f() {",
				"}",
			}, "\n"),
		},
		{
			name:    "html block comments",
			content: htmlSource,
			ext:     ".html",
			want:    "<html>\n<body>hi</body>\n</html>",
		},
		{
			name:    "unknown extension returns input",
			content: "a\nb",
			ext:     ".md",
			want:    "a\nb",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AggressiveCompress(test.content, test.ext)
			if got != test.want {
				t.Fatalf("AggressiveCompress(%q, %q) = %q, want %q", test.content, test.ext, got, test.want)
			}
		})
	}
}
