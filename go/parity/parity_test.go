//go:build parity

package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// outcome is one process run's observable result.
type outcome struct {
	stdout string
	stderr string
	code   int
}

// parityCase is one table row: identical argv/stdin/env for both binaries.
type parityCase struct {
	name  string
	args  []string
	stdin string
	env   map[string]string
	// setup names a shared fixture TestParity prepares before running the
	// case: "bins" puts fake rg/fd first on PATH with a temporary HOME;
	// "shared" points both sides at one data/config/runtime dir so outputs
	// that embed those paths stay byte-identical.
	setup string

	// goOnlyExtension marks a command the Go binary intentionally adds and
	// the TypeScript reference does not implement. It is not a waiver: both
	// sides are pinned to their own exact output (wantTS / wantGo), so a
	// change on either implementation fails the harness. The only member is
	// the Go-only `--version` probe; every other row must compare byte-for-byte.
	goOnlyExtension string
	wantTS          *outcome
	wantGo          *outcome
}

// tsUsageLine is what src/cli.ts prints for an unknown command, including
// --version (it has no version branch).
const tsUsageLine = "Usage: cave-tools [mcp|bench|install-agent|init|status [--emit-statusline|--verbose]|errors [--limit N]|jobs [kill <id> [signal]|log <id> [lines]]|bins|daemon [start|stop|status]|raw <command...>|filter --json [--session <id>]|help [tool]|<tool> [args...]|tool <tool> [args...]]"

// parityCases is the shipped table. Wave 4 grows it with one case per CLI
// command; wave 0 can only pin --version because the Go binary is a stub.
func parityCases(version string) []parityCase {
	return []parityCase{
		{
			name: "version",
			args: []string{"--version"},
			goOnlyExtension: "Go-only build probe: src/cli.ts has no --version branch, so the " +
				"TypeScript reference prints the usage line to stderr and exits 2. The Go binary " +
				"adds `cave-tools-go <version>` (exit 0) for build stamping; both outputs are pinned.",
			wantTS: &outcome{stdout: "", stderr: tsUsageLine + "\n", code: 2},
			wantGo: &outcome{stdout: "cave-tools-go " + version + "\n", stderr: "", code: 0},
		},
		{name: "help", args: []string{"help"}},
		{name: "help-read", args: []string{"help", "read"}},
		{name: "help-bash", args: []string{"help", "bash"}},
		{name: "help-unknown", args: []string{"help", "bogus"}},
		{name: "read", args: []string{"read", "README.md", "--limit", "5"}},
		{name: "read-session", args: []string{"read", "README.md", "--limit", "2", "--session", "abc"}},
		{name: "read-bad-flag", args: []string{"read", "README.md", "--bogus", "1"}},
		{name: "grep", args: []string{"grep", "Token", "README.md", "--limit", "3"}},
		{name: "bash", args: []string{"bash", "echo cli-parity"}},
		{name: "raw-exit", args: []string{"raw", "echo x; echo y >&2; exit 4"}},
		{name: "raw-quote", args: []string{"raw", "--", "printf", "%s|", "a b"}},
		{name: "raw-signal", args: []string{"raw", "kill -TERM $$"}},
		{name: "raw-stdin", args: []string{"raw", "cat"}, stdin: "piped\n"},
		{name: "raw-usage", args: []string{"raw"}},
		{name: "filter-json", args: []string{"filter", "--json"}, stdin: `{"command":"make","stdout":"ok\n","stderr":"token=ghp_abcdefghijklmnopqrstuvwxyz0123456789","exitCode":0}`},
		{name: "filter-missing-fields", args: []string{"filter", "--json"}, stdin: `{"stdout":"x"}`},
		{name: "filter-no-json", args: []string{"filter"}, stdin: `{}`},
		{name: "status", args: []string{"status", "--verbose"}, env: map[string]string{"CAVE_TOOLS_STATUS_RTK": "0", "CAVE_TOOLS_ASCII": "1"}},
		{name: "status-emit-statusline", args: []string{"status", "--emit-statusline"}, env: map[string]string{"CAVE_TOOLS_STATUS_RTK": "0"}},
		{name: "jobs", args: []string{"jobs"}},
		{name: "jobs-usage", args: []string{"jobs", "kill"}},
		{name: "jobs-unknown", args: []string{"jobs", "log", "nope"}},
		{name: "bins", args: []string{"bins"}, setup: "bins"},
		{name: "daemon-status", args: []string{"daemon", "status"}, setup: "shared"},
		{name: "daemon-stop", args: []string{"daemon", "stop"}, setup: "shared"},
		{name: "daemon-usage", args: []string{"daemon", "bogus"}},
		{name: "errors", args: []string{"errors"}, setup: "shared"},
		{name: "errors-bad-limit", args: []string{"errors", "--limit", "abc"}, setup: "shared"},
		{name: "bench", args: []string{"bench"}},
		{name: "tool-unknown", args: []string{"tool"}},
		{name: "unknown-command", args: []string{"bogus"}},
	}
}

// parityFixtures holds the shared directories CLI cases may need.
type parityFixtures struct {
	binDir string
	home   string
	shared string
}

// prepareFixtures creates fake rg/fd executables and the shared data/config/
// runtime tree used by cases whose output embeds those paths.
func prepareFixtures(t *testing.T) parityFixtures {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	shared := filepath.Join(root, "shared")
	for _, dir := range []string{binDir, home, filepath.Join(shared, "data"), filepath.Join(shared, "claude"), filepath.Join(shared, "run")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	script := []byte("#!/bin/sh\nexit 0\n")
	for _, name := range []string{"rg", "fd"} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, script, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return parityFixtures{binDir: binDir, home: home, shared: shared}
}

// mergeEnv copies base and applies extra on top.
func mergeEnv(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func TestParity(t *testing.T) {
	root := repoRoot(t)

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; parity harness needs node")
	}
	distCLI := filepath.Join(root, "dist", "cli.js")
	if _, err := os.Stat(distCLI); err != nil {
		t.Fatalf("dist/cli.js is missing: run `pnpm build` first (%v)", err)
	}
	version := packageVersion(t, root)
	goBinary := buildGoBinary(t, root, version)

	fixtures := prepareFixtures(t)
	cases := parityCases(version)
	for i := range cases {
		switch cases[i].setup {
		case "bins":
			cases[i].env = mergeEnv(cases[i].env, map[string]string{
				"PATH": fixtures.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME": fixtures.home,
			})
		case "shared":
			cases[i].env = mergeEnv(cases[i].env, map[string]string{
				"CAVE_TOOLS_DATA_DIR": filepath.Join(fixtures.shared, "data"),
				"CLAUDE_CONFIG_DIR":   filepath.Join(fixtures.shared, "claude"),
				"XDG_RUNTIME_DIR":     filepath.Join(fixtures.shared, "run"),
			})
		}
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsDir := t.TempDir()
			goDir := t.TempDir()

			tsResult := runProcess(t, nodePath, []string{distCLI}, testCase, tsDir)
			goResult := runProcess(t, goBinary, nil, testCase, goDir)

			if testCase.goOnlyExtension != "" {
				t.Logf("go-only extension: %s", testCase.goOnlyExtension)
				if testCase.wantTS == nil || testCase.wantGo == nil {
					t.Fatalf("case %q declares a Go-only extension without pinned expectations", testCase.name)
				}
				assertOutcome(t, "TS", tsResult, *testCase.wantTS)
				assertOutcome(t, "Go", goResult, *testCase.wantGo)
				return
			}

			if differences := diffOutcome(tsResult, goResult); len(differences) > 0 {
				t.Fatalf("TS and Go disagree on %v:\n%s", testCase.args, strings.Join(differences, "\n"))
			}
		})
	}
}

// TestDiffOutcome covers the comparator so a green parity run cannot hide a
// broken comparison.
func TestDiffOutcome(t *testing.T) {
	base := outcome{stdout: "out\n", stderr: "err\n", code: 0}
	tests := []struct {
		name string
		got  outcome
		want int
	}{
		{name: "identical", got: base, want: 0},
		{name: "stdout", got: outcome{stdout: "other\n", stderr: "err\n"}, want: 1},
		{name: "stderr", got: outcome{stdout: "out\n", stderr: "other\n"}, want: 1},
		{name: "exit code", got: outcome{stdout: "out\n", stderr: "err\n", code: 2}, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			differences := diffOutcome(base, test.got)
			if len(differences) != test.want {
				t.Fatalf("diffOutcome = %v, want %d difference(s)", differences, test.want)
			}
		})
	}
}

// diffOutcome lists the observable differences between TS and Go.
func diffOutcome(ts, goResult outcome) []string {
	var differences []string
	if ts.stdout != goResult.stdout {
		differences = append(differences, fmt.Sprintf("stdout: ts=%q go=%q", ts.stdout, goResult.stdout))
	}
	if ts.stderr != goResult.stderr {
		differences = append(differences, fmt.Sprintf("stderr: ts=%q go=%q", ts.stderr, goResult.stderr))
	}
	if ts.code != goResult.code {
		differences = append(differences, fmt.Sprintf("exit code: ts=%d go=%d", ts.code, goResult.code))
	}
	return differences
}

// assertOutcome checks one side against its pinned expectation.
func assertOutcome(t *testing.T, label string, got, want outcome) {
	t.Helper()
	if differences := diffOutcome(want, got); len(differences) > 0 {
		t.Fatalf("%s output drifted from the pinned expectation:\n%s", label, strings.Join(differences, "\n"))
	}
}

// runProcess runs binary with the case's argv/stdin/env and a private data dir.
func runProcess(t *testing.T, binary string, prefix []string, testCase parityCase, dataDir string) outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := append(append([]string{}, prefix...), testCase.args...)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = repoRoot(t)
	cmd.Stdin = strings.NewReader(testCase.stdin)
	cmd.Env = isolatedEnv(dataDir, testCase.env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s %v: %v", binary, args, err)
		}
		code = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out: %v", binary, args, ctx.Err())
	}
	return outcome{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// isolatedEnv gives each side its own temp data dir, a private Claude config
// dir and no daemon, so runs never read or write the developer's state.
func isolatedEnv(dataDir string, extra map[string]string) []string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	env["CAVE_TOOLS_DATA_DIR"] = dataDir
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(dataDir, "claude")
	env["CAVE_TOOLS_DAEMON"] = "0"
	for key, value := range extra {
		env[key] = value
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	flattened := make([]string, 0, len(keys))
	for _, key := range keys {
		flattened = append(flattened, key+"="+env[key])
	}
	return flattened
}

// buildGoBinary builds the Go CLI with the package.json version stamped in.
func buildGoBinary(t *testing.T, root, version string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "cave-tools-go")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", binary, "./cmd/cave-tools-go")
	cmd.Dir = filepath.Join(root, "go")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

// packageVersion reads the version both implementations must agree on.
func packageVersion(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if pkg.Version == "" {
		t.Fatal("package.json has no version")
	}
	return pkg.Version
}

// repoRoot is go/parity -> repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}
