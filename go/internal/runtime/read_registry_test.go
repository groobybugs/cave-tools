package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Ports scripts/test-read-registry.mjs.

func registryTestCtx(t *testing.T) (context.Context, string) {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	ctx := w1bCtx(t, t.TempDir(), map[string]string{"CLAUDE_CONFIG_DIR": configDir})
	return ctx, filepath.Join(configDir, "cave-tools", "read-registry.txt")
}

func registryLines(body string) []string {
	lines := []string{}
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func TestRegisterReadPathOnce(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	file := filepath.Join(t.TempDir(), "target.txt")

	RegisterReadPathCompat(ctx, file)

	body, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("registry must exist when registration resolves: %v", err)
	}
	if string(body) != file+"\n" {
		t.Fatalf("registry = %q, want %q", body, file+"\n")
	}
	info, err := os.Stat(registry)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("registry mode = %04o, want 0600", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(registry))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("registry dir mode = %04o, want 0700", mode)
	}

	RegisterReadPathCompat(ctx, file)
	body, err = os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if lines := registryLines(string(body)); len(lines) != 1 || lines[0] != file {
		t.Fatalf("repeated registration duplicated the path: %#v", lines)
	}
}

func TestRegisterReadPathRejectsInvalid(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	base := t.TempDir()
	cases := map[string]string{
		"crlf injection":         base + "/safe\n" + base + "/injected",
		"carriage return":        base + "/safe\rx",
		"nul":                    base + "/safe\x00x",
		"relative":               "relative/path.txt",
		"empty":                  "",
		"surrounding whitespace": " " + base + "/padded ",
		"too long":               "/" + strings.Repeat("a", maxPathChars),
	}
	for name, value := range cases {
		RegisterReadPathCompat(ctx, value)
		if _, err := os.Stat(registry); err == nil {
			t.Fatalf("%s: registry must not be created for %q", name, value)
		}
	}
	if body, err := os.ReadFile(registry); err == nil && strings.Contains(string(body), "injected") {
		t.Fatalf("CR/LF path was written as a split line: %q", body)
	}
}

func TestRegisterReadPathPreservesExistingLines(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	root := t.TempDir()
	legacyA := filepath.Join(root, "legacy-a.txt")
	legacyB := filepath.Join(root, "legacy-b.txt")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	// No trailing newline: the new append must still start on its own line.
	if err := os.WriteFile(registry, []byte(legacyA+"\n"+legacyB), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(root, "new-file.txt")

	RegisterReadPathCompat(ctx, fresh)

	body, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	lines := registryLines(string(body))
	if len(lines) != 3 {
		t.Fatalf("lines = %#v, want 3", lines)
	}
	for _, want := range []string{legacyA, legacyB, fresh} {
		found := false
		for _, line := range lines {
			if line == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %q in %#v", want, lines)
		}
	}
}

func TestRegisterReadPathSeedTightensModes(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	root := t.TempDir()
	known := filepath.Join(root, "already-known.txt")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(known+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pre-existing permissive modes.
	if err := os.Chmod(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registry, 0o644); err != nil {
		t.Fatal(err)
	}

	RegisterReadPathCompat(ctx, known)

	body, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if lines := registryLines(string(body)); len(lines) != 1 || lines[0] != known {
		t.Fatalf("seeded known path was duplicated: %#v", lines)
	}
	info, _ := os.Stat(registry)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("seeded registry mode = %04o, want 0600", mode)
	}
	dirInfo, _ := os.Stat(filepath.Dir(registry))
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("seeded registry dir mode = %04o, want 0700", mode)
	}
}

func TestRegisterReadPathDoesNotTraverseSymlinkedDir(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	realDir := filepath.Join(root, "real-cave-tools")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(root, "known-in-symlink-dir.txt")
	realRegistry := filepath.Join(realDir, "read-registry.txt")
	if err := os.WriteFile(realRegistry, []byte(known+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(configDir, "cave-tools")); err != nil {
		t.Fatal(err)
	}
	ctx := w1bCtx(t, t.TempDir(), map[string]string{"CLAUDE_CONFIG_DIR": configDir})

	RegisterReadPathCompat(ctx, known)

	body, err := os.ReadFile(realRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != known+"\n" {
		t.Fatalf("symlinked final dir: target registry changed: %q", body)
	}
	linkInfo, err := os.Lstat(filepath.Join(configDir, "cave-tools"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlinked final dir must not be replaced: %v", err)
	}
	info, _ := os.Stat(realRegistry)
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Fatalf("target file must not be chmodded through the symlink: %04o", mode)
	}
}

func TestRegisterReadPathSeedIsBounded(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	root := t.TempDir()
	known := filepath.Join(root, "known-before-cap.txt")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	// Larger than maxSeedBytes; the known line sits inside the cap.
	body := known + "\n" + strings.Repeat("p", maxSeedBytes+4096)
	if err := os.WriteFile(registry, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	RegisterReadPathCompat(ctx, known)

	after, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(body) {
		t.Fatalf("oversized registry was appended to: %d bytes, want %d", len(after), len(body))
	}
	if !strings.HasPrefix(string(after), known+"\n") {
		t.Fatal("known path line changed")
	}
}

func TestRegisterReadPathRefusesSymlinkedFile(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	root := t.TempDir()
	target := filepath.Join(root, "sentinel.txt")
	if err := os.WriteFile(target, []byte("SENTINEL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, registry); err != nil {
		t.Fatal(err)
	}

	RegisterReadPathCompat(ctx, filepath.Join(root, "must-not-leak.txt"))

	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "SENTINEL\n" {
		t.Fatalf("symlink target was modified: %q", body)
	}
	info, err := os.Lstat(registry)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("registry symlink must not be replaced: %v", err)
	}
}

func TestRegisterReadPathSwallowsIOErrors(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	ctx := w1bCtx(t, t.TempDir(), map[string]string{"CLAUDE_CONFIG_DIR": configDir})
	// Make `cave-tools` a file so creating the registry directory fails.
	blocker := filepath.Join(configDir, "cave-tools")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "still-readable.txt")

	RegisterReadPathCompat(ctx, file)

	body, err := os.ReadFile(blocker)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "not a directory\n" {
		t.Fatalf("blocking file must be untouched: %q", body)
	}
}

func TestRegisterReadPathConcurrent(t *testing.T) {
	ctx, registry := registryTestCtx(t)
	root := t.TempDir()
	files := make([]string, 8)
	for index := range files {
		files[index] = filepath.Join(root, "concurrent-"+strconv.Itoa(index)+".txt")
	}

	var wait sync.WaitGroup
	for _, file := range files {
		wait.Add(1)
		go func(file string) {
			defer wait.Done()
			RegisterReadPathCompat(ctx, file)
		}(file)
	}
	wait.Wait()

	body, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	lines := registryLines(string(body))
	for _, file := range files {
		found := false
		for _, line := range lines {
			if line == file {
				found = true
			}
		}
		if !found {
			t.Fatalf("concurrent registration lost %s (lines=%#v)", file, lines)
		}
	}

	// Concurrent registrations of the same path coalesce into one line.
	same := filepath.Join(root, "same.txt")
	var sameWait sync.WaitGroup
	for index := 0; index < 8; index++ {
		sameWait.Add(1)
		go func() {
			defer sameWait.Done()
			RegisterReadPathCompat(ctx, same)
		}()
	}
	sameWait.Wait()
	body, err = os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range registryLines(string(body)) {
		if line == same {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("same path appended %d times, want 1", count)
	}
}

func TestIsRegistrablePath(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"absolute", filepath.Join(base, "a.txt"), true},
		{"absolute root", "/", true},
		{"empty", "", false},
		{"relative", "relative/path", false},
		{"newline", base + "/a\nb", false},
		{"carriage return", base + "/a\rb", false},
		{"nul", base + "/a\x00b", false},
		{"leading space", " " + base + "/a", false},
		{"trailing space", base + "/a ", false},
		// Muse regression: trim() strips U+FEFF but keeps U+0085.
		{"leading bom", "\ufeff" + base + "/a", false},
		{"trailing nel", base + "/a\u0085", true},
		{"leading nbsp", "\u00a0" + base + "/a", false},
		{"too long", "/" + strings.Repeat("a", maxPathChars), false},
		{"max length", "/" + strings.Repeat("a", maxPathChars-1), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isRegistrablePath(test.value); got != test.want {
				t.Fatalf("isRegistrablePath(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestOrderedSetEviction(t *testing.T) {
	set := newOrderedSet()
	for index := 0; index < maxKnownPaths+1; index++ {
		set.add("path-" + strconv.Itoa(index))
		if set.size() > maxKnownPaths {
			set.evictOldest()
		}
	}
	if set.size() != maxKnownPaths {
		t.Fatalf("size = %d, want %d", set.size(), maxKnownPaths)
	}
	if set.has("path-0") {
		t.Fatal("oldest entry was not evicted")
	}
	if !set.has("path-" + strconv.Itoa(maxKnownPaths)) {
		t.Fatal("newest entry missing")
	}

	// delete + re-add must behave like the JS Set (new insertion position).
	set = newOrderedSet()
	set.add("a")
	set.add("b")
	set.delete("a")
	set.add("a")
	set.evictOldest()
	if set.has("b") {
		t.Fatal("re-added key must sort after the older key")
	}
	if !set.has("a") {
		t.Fatal("re-added key must survive")
	}
}

func TestStateForCapsStates(t *testing.T) {
	registryStates.Lock()
	registryStates.byPath = map[string]*registryState{}
	registryStates.order = nil
	registryStates.Unlock()

	var first string
	for index := 0; index < maxStates+1; index++ {
		registry := filepath.Join(t.TempDir(), "read-registry.txt")
		if index == 0 {
			first = registry
		}
		stateFor(registry)
	}
	registryStates.Lock()
	defer registryStates.Unlock()
	if len(registryStates.byPath) != maxStates {
		t.Fatalf("states = %d, want %d", len(registryStates.byPath), maxStates)
	}
	if _, ok := registryStates.byPath[first]; ok {
		t.Fatal("oldest state was not evicted")
	}
}

// TestSeedKnownJSWhitespace is the Muse regression for the registry loader:
// TS trims each line with line.trim(), which strips U+FEFF and keeps U+0085.
func TestSeedKnownJSWhitespace(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "cave-tools", "read-registry.txt")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "\ufeff/tmp/a.ts\n/tmp/b.ts\u0085\n\u00a0/tmp/c.ts\u00a0\n"
	if err := os.WriteFile(registry, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	state := &registryState{known: newOrderedSet(), pending: map[string]*pendingAppend{}}
	seedKnown(state, registry)

	for _, want := range []string{"/tmp/a.ts", "/tmp/b.ts\u0085", "/tmp/c.ts"} {
		if !state.known.has(want) {
			t.Errorf("seedKnown did not load %q", want)
		}
	}
	for _, unwanted := range []string{"\ufeff/tmp/a.ts", "/tmp/b.ts", "\u00a0/tmp/c.ts\u00a0"} {
		if state.known.has(unwanted) {
			t.Errorf("seedKnown kept un-trimmed %q", unwanted)
		}
	}
}
