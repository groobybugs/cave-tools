//go:build windows

// Windows executable check for the search bins.
//
// Mirrors src/runtime/bins.ts:121-127 (access with F_OK on win32; Windows has
// no executable bit).
package runtime

import "os"

// port of src/runtime/bins.ts:isExecutable (win32: access with F_OK)
func isExecutable(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}
