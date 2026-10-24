// Package daemon owns the shared daemon: socket naming, the client-side
// control commands and (later wave) the server itself.
//
// Port of src/daemon/paths.ts and src/daemon/control.ts.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// maxSocketPath mirrors TS MAX_SOCKET_PATH: unix sun_path is ~107 bytes.
const maxSocketPath = 100

// collectJS appends every .js file below dir, like TS collectJs.
// port of src/daemon/paths.ts:collectJs
func collectJS(dir string, out *[]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if err := collectJS(full, out); err != nil {
				return err
			}
			continue
		}
		if strings.HasSuffix(entry.Name(), ".js") {
			*out = append(*out, full)
		}
	}
	return nil
}

// tsDistDir locates the compiled TypeScript tree that src/daemon/paths.ts gets
// from import.meta.url. The Go binary has no module URL, so it honours
// CAVE_TOOLS_GO_DIST_DIR (set by the spawning Go client) and otherwise walks up
// from its own directory and then from the working directory to the repository
// root (a directory with both package.json and dist/).
//
// This is what lets the Go client derive the same daemon key as a TS daemon.
func tsDistDir() (string, error) {
	if hinted := os.Getenv(distDirEnv); hinted != "" && isDirectory(hinted) {
		return hinted, nil
	}
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	seen := make(map[string]struct{}, len(starts))
	for _, start := range starts {
		for dir := start; ; dir = filepath.Dir(dir) {
			if _, ok := seen[dir]; ok {
				break
			}
			seen[dir] = struct{}{}
			dist := filepath.Join(dir, "dist")
			if isDirectory(dist) && isFile(filepath.Join(dir, "package.json")) {
				return dist, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	return "", fmt.Errorf("dist directory not found")
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// formatMtimeMs mirrors JavaScript's String(stat.mtimeMs), the value TS feeds
// into the build hash. Node computes tv_sec*1000 + tv_nsec/1e6 as a double.
func formatMtimeMs(t time.Time) string {
	ms := float64(t.Unix())*1000 + float64(t.Nanosecond())/1e6
	return strconv.FormatFloat(ms, 'f', -1, 64)
}

// BuildID returns the 16-hex-character fingerprint of the package version plus
// every compiled .js file's name, size and mtime. Not memoized: a long-lived
// client must see a rebuild, exactly like TS.
//
// port of src/daemon/paths.ts:buildId
func BuildID() (string, error) {
	dist, err := tsDistDir()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	// No package.json next to dist is tolerated by TS; the version is skipped.
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(dist), "package.json")); err == nil {
		var pkg struct {
			Version any `json:"version"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			if version, ok := pkg.Version.(string); ok {
				hash.Write([]byte(version))
			}
		}
	}
	var files []string
	if err := collectJS(dist, &files); err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(file))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		hash.Write([]byte{0})
		hash.Write([]byte(formatMtimeMs(info.ModTime())))
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}

// claudeConfigDir mirrors TS claudeConfigDir: raw env value or ~/.claude.
// port of src/daemon/paths.ts:claudeConfigDir
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// DaemonKey returns the 12-hex-character key covering the build and the
// data/config dirs.
//
// port of src/daemon/paths.ts:daemonKey
func DaemonKey() (string, error) {
	build, err := BuildID()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(build + "\x00" + storage.CaveToolsDataDir() + "\x00" + claudeConfigDir()))
	return hex.EncodeToString(hash[:])[:12], nil
}

// keyOrDefault resolves TS's `key: string = daemonKey()` default parameter.
func keyOrDefault(key []string) (string, error) {
	if len(key) > 0 {
		return key[0], nil
	}
	return DaemonKey()
}

// SocketDir returns the directory holding the daemon socket, falling back to
// the temp dir when the preferred path would exceed sun_path.
//
// port of src/daemon/paths.ts:socketDir
func SocketDir(key ...string) (string, error) {
	k, err := keyOrDefault(key)
	if err != nil {
		return "", err
	}
	runtime := jsre.Trim(os.Getenv("XDG_RUNTIME_DIR"))
	var preferred string
	if runtime != "" {
		preferred = filepath.Join(runtime, "cave-tools")
	} else {
		preferred = filepath.Join(storage.CaveToolsDataDir(), "run")
	}
	if jsLen(filepath.Join(preferred, "daemon-"+k+".sock")) <= maxSocketPath {
		return preferred, nil
	}
	uid := "user"
	if id, ok := currentUID(); ok {
		uid = strconv.Itoa(id)
	}
	dir := nodeTmpDir()
	if jsLen(dir) >= 40 {
		dir = "/tmp"
	}
	return filepath.Join(dir, "cave-tools-"+uid), nil
}

// EnsureSocketDir creates the socket dir 0700 and refuses one we do not own or
// that is a symlink (matters for the shared /tmp fallback).
//
// port of src/daemon/paths.ts:ensureSocketDir
func EnsureSocketDir(key ...string) (string, error) {
	dir, err := SocketDir(key...)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("unsafe socket dir: %s", dir)
	}
	if uid, ok := currentUID(); ok {
		if owner, ok := fileOwnerUID(info); ok && owner != uid {
			return "", fmt.Errorf("socket dir not owned by this user: %s", dir)
		}
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// SocketPath returns the unix socket path, or the named pipe on Windows.
//
// port of src/daemon/paths.ts:socketPath
func SocketPath(key ...string) (string, error) {
	k, err := keyOrDefault(key)
	if err != nil {
		return "", err
	}
	if pathsPlatform == "win32" {
		return `\\.\pipe\cave-tools-` + k, nil
	}
	dir, err := SocketDir(k)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon-"+k+".sock"), nil
}

// LockPath returns the spawn-lock path next to the socket.
//
// port of src/daemon/paths.ts:lockPath
func LockPath(key ...string) (string, error) {
	k, err := keyOrDefault(key)
	if err != nil {
		return "", err
	}
	dir, err := SocketDir(k)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon-"+k+".lock"), nil
}

// DaemonLogPath returns the shared daemon log file.
//
// port of src/daemon/paths.ts:daemonLogPath
func DaemonLogPath() string {
	return filepath.Join(storage.CaveToolsDataDir(), "daemon.log")
}

// CLIEntryPath returns the compiled TS CLI entry point the daemon spawns.
//
// port of src/daemon/paths.ts:cliEntryPath
func CLIEntryPath() (string, error) {
	dist, err := tsDistDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dist, "cli.js"), nil
}

// jsLen mirrors JavaScript String.prototype.length (UTF-16 code units).
func jsLen(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
