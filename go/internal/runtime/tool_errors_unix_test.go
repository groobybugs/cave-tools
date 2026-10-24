//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOwnerOnlyFiles(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	observeSoft(t, "cave__ls", "/nonexistent-perms")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 1 {
		t.Fatalf("total = %d, want 1", got)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, filepath.Join(dir, "cave-tools.db"), 0o600)
	for _, sidecar := range []string{"cave-tools.db-wal", "cave-tools.db-shm"} {
		path := filepath.Join(dir, sidecar)
		if _, err := os.Stat(path); err == nil {
			assertMode(t, path, 0o600)
		}
	}
}

func TestSymlinkDBRefused(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	target := filepath.Join(dir, "real-target.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "cave-tools.db")); err != nil {
		t.Fatal(err)
	}
	observeSoft(t, "cave__ls", "/nonexistent-sym")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("total = %d, want 0", got)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "x" {
		t.Errorf("symlink target touched: %q", content)
	}
}

func TestDanglingSymlinkTargetNeverCreated(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	ghost := filepath.Join(dir, "no-such-target.db")
	if err := os.Symlink(ghost, filepath.Join(dir, "cave-tools.db")); err != nil {
		t.Fatal(err)
	}
	observeSoft(t, "cave__ls", "/nonexistent-dangle")
	FlushToolErrorQueue()
	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
		t.Errorf("dangling target created: %v", err)
	}
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("total = %d, want 0", got)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}
