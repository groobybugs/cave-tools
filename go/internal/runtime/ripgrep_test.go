package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobToRegExp(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		match   []string
		nomatch []string
	}{
		{
			name:    "star stays inside a segment",
			pattern: "*",
			match:   []string{"a", "a.ts", ".hidden"},
			nomatch: []string{"a/b", "sub/deep.ts"},
		},
		{
			name:    "double star crosses segments",
			pattern: "**/*.ts",
			match:   []string{"sub/deep.ts", "a/b/c.ts"},
			nomatch: []string{"deep.ts", "deep.tsx"},
		},
		{
			name:    "question mark is one char",
			pattern: "?",
			match:   []string{"a"},
			nomatch: []string{"ab", "a/b"},
		},
		{
			name:    "dots are literal",
			pattern: "a.b",
			match:   []string{"a.b"},
			nomatch: []string{"axb"},
		},
		{
			name:    "brackets and dollars are literal",
			pattern: "[a]$",
			match:   []string{"[a]$"},
			nomatch: []string{"a", "[a]"},
		},
		{
			// The glob source is fed to jsre; JS whitespace characters are
			// literals in a glob and must survive the translation.
			name:    "unicode whitespace is literal",
			pattern: "a\u00a0*",
			match:   []string{"a\u00a0b", "a\u00a0"},
			nomatch: []string{"a b", "ab"},
		},
		{
			name:    "bom is literal",
			pattern: "\ufeffa?",
			match:   []string{"\ufeffab"},
			nomatch: []string{"\ufeffa", "ab"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matcher := globToRegExp(test.pattern)
			for _, value := range test.match {
				if !matcher.MatchString(value) {
					t.Errorf("%q must match %q", test.pattern, value)
				}
			}
			for _, value := range test.nomatch {
				if matcher.MatchString(value) {
					t.Errorf("%q must not match %q", test.pattern, value)
				}
			}
		})
	}
}

func TestParsePathLines(t *testing.T) {
	stdout := strings.Join([]string{
		"./a.txt",
		".\\b.txt",
		"/abs/c.txt",
		".//d.txt",
		"..\\e.txt",
		"",
		"  spaced.txt  ",
		"nested\\f.txt",
	}, "\n")

	got := parsePathLines(stdout, 10)
	want := []string{"a.txt", "b.txt", "abs/c.txt", "d.txt", "../e.txt", "spaced.txt", "nested/f.txt"}
	if len(got) != len(want) {
		t.Fatalf("parsePathLines = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("parsePathLines[%d] = %q, want %q", index, got[index], want[index])
		}
	}
	if limited := parsePathLines(stdout, 2); len(limited) != 2 || limited[0] != "a.txt" || limited[1] != "b.txt" {
		t.Fatalf("parsePathLines(limit 2) = %#v", limited)
	}
	if zero := parsePathLines(stdout, 0); len(zero) != 0 {
		t.Fatalf("parsePathLines(limit 0) = %#v", zero)
	}

	// Muse regression: line.trim() strips U+FEFF but keeps U+0085, the
	// opposite of strings.TrimSpace.
	jsWhitespace := parsePathLines("\ufeffa.ts\nb.ts\u0085\n", 10)
	wantJS := []string{"a.ts", "b.ts\u0085"}
	if len(jsWhitespace) != len(wantJS) {
		t.Fatalf("parsePathLines(JS whitespace) = %#v, want %#v", jsWhitespace, wantJS)
	}
	for index := range wantJS {
		if jsWhitespace[index] != wantJS[index] {
			t.Fatalf("parsePathLines(JS whitespace)[%d] = %q, want %q", index, jsWhitespace[index], wantJS[index])
		}
	}
}

func TestFindWithNode(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"a.ts",
		".hidden.ts",
		"b.txt",
		"sub/deep.ts",
		"sub/other.txt",
		"dir/c.ts",
		".git/ignored.ts",
	}
	for _, name := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := findWithNode(root, "*.ts", 50, true)
	want := []string{".hidden.ts", "a.ts", "dir/c.ts", "sub/deep.ts"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("findWithNode(hidden) = %#v, want %#v", got, want)
	}

	got = findWithNode(root, "*.ts", 50, false)
	want = []string{"a.ts", "dir/c.ts", "sub/deep.ts"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("findWithNode(visible) = %#v, want %#v", got, want)
	}

	// Patterns containing "/" match the relative path only.
	got = findWithNode(root, "sub/*.ts", 50, true)
	if len(got) != 1 || got[0] != "sub/deep.ts" {
		t.Fatalf("findWithNode(sub/*.ts) = %#v", got)
	}

	// .git is always skipped, even when hidden files are included.
	for _, value := range got {
		if strings.HasPrefix(value, ".git/") {
			t.Fatalf("findWithNode returned a .git entry: %#v", got)
		}
	}

	if limited := findWithNode(root, "*.ts", 1, true); len(limited) != 1 {
		t.Fatalf("findWithNode(limit 1) = %#v", limited)
	}
	if zero := findWithNode(root, "*.ts", 0, true); len(zero) != 0 {
		t.Fatalf("findWithNode(limit 0) = %#v", zero)
	}
}

// rgMocks installs fake rg/fd executables and returns a call context whose
// environment carries the mock outputs.
func rgMocks(t *testing.T, withFd bool) context.Context {
	t.Helper()
	binDir := t.TempDir()
	writeScript(t, binDir, "rg", `#!/bin/sh
/bin/cat "$RGMOCK"
if [ -n "$RGMOCK_ERR" ]; then printf '%s' "$RGMOCK_ERR" >&2; fi
exit "${RGMOCK_EXIT:-0}"
`)
	if withFd {
		writeScript(t, binDir, "fd", `#!/bin/sh
/bin/cat "$FDMOCK"
exit "${FDMOCK_EXIT:-0}"
`)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", t.TempDir())
	stdout := filepath.Join(t.TempDir(), "stdout.json")
	if err := os.WriteFile(stdout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	extra := map[string]string{
		"PATH":        binDir,
		"RGMOCK":      stdout,
		"RGMOCK_ERR":  "",
		"RGMOCK_EXIT": "",
	}
	if withFd {
		fdOut := filepath.Join(t.TempDir(), "fd.txt")
		if err := os.WriteFile(fdOut, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		extra["FDMOCK"] = fdOut
		extra["FDMOCK_EXIT"] = "0"
	}
	ResetBinsCache()
	return w1bCtx(t, t.TempDir(), extra)
}

func mockPath(t *testing.T, ctx context.Context, key string) string {
	t.Helper()
	return GetEnv(ctx)[key]
}

func TestRgFilesUsesFd(t *testing.T) {
	ctx := rgMocks(t, true)
	if err := os.WriteFile(mockPath(t, ctx, "FDMOCK"), []byte("./x.ts\n./sub/y.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := RgFiles(ctx, t.TempDir(), "*.ts", 10, true)
	if err != nil {
		t.Fatalf("RgFiles: %v", err)
	}
	if len(got) != 2 || got[0] != "x.ts" || got[1] != "sub/y.ts" {
		t.Fatalf("RgFiles = %#v", got)
	}
}

func TestRgFilesFallsBackToRipgrep(t *testing.T) {
	ctx := rgMocks(t, true)
	if err := os.WriteFile(mockPath(t, ctx, "FDMOCK"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := GetEnv(ctx)
	env["FDMOCK_EXIT"] = "2"
	if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte("./r.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := RgFiles(ctx, t.TempDir(), "*.ts", 10, true)
	if err != nil {
		t.Fatalf("RgFiles: %v", err)
	}
	if len(got) != 1 || got[0] != "r.ts" {
		t.Fatalf("RgFiles = %#v", got)
	}
}

func TestRgFilesRipgrepError(t *testing.T) {
	ctx := rgMocks(t, false)
	previous := installTool
	defer func() { installTool = previous }()
	installTool = func(context.Context, SearchBin, func(string)) (string, error) { return "", nil }

	if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	env := GetEnv(ctx)
	env["RGMOCK_EXIT"] = "2"
	env["RGMOCK_ERR"] = "boom\n"
	_, err := RgFiles(ctx, t.TempDir(), "*.ts", 10, true)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("RgFiles error = %v, want boom", err)
	}

	// stderr.trim() in TS strips U+FEFF; strings.TrimSpace would keep it.
	env["RGMOCK_ERR"] = "boom\ufeff"
	_, err = RgFiles(ctx, t.TempDir(), "*.ts", 10, true)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("RgFiles error = %q, want boom", err)
	}
}

func TestRgFilesNoBinsUsesNodeFinder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "only.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := installTool
	defer func() { installTool = previous }()
	installTool = func(context.Context, SearchBin, func(string)) (string, error) { return "", nil }
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	ResetBinsCache()
	ctx := w1bCtx(t, root, map[string]string{"PATH": ""})

	got, err := RgFiles(ctx, root, "*.ts", 10, true)
	if err != nil {
		t.Fatalf("RgFiles: %v", err)
	}
	if len(got) != 1 || got[0] != "only.ts" {
		t.Fatalf("RgFiles = %#v", got)
	}
}

func TestRgGrepParsesMatches(t *testing.T) {
	ctx := rgMocks(t, true)
	body := strings.Join([]string{
		`{"type":"begin","data":{"path":{"text":"./src/a.ts"}}}`,
		`{"type":"match","data":{"path":{"text":"./src/a.ts"},"lines":{"text":"hello\n"},"line_number":7}}`,
		`{"type":"match","data":{"path":{"text":"src\\b.ts"},"lines":{"text":"world\r\n"},"line_number":9}}`,
		"",
	}, "\n")
	if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "o", Limit: 50})
	if err != nil {
		t.Fatalf("RgGrep: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("matches = %#v", result.Matches)
	}
	if result.Matches[0] != (GrepMatch{Path: "src/a.ts", Line: 7, Text: "hello"}) {
		t.Fatalf("match[0] = %#v", result.Matches[0])
	}
	if result.Matches[1] != (GrepMatch{Path: "src/b.ts", Line: 9, Text: "world"}) {
		t.Fatalf("match[1] = %#v", result.Matches[1])
	}
	if result.Truncated {
		t.Fatal("Truncated = true")
	}

	t.Run("limit truncates", func(t *testing.T) {
		limited, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "o", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(limited.Matches) != 1 || !limited.Truncated {
			t.Fatalf("limited = %#v", limited)
		}
	})

	t.Run("long text is truncated", func(t *testing.T) {
		long := strings.Repeat("x", 2500)
		line := `{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"` + long + `\n"},"line_number":1}}`
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Repeat("x", 2000) + "..."
		if result.Matches[0].Text != want {
			t.Fatalf("text length = %d, want %d", len(result.Matches[0].Text), len(want))
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte("not json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
		if err == nil || err.Error() != "Invalid ripgrep JSON output" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("oversized record", func(t *testing.T) {
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(strings.Repeat("a", 70000)), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
		if err == nil || err.Error() != "Ripgrep JSON record exceeded 65536 bytes" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid pattern", func(t *testing.T) {
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = "2"
		env["RGMOCK_ERR"] = "regex parse error: bad\n"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "[", Limit: 10})
		if err == nil || err.Error() != "regex parse error: bad" {
			t.Fatalf("error = %v", err)
		}
		env["RGMOCK_EXIT"] = ""
		env["RGMOCK_ERR"] = ""
	})

	t.Run("exit 2 without a pattern error truncates", func(t *testing.T) {
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = "2"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Truncated {
			t.Fatal("Truncated = false")
		}
		env["RGMOCK_EXIT"] = ""
	})

	// Muse regression: the error text is stderr.trim() in TS, so U+FEFF is
	// stripped while strings.TrimSpace would keep it.
	t.Run("stderr trim uses JS whitespace", func(t *testing.T) {
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = "3"
		env["RGMOCK_ERR"] = "boom\ufeff"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("error = %q, want %q", err, "boom")
		}
		env["RGMOCK_EXIT"] = ""
		env["RGMOCK_ERR"] = ""
	})

	t.Run("invalid pattern trim uses JS whitespace", func(t *testing.T) {
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = "2"
		env["RGMOCK_ERR"] = "regex parse error: bad\ufeff"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "[", Limit: 10})
		if err == nil || err.Error() != "regex parse error: bad" {
			t.Fatalf("error = %q, want %q", err, "regex parse error: bad")
		}
		env["RGMOCK_EXIT"] = ""
		env["RGMOCK_ERR"] = ""
	})

	// A U+FEFF-only line is blank after JS trim, so it is skipped instead of
	// failing JSON parsing.
	t.Run("bom only line is skipped", func(t *testing.T) {
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = ""
		env["RGMOCK_ERR"] = ""
		body := "\ufeff\n" +
			`{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"x\n"},"line_number":1}}` + "\n"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
		if err != nil {
			t.Fatalf("RgGrep: %v", err)
		}
		if len(result.Matches) != 1 || result.Matches[0] != (GrepMatch{Path: "a.ts", Line: 1, Text: "x"}) {
			t.Fatalf("matches = %#v", result.Matches)
		}
	})
}

func TestRgGrepWithoutRipgrep(t *testing.T) {
	previous := installTool
	defer func() { installTool = previous }()
	installTool = func(context.Context, SearchBin, func(string)) (string, error) { return "", nil }
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	ResetBinsCache()
	ctx := w1bCtx(t, t.TempDir(), map[string]string{"PATH": ""})

	_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 10})
	if err == nil || err.Error() != "ripgrep not available (not on PATH and download failed)" {
		t.Fatalf("error = %v", err)
	}
}

func TestReadLineWindow(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(file, []byte("l1\r\nl2\rl3\nl4"), 0o600); err != nil {
		t.Fatal(err)
	}

	window, err := ReadLineWindow(file, 2, 1)
	if err != nil {
		t.Fatalf("ReadLineWindow: %v", err)
	}
	want := []LineWindow{{Line: 1, Text: "l1"}, {Line: 2, Text: "l2", Match: true}, {Line: 3, Text: "l3"}}
	if len(window) != len(want) {
		t.Fatalf("window = %#v", window)
	}
	for index := range want {
		if window[index] != want[index] {
			t.Fatalf("window[%d] = %#v, want %#v", index, window[index], want[index])
		}
	}

	window, err = ReadLineWindow(file, 4, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 4 || window[3].Text != "l4" || !window[3].Match {
		t.Fatalf("window = %#v", window)
	}

	t.Run("long lines are truncated", func(t *testing.T) {
		long := strings.Repeat("y", 600)
		path := filepath.Join(dir, "long.txt")
		if err := os.WriteFile(path, []byte(long+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		window, err := ReadLineWindow(path, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if window[0].Text != strings.Repeat("y", 500)+"... (truncated)" {
			t.Fatalf("text = %q", window[0].Text)
		}
	})

	if _, err := ReadLineWindow(filepath.Join(dir, "missing.txt"), 1, 0); err == nil {
		t.Fatal("ReadLineWindow(missing) must fail")
	}
}
