//go:build unix

package daemon

import (
	"os"
	"strings"
	"syscall"
)

// pathsPlatform is the value TS compares against "win32".
const pathsPlatform = "unix"

// port of src/daemon/paths.ts:socketDir (typeof process.getuid branch)
func currentUID() (int, bool) { return os.Getuid(), true }

// fileOwnerUID reads st_uid for ensureSocketDir's ownership refusal.
func fileOwnerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

// nodeTmpDir mirrors Node's os.tmpdir() on POSIX: TMPDIR, TMP, TEMP,
// TEMPDIR, one trailing slash stripped, else /tmp.
func nodeTmpDir() string {
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "TEMPDIR"} {
		path := os.Getenv(name)
		if path == "" {
			continue
		}
		if len(path) > 1 && strings.HasSuffix(path, "/") {
			path = path[:len(path)-1]
		}
		return path
	}
	return "/tmp"
}
