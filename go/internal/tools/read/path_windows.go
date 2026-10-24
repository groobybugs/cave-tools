//go:build windows

// Separator predicate for Node's win32 path implementations.
// port of src/tools/read.ts (path.extname/dirname/basename, node:path win32)

package read

// pathIsSeparator reports whether c is a windows path separator.
func pathIsSeparator(c byte) bool {
	return c == '/' || c == '\\'
}
