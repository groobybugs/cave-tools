//go:build windows

package daemon

import "os"

// pathsPlatform is the value TS compares against "win32".
const pathsPlatform = "win32"

// port of src/daemon/paths.ts:socketDir (no process.getuid on Windows)
func currentUID() (int, bool) { return 0, false }

// fileOwnerUID has no equivalent of st_uid on Windows.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }

// nodeTmpDir mirrors Node's os.tmpdir() on Windows (GetTempPath).
func nodeTmpDir() string { return os.TempDir() }
