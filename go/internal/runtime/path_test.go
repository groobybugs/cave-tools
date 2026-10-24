package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w1bCtx builds a CallContext for a test with an explicit cwd and env overrides.
func w1bCtx(t *testing.T, cwd string, extra map[string]string) context.Context {
	t.Helper()
	env := EnvFromEnviron(os.Environ())
	for key, value := range extra {
		env[key] = value
	}
	call := &CallContext{SessionID: "w1b-test", Cwd: cwd, Env: env, ClientID: "w1b"}
	return context.WithValue(context.Background(), contextKey{}, call)
}

// w1bWait blocks until ch is closed or the test fails after a stuck operation.
func w1bWait(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not finish in time", label)
	}
}

func TestToPosixPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// TS: value.replace(/\\/g, "/")
		{"backslashes", `a\b\c`, "a/b/c"},
		{"already posix", "a/b/c", "a/b/c"},
		{"mixed", `a\b/c`, "a/b/c"},
		{"empty", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ToPosixPath(test.input); got != test.want {
				t.Fatalf("ToPosixPath(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestContainsPath(t *testing.T) {
	base := t.TempDir()
	sibling := base + "-sibling"
	tests := []struct {
		name   string
		parent string
		child  string
		want   bool
	}{
		{"same path", base, base, true},
		{"direct child", base, filepath.Join(base, "file.txt"), true},
		{"nested child", base, filepath.Join(base, "a", "b", "c.txt"), true},
		{"dot cleanup", filepath.Join(base, "a"), filepath.Join(base, "a", ".", "b"), true},
		{"parent", base, filepath.Dir(base), false},
		{"sibling prefix", base, sibling, false},
		{"unrelated", base, "/definitely/elsewhere", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ContainsPath(test.parent, test.child); got != test.want {
				t.Fatalf("ContainsPath(%q, %q) = %v, want %v", test.parent, test.child, got, test.want)
			}
		})
	}
}

func TestRealPathSafe(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	ctx := w1bCtx(t, cwd, nil)
	got, err := RealPathSafe(ctx, link)
	if err != nil {
		t.Fatalf("RealPathSafe(existing symlink): %v", err)
	}
	wantTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantTarget {
		t.Fatalf("RealPathSafe(symlink) = %q, want %q", got, wantTarget)
	}

	got, err = RealPathSafe(ctx, "missing.txt")
	if err != nil {
		t.Fatalf("RealPathSafe(missing): %v", err)
	}
	if want := filepath.Join(cwd, "missing.txt"); got != want {
		t.Fatalf("RealPathSafe(missing) = %q, want %q", got, want)
	}

	// Relative input resolves against the call cwd before realpath, so a
	// relative symlink under the call cwd is resolved to its target.
	got, err = RealPathSafe(ctx, "link.txt")
	if err != nil {
		t.Fatalf("RealPathSafe(relative): %v", err)
	}
	if got != wantTarget {
		t.Fatalf("RealPathSafe(relative) = %q, want %q", got, wantTarget)
	}

	// A relative path that exists only under the process cwd (not the call
	// cwd) must not leak the server's answer: ENOENT falls back to the call
	// cwd path.
	processOnly := filepath.Join(cwd, "process-only.txt")
	if err := os.WriteFile(processOnly, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	otherCwd := t.TempDir()
	got, err = RealPathSafe(w1bCtx(t, otherCwd, nil), "process-only.txt")
	if err != nil {
		t.Fatalf("RealPathSafe(server-only relative): %v", err)
	}
	if want := filepath.Join(otherCwd, "process-only.txt"); got != want {
		t.Fatalf("RealPathSafe(server-only relative) = %q, want %q", got, want)
	}
}

func TestResolveExistingDirectory(t *testing.T) {
	cwd := t.TempDir()
	real := filepath.Join(cwd, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cwd, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := w1bCtx(t, cwd, nil)

	got, err := ResolveExistingDirectory(ctx, "")
	if err != nil {
		t.Fatalf("ResolveExistingDirectory(\"\"): %v", err)
	}
	if wantReal, _ := filepath.EvalSymlinks(cwd); got != wantReal {
		t.Fatalf("ResolveExistingDirectory(\"\") = %q, want %q", got, wantReal)
	}

	got, err = ResolveExistingDirectory(ctx, "link")
	if err != nil {
		t.Fatalf("ResolveExistingDirectory(link): %v", err)
	}
	if wantReal, _ := filepath.EvalSymlinks(real); got != wantReal {
		t.Fatalf("ResolveExistingDirectory(link) = %q, want %q", got, wantReal)
	}

	if _, err := ResolveExistingDirectory(ctx, file); err == nil ||
		!strings.HasPrefix(err.Error(), "Not a directory: ") {
		t.Fatalf("ResolveExistingDirectory(file) error = %v, want Not a directory", err)
	}
	if _, err := ResolveExistingDirectory(ctx, "missing"); err == nil {
		t.Fatal("ResolveExistingDirectory(missing) must fail like fs.stat")
	}
}

func TestEnsureParentDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c", "file.txt")
	if err := EnsureParentDirectory(target); err != nil {
		t.Fatalf("EnsureParentDirectory: %v", err)
	}
	info, err := os.Stat(filepath.Dir(target))
	if err != nil || !info.IsDir() {
		t.Fatalf("parent directory not created: %v", err)
	}
}

func TestResolveMutationTarget(t *testing.T) {
	cwd := t.TempDir()
	realDir := filepath.Join(cwd, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(realDir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(cwd, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	ctx := w1bCtx(t, cwd, nil)

	t.Run("existing through symlink", func(t *testing.T) {
		target, err := ResolveMutationTarget(ctx, filepath.Join("alias", "file.txt"))
		if err != nil {
			t.Fatal(err)
		}
		wantCanonical, _ := filepath.EvalSymlinks(file)
		if target.Canonical != wantCanonical || !target.Existed {
			t.Fatalf("ResolveMutationTarget = %#v, want {%q true}", target, wantCanonical)
		}
	})

	t.Run("missing in existing dir", func(t *testing.T) {
		target, err := ResolveMutationTarget(ctx, filepath.Join("alias", "new.txt"))
		if err != nil {
			t.Fatal(err)
		}
		wantDir, _ := filepath.EvalSymlinks(realDir)
		want := filepath.Join(wantDir, "new.txt")
		if target.Canonical != want || target.Existed {
			t.Fatalf("ResolveMutationTarget = %#v, want {%q false}", target, want)
		}
	})

	t.Run("missing parents", func(t *testing.T) {
		target, err := ResolveMutationTarget(ctx, filepath.Join("missing", "deep", "new.txt"))
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(cwd, "missing", "deep", "new.txt")
		if target.Canonical != want || target.Existed {
			t.Fatalf("ResolveMutationTarget = %#v, want {%q false}", target, want)
		}
	})
}
