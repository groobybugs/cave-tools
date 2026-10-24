//go:build windows

// Platform half of the tool-error sink. TS skips the pre-create and every
// permission check on win32, so both helpers are no-ops there.
//
// port of src/runtime/tool-errors.ts:tightenIfOwned
package runtime

import "os"

// platformSkipDBCreate reports the TS `process.platform === "win32"` guard.
func platformSkipDBCreate() bool { return true }

// tightenIfOwned is a no-op on Windows, like TS's win32 early return.
// port of src/runtime/tool-errors.ts:tightenIfOwned
func tightenIfOwned(target string, expectDir bool, mode os.FileMode) bool {
	return true
}

// openNoFollow is never called on Windows (platformSkipDBCreate is true).
// port of src/runtime/tool-errors.ts:openSync
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
}
