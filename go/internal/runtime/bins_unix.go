//go:build !windows

// Unix executable check for the search bins.
//
// Mirrors src/runtime/bins.ts:121-127 (access with X_OK on non-win32).
package runtime

import "syscall"

// port of src/runtime/bins.ts:isExecutable (non-win32: access with X_OK)
func isExecutable(filePath string) bool {
	// X_OK is 1 in POSIX; Go keeps it out of package syscall.
	const xOK = 1
	return syscall.Access(filePath, xOK) == nil
}
