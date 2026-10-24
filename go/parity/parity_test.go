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

	// knownDivergence, when set, explains why the two implementations cannot
	// be compared byte-for-byte yet. In that mode wantTS / wantGo pin each
	// side's expected output instead, so a change on either side still fails
	// the harness. Remove it once the divergence is gone.
	knownDivergence string
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
			knownDivergence: "src/cli.ts has no --version branch, so node dist/cli.js --version " +
				"prints the usage line to stderr and exits 2. The Go stub prints the package.json " +
				"version and exits 0 (W0 requirement). This case pins both sides until W3 ports the " +
				"CLI, then it becomes a strict comparison.",
			wantTS: &outcome{stdout: "", stderr: tsUsageLine + "\n", code: 2},
			wantGo: &outcome{stdout: "cave-tools-go " + version + "\n", stderr: "", code: 0},
		},
	}
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

	for _, testCase := range parityCases(version) {
		t.Run(testCase.name, func(t *testing.T) {
			tsDir := t.TempDir()
			goDir := t.TempDir()

			tsResult := runProcess(t, nodePath, []string{distCLI}, testCase, tsDir)
			goResult := runProcess(t, goBinary, nil, testCase, goDir)

			if testCase.knownDivergence != "" {
				t.Logf("known divergence: %s", testCase.knownDivergence)
				if testCase.wantTS == nil || testCase.wantGo == nil {
					t.Fatalf("case %q declares a divergence without pinned expectations", testCase.name)
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
