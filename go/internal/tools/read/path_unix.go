//go:build !windows

// Separator predicate for Node's posix path implementations.
// port of src/tools/read.ts (path.extname/dirname/basename, node:path posix)

package read

// pathIsSeparator reports whether c is a posix path separator.
func pathIsSeparator(c byte) bool {
	return c == '/'
}
