package runtime

import (
	"context"
	"math"
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
if [ -n "$RGARGS" ]; then printf '%s\n' "$@" > "$RGARGS"; fi
/bin/cat "$RGMOCK"
if [ -n "$RGMOCK_ERR" ]; then printf '%s' "$RGMOCK_ERR" >&2; fi
exit "${RGMOCK_EXIT:-0}"
`)
	if withFd {
		writeScript(t, binDir, "fd", `#!/bin/sh
if [ -n "$FDARGS" ]; then printf '%s\n' "$@" > "$FDARGS"; fi
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

	// TS bug: rg omits line_number for some inputs, and Number(undefined) is
	// NaN. A missing (or non-numeric) line number must be 0, not NaN.
	t.Run("missing line number is zero", func(t *testing.T) {
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = ""
		env["RGMOCK_ERR"] = ""
		body := `{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"héllo ✓\n"}}}` + "\n" +
			`{"type":"match","data":{"path":{"text":"b.txt"},"lines":{"text":"x\n"},"line_number":"5"}}` + "\n"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "h", Limit: 10})
		if err != nil {
			t.Fatalf("RgGrep: %v", err)
		}
		if len(result.Matches) != 2 {
			t.Fatalf("matches = %#v", result.Matches)
		}
		if result.Matches[0].Line != 0 {
			t.Errorf("missing line_number = %d, want 0", result.Matches[0].Line)
		}
		// Number.isFinite("5") is false too: only JSON numbers count.
		if result.Matches[1].Line != 0 {
			t.Errorf("string line_number = %d, want 0", result.Matches[1].Line)
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

// readMockArgv reads one argument per line from a recorded mock argv file.
func readMockArgv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// maxResultsArg returns the value that follows --max-results.
func maxResultsArg(t *testing.T, argv []string) string {
	t.Helper()
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == "--max-results" {
			return argv[index+1]
		}
	}
	t.Fatalf("no --max-results in argv %#v", argv)
	return ""
}

// TestRgFilesPassesJSNumberLimitToFd pins String(limit) exactly as TS hands it
// to fd. TS formats the JS number (2.5, 1e+21, Infinity), so fd rejects the
// value and rg/findWithNode takes over; a truncated Go int would change both
// the argv and the fallback.
func TestRgFilesPassesJSNumberLimitToFd(t *testing.T) {
	cases := []struct {
		name  string
		limit float64
		want  string
	}{
		{name: "integer", limit: 1000, want: "1000"},
		{name: "fractional", limit: 2.5, want: "2.5"},
		{name: "huge-exponent", limit: 1e21, want: "1e+21"},
		{name: "huge-fixed", limit: 1e19, want: "10000000000000000000"},
		{name: "infinity", limit: math.Inf(1), want: "Infinity"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := rgMocks(t, true)
			env := GetEnv(ctx)
			fdArgs := filepath.Join(t.TempDir(), "fd.args")
			rgArgs := filepath.Join(t.TempDir(), "rg.args")
			env["FDARGS"] = fdArgs
			env["RGARGS"] = rgArgs
			// fd rejects any non-integer --max-results, like the real binary.
			env["FDMOCK_EXIT"] = "2"
			if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte("one.ts\ntwo.ts\nthree.ts\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := RgFiles(ctx, t.TempDir(), "*.ts", testCase.limit, true)
			if err != nil {
				t.Fatalf("RgFiles: %v", err)
			}
			if value := maxResultsArg(t, readMockArgv(t, fdArgs)); value != testCase.want {
				t.Fatalf("fd --max-results = %q, want %q", value, testCase.want)
			}
			if len(readMockArgv(t, rgArgs)) == 0 {
				t.Fatal("rg was not used after fd rejected the limit")
			}
			wantLen := len([]string{"one.ts", "two.ts", "three.ts"})
			if testCase.limit == 2.5 {
				wantLen = 2 // slice(0, 2.5)
			}
			if len(got) != wantLen {
				t.Fatalf("RgFiles = %#v, want %d entries", got, wantLen)
			}
		})
	}
}

// TestRgFilesFdSuccessSlicesLikeJS: fd accepted the limit, and parsePathLines
// applies Array.prototype.slice(0, limit) semantics.
func TestRgFilesFdSuccessSlicesLikeJS(t *testing.T) {
	ctx := rgMocks(t, true)
	env := GetEnv(ctx)
	fdArgs := filepath.Join(t.TempDir(), "fd.args")
	env["FDARGS"] = fdArgs
	if err := os.WriteFile(mockPath(t, ctx, "FDMOCK"), []byte("a.ts\nb.ts\nc.ts\nd.ts\ne.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := RgFiles(ctx, t.TempDir(), "*.ts", 2.5, true)
	if err != nil {
		t.Fatalf("RgFiles: %v", err)
	}
	if value := maxResultsArg(t, readMockArgv(t, fdArgs)); value != "2.5" {
		t.Fatalf("fd --max-results = %q, want 2.5", value)
	}
	if strings.Join(got, ",") != "a.ts,b.ts" {
		t.Fatalf("RgFiles = %#v, want [a.ts b.ts]", got)
	}
}

// TestParsePathLinesJSSliceEnd pins slice(0, limit) for fractional, negative,
// NaN and infinite limits.
func TestParsePathLinesJSSliceEnd(t *testing.T) {
	stdout := "a\nb\nc\nd\ne\n"
	cases := []struct {
		limit float64
		want  string
	}{
		{limit: 2.5, want: "a,b"},
		{limit: 2, want: "a,b"},
		{limit: -1, want: "a,b,c,d"},
		{limit: math.NaN(), want: ""},
		{limit: math.Inf(1), want: "a,b,c,d,e"},
		{limit: math.Inf(-1), want: ""},
	}
	for _, testCase := range cases {
		got := strings.Join(parsePathLines(stdout, testCase.limit), ",")
		if got != testCase.want {
			t.Fatalf("parsePathLines(limit %v) = %q, want %q", testCase.limit, got, testCase.want)
		}
	}
}

// TestFindWithNodeLocaleOrder pins the TS fallback finder's localeCompare sort
// (mixed case differs from a byte-wise sort).
func TestFindWithNodeLocaleOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"B.ts", "a.ts", "C.ts", "b.ts", "null", "undefined"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := findWithNode(root, "*", 100, true)
	want := "a.ts,b.ts,B.ts,C.ts,null,undefined"
	if strings.Join(got, ",") != want {
		t.Fatalf("findWithNode = %#v, want %s", got, want)
	}
}

// TestRgGrepNaNLimitMatchesTS pins the TS rgGrep behaviour for a NaN limit:
// every record is parsed (matches.length > NaN is false), the slice is empty
// and truncation comes from the rg exit code alone.
func TestRgGrepNaNLimitMatchesTS(t *testing.T) {
	valid := `{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"one\n"},"line_number":1}}
{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"two\n"},"line_number":2}}
`
	t.Run("code 2 truncates", func(t *testing.T) {
		ctx := rgMocks(t, true)
		env := GetEnv(ctx)
		env["RGMOCK_EXIT"] = "2"
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: math.NaN()})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Matches) != 0 || !result.Truncated {
			t.Fatalf("RgGrep = %#v, want empty matches and truncated", result)
		}
	})
	t.Run("record after a match is still parsed", func(t *testing.T) {
		ctx := rgMocks(t, true)
		if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(valid+"not json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: math.NaN()})
		if err == nil || err.Error() != "Invalid ripgrep JSON output" {
			t.Fatalf("error = %v, want Invalid ripgrep JSON output", err)
		}
	})
}

// TestRgGrepFractionalLimit pins slice(0, limit) and truncated for a passed
// limit.
func TestRgGrepFractionalLimit(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"1\n"},"line_number":1}}`,
		`{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"2\n"},"line_number":2}}`,
		`{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"3\n"},"line_number":3}}`,
		`{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"4\n"},"line_number":4}}`,
	}, "\n") + "\n"
	ctx := rgMocks(t, true)
	if err := os.WriteFile(mockPath(t, ctx, "RGMOCK"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: 2.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || !result.Truncated {
		t.Fatalf("RgGrep(2.5) = %#v", result)
	}
	result, err = RgGrep(ctx, RgGrepInput{Cwd: t.TempDir(), Pattern: "x", Limit: math.Inf(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 4 || result.Truncated {
		t.Fatalf("RgGrep(Inf) = %#v", result)
	}
}

// TestReadLineWindowFractionalContext pins the TS readLineWindow loop: a
// fractional `current` produces a fractional line number and an empty text
// (lines[current - 1] is undefined); an integer start keeps real text.
func TestReadLineWindowFractionalContext(t *testing.T) {
	file := filepath.Join(t.TempDir(), "frac.txt")
	if err := os.WriteFile(file, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	window, err := ReadLineWindow(file, 3, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	want := []LineWindow{
		{Line: 1.5}, {Line: 2.5}, {Line: 3.5}, {Line: 4.5},
	}
	if len(window) != len(want) {
		t.Fatalf("window = %#v", window)
	}
	for index := range want {
		if window[index] != want[index] {
			t.Fatalf("window[%d] = %#v, want %#v", index, window[index], want[index])
		}
	}

	window, err = ReadLineWindow(file, 1, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	want = []LineWindow{{Line: 1, Text: "alpha", Match: true}, {Line: 2, Text: "beta"}}
	if len(window) != len(want) {
		t.Fatalf("window = %#v", window)
	}
	for index := range want {
		if window[index] != want[index] {
			t.Fatalf("window[%d] = %#v, want %#v", index, window[index], want[index])
		}
	}
}

// TestTruncateUTF16AstralCutLimitation documents the known deviation: TS
// String.prototype.slice can cut a surrogate pair and yield a lone high
// surrogate, which Go strings cannot hold, so utf16.Decode emits U+FFFD.
func TestTruncateUTF16AstralCutLimitation(t *testing.T) {
	input := strings.Repeat("a", 499) + "\U0001F600" + "b"
	got := truncateUTF16(input, 500)
	want := strings.Repeat("a", 499) + "\uFFFD"
	if got != want {
		t.Fatalf("truncateUTF16 = %q, want %q", got, want)
	}
	if !strings.Contains(got, "\uFFFD") {
		t.Fatal("expected the astral cut to produce U+FFFD in Go")
	}
}
