//go:build windows

package statusline

import "os"

// dirOwnerOK: Node's process.getuid is undefined on Windows, so the TS
// ownership check is skipped.
func dirOwnerOK(info os.FileInfo) bool { return true }
