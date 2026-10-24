//go:build parity

// Twin-sandbox harness for black-box CLI differentials.
//
// Each side gets an identical fixture tree and its own data/home/runtime
// directories. Every command runs with the fixture root as cwd, then stdout,
// stderr and the exit code are compared after replacing the side's absolute
// roots with placeholders. The fixture tree (and the Claude config tree, which
// holds the statusline suffix) is snapshotted after the last step, so side
// effects are compared too.
package parity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureFile is one file or symlink in a sandbox fixture.
type fixtureFile struct {
	Content string
	// Mode defaults to 0o644 for files and 0o755 for directories.
	Mode fs.FileMode
	// Symlink, when non-empty, replaces the file with a symlink to this target.
	Symlink string
}

// fixtureSpec is the shared tree both sides start from.
type fixtureSpec struct {
	Files map[string]fixtureFile
	Dirs  []string
}

// baseFixture is the small tree most CLI cases use.
func baseFixture() fixtureSpec {
	return fixtureSpec{
		Files: map[string]fixtureFile{
			"note.txt": {
				Content: "alpha\nbeta token\nGamma 42\n",
			},
			"code.ts": {
				Content: "export const x = 1;\nexport function f(a) {\n  return a;\n}\n// comment\nexport async function g() {}\n",
			},
			"lines.txt": {
				Content: "one\ntwo\nthree\nfour\nfive\n",
			},
			"crlf.txt": {
				Content: "first\r\nsecond\r\n",
			},
			"nonl.txt": {
				Content: "no newline at end",
			},
			"unicode.txt": {
				Content: "héllo wörld\n日本\n",
			},
			"sub/a.txt": {
				Content: "needle one\nother\nneedle two\n",
			},
			"sub/b.md": {
				Content: "markdown needle\n",
			},
			"binary.bin": {
				Content: "a\x00b\n",
			},
			"img.png": {
				Content: "\x89PNG\r\n\x1a\n" + strings.Repeat("0", 64),
			},
			"doc.pdf": {
				Content: "%PDF-1.4\nfake pdf\n",
			},
			"empty.txt": {
				Content: "",
			},
		},
		Dirs: []string{"emptydir", "existingdir"},
	}
}

// side is one implementation's private sandbox.
type side struct {
	label  string
	binary string
	prefix []string
	root   string
	data   string
	home   string
	runDir string
	claude string
}

// newSide creates a sandbox for one implementation and seeds fixture into root.
func newSide(t *testing.T, label, binary string, prefix []string, fixture fixtureSpec) *side {
	t.Helper()
	base := t.TempDir()
	paths := map[string]string{
		"root":   filepath.Join(base, "root"),
		"data":   filepath.Join(base, "data"),
		"home":   filepath.Join(base, "home"),
		"run":    filepath.Join(base, "run"),
		"claude": filepath.Join(base, "claude"),
	}
	for _, dir := range paths {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	resolved := make(map[string]string, len(paths))
	for key, dir := range paths {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir
		}
		resolved[key] = real
	}
	seedFixture(t, resolved["root"], fixture)
	return &side{
		label:  label,
		binary: binary,
		prefix: prefix,
		root:   resolved["root"],
		data:   resolved["data"],
		home:   resolved["home"],
		runDir: resolved["run"],
		claude: resolved["claude"],
	}
}

// seedFixture writes the fixture tree below root.
func seedFixture(t *testing.T, root string, fixture fixtureSpec) {
	t.Helper()
	for _, dir := range fixture.Dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir fixture %s: %v", dir, err)
		}
	}
	names := make([]string, 0, len(fixture.Files))
	for name := range fixture.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := fixture.Files[name]
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if spec.Symlink != "" {
			if err := os.Symlink(spec.Symlink, full); err != nil {
				t.Fatalf("symlink %s: %v", name, err)
			}
			continue
		}
		mode := spec.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(full, []byte(spec.Content), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatalf("chmod %s: %v", name, err)
		}
	}
}

// cleanEnv returns an environment without the developer's CAVE_*/agent
// variables, so the harness cannot inherit a forced-fresh or daemon setting.
func cleanEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"PATH":    os.Getenv("PATH"),
		"TMPDIR":  os.Getenv("TMPDIR"),
		"LANG":    "C.UTF-8",
		"LC_ALL":  "C.UTF-8",
		"TZ":      "UTC",
		"SHELL":   "/bin/sh",
		"USER":    os.Getenv("USER"),
		"LOGNAME": os.Getenv("LOGNAME"),
	}
	for _, key := range []string{"SystemRoot", "ComSpec", "PATHEXT", "USERPROFILE", "WINDIR"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// baseEnv is the isolated environment for one side. The suite sets
// CAVE_TOOLS_DAEMON=0 unless a test opts into the daemon.
func (s *side) baseEnv(extra map[string]string) map[string]string {
	merged := map[string]string{
		"CAVE_TOOLS_DATA_DIR":   s.data,
		"CLAUDE_CONFIG_DIR":     s.claude,
		"XDG_RUNTIME_DIR":       s.runDir,
		"HOME":                  s.home,
		"CAVE_TOOLS_DAEMON":     "0",
		"CAVE_TOOLS_STATUS_RTK": "0",
		"CAVE_TOOLS_ERROR_LOG":  "1",
	}
	for key, value := range extra {
		merged[key] = value
	}
	return cleanEnv(merged)
}

// runCommandOutcome runs binary+prefix with argv in cwd and a merged env.
func runCommandOutcome(t *testing.T, binary string, prefix []string, cwd string, env map[string]string, stdin string, args ...string) outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	full := append(append([]string{}, prefix...), args...)
	cmd := exec.CommandContext(ctx, binary, full...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = flattenEnv(env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s %v: %v", binary, full, err)
		}
		code = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out: %v", binary, full, ctx.Err())
	}
	return outcome{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// flattenEnv sorts an env map into KEY=VALUE entries.
func flattenEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	flat := make([]string, 0, len(keys))
	for _, key := range keys {
		flat = append(flat, key+"="+env[key])
	}
	return flat
}

// run executes one CLI step in the side's fixture root.
func (s *side) run(t *testing.T, args []string, stdin string, extraEnv map[string]string) outcome {
	t.Helper()
	return runCommandOutcome(t, s.binary, s.prefix, s.root, s.baseEnv(extraEnv), stdin, args...)
}

// normalize replaces implementation-private absolute paths with placeholders.
func (s *side) normalize(text string) string {
	replacements := []string{
		s.claude, "<CLAUDE>",
		s.data, "<DATA>",
		s.home, "<HOME>",
		s.runDir, "<RUN>",
		s.root, "<ROOT>",
	}
	return strings.NewReplacer(replacements...).Replace(text)
}

// treeEntry is one filesystem entry in a side-effect snapshot.
type treeEntry struct {
	Kind   string
	Mode   fs.FileMode
	Hash   string
	Target string
}

// snapshotTree captures relative path, type, permission bits and content hash.
// normalize (optional) is applied to regular-file content before hashing, for
// trees whose contents legitimately embed the side's private fixture root.
func snapshotTree(t *testing.T, root string, normalize func(string) string) map[string]treeEntry {
	t.Helper()
	entries := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := treeEntry{Mode: info.Mode().Perm()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.Kind = "symlink"
			item.Target = target
		case info.IsDir():
			item.Kind = "dir"
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if normalize != nil {
				content = []byte(normalize(string(content)))
			}
			sum := sha256.Sum256(content)
			item.Kind = "file"
			item.Hash = hex.EncodeToString(sum[:])
		default:
			item.Kind = "other"
		}
		entries[filepath.ToSlash(rel)] = item
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return entries
}

// formatTreeDiff renders the first differences between two snapshots.
func formatTreeDiff(want, got map[string]treeEntry) string {
	var lines []string
	keys := make([]string, 0, len(want)+len(got))
	for key := range want {
		keys = append(keys, key)
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		wantEntry, wantOK := want[key]
		gotEntry, gotOK := got[key]
		switch {
		case !wantOK:
			lines = append(lines, fmt.Sprintf("extra %s (%s mode=%o)", key, gotEntry.Kind, gotEntry.Mode))
		case !gotOK:
			lines = append(lines, fmt.Sprintf("missing %s (%s mode=%o)", key, wantEntry.Kind, wantEntry.Mode))
		case !reflect.DeepEqual(wantEntry, gotEntry):
			lines = append(lines, fmt.Sprintf("%s: ts=%#v go=%#v", key, wantEntry, gotEntry))
		}
		if len(lines) == 20 {
			lines = append(lines, "...")
			break
		}
	}
	return strings.Join(lines, "\n")
}

// cliStep is one invocation in a black-box case.
type cliStep struct {
	args  []string
	stdin string
	env   map[string]string
	// pause runs before the step. The error log is documented as best-effort
	// under SQLite contention, so back-to-back failing CLI calls pause briefly
	// instead of manufacturing an artificial busy race.
	pause time.Duration
	// setup runs on each side before the step (for example to seed a job).
	setup func(t *testing.T, s *side)
}

// cliCase compares a sequence of CLI invocations and the final trees.
type cliCase struct {
	name    string
	fixture fixtureSpec
	steps   []cliStep
	// requirePOSIX skips shell/POSIX-only cases on Windows.
	requirePOSIX bool
	// skipTree disables the side-effect tree comparison for cases whose
	// output depends on timestamps or external state.
	skipTree bool
	// normalize smooths a known-nondeterministic field (timestamps, UUIDs,
	// generated daemon keys) without hiding the surrounding text. Every use
	// must come with an independent assertion in the case's test.
	normalize func(string) string
	// verify runs after the transcript/tree comparison for independent
	// invariants a normalization could otherwise hide.
	verify func(t *testing.T, ts, goSide *side)
}

// runTranscript runs every step on one side and renders the observable result.
func runTranscript(t *testing.T, s *side, steps []cliStep, normalize func(string) string) string {
	t.Helper()
	clean := func(text string) string {
		text = s.normalize(text)
		if normalize != nil {
			text = normalize(text)
		}
		return text
	}
	var transcript strings.Builder
	for index, step := range steps {
		if step.setup != nil {
			step.setup(t, s)
		}
		if step.pause > 0 {
			time.Sleep(step.pause)
		}
		result := s.run(t, step.args, step.stdin, step.env)
		fmt.Fprintf(&transcript, "=== step %d: %q\n", index+1, step.args)
		if step.stdin != "" {
			fmt.Fprintf(&transcript, "stdin: %q\n", step.stdin)
		}
		fmt.Fprintf(&transcript, "exit: %d\nstdout:\n%s\nstderr:\n%s\n", result.code, clean(result.stdout), clean(result.stderr))
	}
	return transcript.String()
}

// runCLICase is the shared table runner used by the CLI differential suites.
func runCLICase(t *testing.T, testCase cliCase, tsBinary string, tsPrefix []string, goBinary string) {
	t.Helper()
	if testCase.requirePOSIX && runtime.GOOS == "windows" {
		t.Skip("case uses POSIX shell behaviour")
	}
	fixture := testCase.fixture
	if fixture.Files == nil && fixture.Dirs == nil {
		fixture = baseFixture()
	}
	tsSide := newSide(t, "ts", tsBinary, tsPrefix, fixture)
	goSide := newSide(t, "go", goBinary, nil, fixture)

	tsTranscript := runTranscript(t, tsSide, testCase.steps, testCase.normalize)
	goTranscript := runTranscript(t, goSide, testCase.steps, testCase.normalize)
	if tsTranscript != goTranscript {
		t.Fatalf("TS and Go transcripts differ\n--- TS ---\n%s\n--- Go ---\n%s", tsTranscript, goTranscript)
	}
	if testCase.skipTree {
		return
	}
	tsTree := snapshotTree(t, tsSide.root, nil)
	goTree := snapshotTree(t, goSide.root, nil)
	if !reflect.DeepEqual(tsTree, goTree) {
		t.Fatalf("fixture side effects differ:\n%s", formatTreeDiff(tsTree, goTree))
	}
	tsClaude := snapshotTree(t, tsSide.claude, tsSide.normalize)
	goClaude := snapshotTree(t, goSide.claude, goSide.normalize)
	if !reflect.DeepEqual(tsClaude, goClaude) {
		t.Fatalf("claude config side effects differ:\n%s", formatTreeDiff(tsClaude, goClaude))
	}
	if testCase.verify != nil {
		testCase.verify(t, tsSide, goSide)
	}
}

var (
	goBinaryOnce sync.Once
	goBinaryPath string
	goBinaryErr  error
)

// sharedGoBinary builds the stamping Go binary once per test process.
func sharedGoBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	version := packageVersion(t, root)
	goBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cave-tools-go-parity-")
		if err != nil {
			goBinaryErr = err
			return
		}
		binary := filepath.Join(dir, "cave-tools-go")
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", binary, "./cmd/cave-tools-go")
		cmd.Dir = filepath.Join(root, "go")
		if output, err := cmd.CombinedOutput(); err != nil {
			goBinaryErr = fmt.Errorf("go build: %w\n%s", err, output)
			return
		}
		goBinaryPath = binary
	})
	if goBinaryErr != nil {
		t.Fatalf("%v", goBinaryErr)
	}
	return goBinaryPath
}

// typescriptCLI returns node and the dist/cli.js prefix, skipping when the
// reference build is missing.
func typescriptCLI(t *testing.T) (string, []string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; parity harness needs node")
	}
	root := repoRoot(t)
	cli := filepath.Join(root, "dist", "cli.js")
	if _, err := os.Stat(cli); err != nil {
		t.Fatalf("dist/cli.js is missing: run `pnpm build` first (%v)", err)
	}
	return node, []string{cli}
}
