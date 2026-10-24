package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// isolate points every path input at a temp dir and pins the clock-independent
// pieces (build id still comes from the real dist, read-only).
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("CAVE_TOOLS_DATA_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", t.TempDir())
}

func requireDist(t *testing.T) string {
	t.Helper()
	build, err := BuildID()
	if err != nil {
		t.Skipf("dist/ not built: %v (run `pnpm build` first)", err)
	}
	if len(build) != 16 {
		t.Fatalf("BuildID() = %q, want 16 hex chars", build)
	}
	return build
}

// Ported from test.mjs "socket dir 0700, socket 0600" and the socket naming
// assertions in the daemon block.
func TestSocketPathsUseRuntimeDir(t *testing.T) {
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	key, err := DaemonKey()
	if err != nil {
		t.Fatalf("DaemonKey: %v", err)
	}
	if len(key) != 12 {
		t.Fatalf("DaemonKey() = %q, want 12 hex chars", key)
	}

	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	if want := filepath.Join(runtimeDir, "cave-tools"); dir != want {
		t.Fatalf("SocketDir() = %q, want %q", dir, want)
	}

	path, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if want := filepath.Join(dir, "daemon-"+key+".sock"); path != want {
		t.Fatalf("SocketPath() = %q, want %q", path, want)
	}

	lock, err := LockPath()
	if err != nil {
		t.Fatalf("LockPath: %v", err)
	}
	if want := filepath.Join(dir, "daemon-"+key+".lock"); lock != want {
		t.Fatalf("LockPath() = %q, want %q", lock, want)
	}
}

func TestSocketPathsFallBackToDataDir(t *testing.T) {
	isolate(t)
	requireDist(t)
	dataDir := os.Getenv("CAVE_TOOLS_DATA_DIR")

	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	if want := filepath.Join(dataDir, "run"); dir != want {
		t.Fatalf("SocketDir() = %q, want %q", dir, want)
	}

	key, err := DaemonKey()
	if err != nil {
		t.Fatalf("DaemonKey: %v", err)
	}
	path, err := SocketPath(key)
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if want := filepath.Join(dir, "daemon-"+key+".sock"); path != want {
		t.Fatalf("SocketPath(key) = %q, want %q", path, want)
	}
}

// port of src/daemon/paths.ts:socketDir fallback branch (>100 char socket).
func TestSocketDirFallsBackToTmp(t *testing.T) {
	isolate(t)
	requireDist(t)
	t.Setenv("XDG_RUNTIME_DIR", strings.Repeat("x", 80))
	tmp := os.Getenv("TMPDIR")
	key, err := DaemonKey()
	if err != nil {
		t.Fatalf("DaemonKey: %v", err)
	}
	uid := "user"
	if id, ok := currentUID(); ok {
		uid = strconv.Itoa(id)
	}
	dir, err := SocketDir(key)
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	if jsLen(tmp) >= 40 {
		tmp = "/tmp"
	}
	if want := filepath.Join(tmp, "cave-tools-"+uid); dir != want {
		t.Fatalf("SocketDir() = %q, want %q", dir, want)
	}
}

func TestSocketDirASCIIWhitespaceTrim(t *testing.T) {
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	// JS trim removes \t and friends, unlike strings.TrimSpace's wider set.
	t.Setenv("XDG_RUNTIME_DIR", "\t "+runtimeDir+" \n")
	dir, err := SocketDir("k")
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	if want := filepath.Join(runtimeDir, "cave-tools"); dir != want {
		t.Fatalf("SocketDir() = %q, want %q", dir, want)
	}
}

func TestSocketDirTrimsECMAScriptWhitespace(t *testing.T) {
	isolate(t)
	requireDist(t)
	t.Setenv("XDG_RUNTIME_DIR", "\u00a0")
	dir, err := SocketDir("k")
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	// U+00A0 is ECMAScript whitespace, so it trims to "" and the data dir wins.
	if want := filepath.Join(os.Getenv("CAVE_TOOLS_DATA_DIR"), "run"); dir != want {
		t.Fatalf("SocketDir() = %q, want %q", dir, want)
	}
}

// port of test.mjs: socket dir 0700.
func TestEnsureSocketDirCreates0700(t *testing.T) {
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	dir, err := EnsureSocketDir("abc")
	if err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	if want := filepath.Join(runtimeDir, "cave-tools"); dir != want {
		t.Fatalf("EnsureSocketDir() = %q, want %q", dir, want)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode = %o, want 700", perm)
	}
}

func TestEnsureSocketDirTightensLooseDir(t *testing.T) {
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	dir := filepath.Join(runtimeDir, "cave-tools")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := EnsureSocketDir("abc"); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode = %o, want 700", perm)
	}
}

func TestEnsureSocketDirRefusesSymlink(t *testing.T) {
	if pathsPlatform == "win32" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	isolate(t)
	requireDist(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	real := filepath.Join(runtimeDir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(runtimeDir, "cave-tools")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := EnsureSocketDir("abc")
	if err == nil {
		t.Fatal("EnsureSocketDir followed a symlink")
	}
	if want := "unsafe socket dir: " + link; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestDaemonLogAndCLIEntry(t *testing.T) {
	isolate(t)
	requireDist(t)
	dataDir := os.Getenv("CAVE_TOOLS_DATA_DIR")
	if got, want := DaemonLogPath(), filepath.Join(dataDir, "daemon.log"); got != want {
		t.Fatalf("DaemonLogPath() = %q, want %q", got, want)
	}
	entry, err := CLIEntryPath()
	if err != nil {
		t.Fatalf("CLIEntryPath: %v", err)
	}
	if !strings.HasSuffix(entry, string(filepath.Separator)+"dist"+string(filepath.Separator)+"cli.js") {
		t.Fatalf("CLIEntryPath() = %q, want .../dist/cli.js", entry)
	}
}

// port of src/daemon/paths.ts:daemonKey composition.
func TestDaemonKeyCoversBuildDataAndConfigDirs(t *testing.T) {
	isolate(t)
	build := requireDist(t)
	key, err := DaemonKey()
	if err != nil {
		t.Fatalf("DaemonKey: %v", err)
	}
	sum := sha256.Sum256([]byte(build + "\x00" + os.Getenv("CAVE_TOOLS_DATA_DIR") + "\x00" + os.Getenv("CLAUDE_CONFIG_DIR")))
	if want := hex.EncodeToString(sum[:])[:12]; key != want {
		t.Fatalf("DaemonKey() = %q, want %q", key, want)
	}
}

func TestBuildIDIsStableAndReadsDist(t *testing.T) {
	isolate(t)
	first := requireDist(t)
	second, err := BuildID()
	if err != nil {
		t.Fatalf("BuildID: %v", err)
	}
	if first != second {
		t.Fatalf("BuildID changed between calls: %q != %q", first, second)
	}
}

func TestClaudeConfigDirFallsBackToHome(t *testing.T) {
	if pathsPlatform == "win32" {
		t.Skip("os.homedir() reads USERPROFILE on Windows")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := claudeConfigDir(), filepath.Join(home, ".claude"); got != want {
		t.Fatalf("claudeConfigDir() = %q, want %q", got, want)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/config")
	if got := claudeConfigDir(); got != "/custom/config" {
		t.Fatalf("claudeConfigDir() = %q, want /custom/config", got)
	}
}
