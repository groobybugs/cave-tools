//go:build !windows

// Platform half of the tool-error sink: owner-only file creation and
// permission tightening without following symlinks.
//
// port of src/runtime/tool-errors.ts:tightenIfOwned
package runtime

import (
	"os"
	"syscall"
)

// platformSkipDBCreate reports the TS `process.platform === "win32"` guard.
func platformSkipDBCreate() bool { return false }

// tightenIfOwned tightens a path without following symlinks. Returns false
// when the path is missing, a symlink, or the wrong kind — never chmods a
// symlink target.
// port of src/runtime/tool-errors.ts:tightenIfOwned
func tightenIfOwned(target string, expectDir bool, mode os.FileMode) bool {
	info, err := os.Lstat(target)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if expectDir && !info.IsDir() {
		return false
	}
	if !expectDir && !info.Mode().IsRegular() {
		return false
	}
	if info.Mode().Perm()&^mode != 0 {
		if err := os.Chmod(target, mode); err != nil {
			return false
		}
	}
	return true
}

// openNoFollow creates the db file with O_NOFOLLOW and explicit 0600.
// port of src/runtime/tool-errors.ts:openSync
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}
