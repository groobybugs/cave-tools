// Path helpers shared by the tool layer.
//
// Port of src/runtime/path.ts.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// port of src/runtime/path.ts:toPosixPath
func ToPosixPath(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}

// port of src/runtime/path.ts:containsPath
func ContainsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		// path.relative only fails across Windows volumes, where Node returns the
		// absolute child path. That is never inside parent.
		return false
	}
	return relative == "" ||
		(!filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// resolvePath mirrors Node's path.resolve(cwd, value): absolute values win,
// everything else is joined to cwd and cleaned.
func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

// realpath mirrors fs.realpath: always an absolute, symlink-free path.
// Go's filepath.EvalSymlinks keeps relative input relative and relative
// output, while Node always resolves against the process cwd.
func realpath(value string) (string, error) {
	absolute := value
	if !filepath.IsAbs(absolute) {
		var err error
		absolute, err = filepath.Abs(absolute)
		if err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// port of src/runtime/path.ts:realPathSafe
func RealPathSafe(ctx context.Context, value string) (string, error) {
	// Resolve relative paths against the call cwd, never the server's.
	resolved, err := realpath(resolvePath(GetCwd(ctx), value))
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return resolvePath(GetCwd(ctx), value), nil
}

// port of src/runtime/path.ts:resolveExistingDirectory
func ResolveExistingDirectory(ctx context.Context, value string) (string, error) {
	v := value
	if v == "" {
		v = "."
	}
	resolved := resolvePath(GetCwd(ctx), v)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Not a directory: %s", resolved)
	}
	return realpath(resolved)
}

// port of src/runtime/path.ts:ensureParentDirectory
func EnsureParentDirectory(filePath string) error {
	// fs.mkdir default mode is 0o777 & ~umask, matching os.MkdirAll here.
	return os.MkdirAll(filepath.Dir(filePath), 0o777)
}

// MutationTarget mirrors the { canonical, existed } result of the mutation helpers.
type MutationTarget struct {
	Canonical string `json:"canonical"`
	Existed   bool   `json:"existed"`
}

// port of src/runtime/path.ts:resolveMutationTarget
func ResolveMutationTarget(ctx context.Context, filePath string) (MutationTarget, error) {
	absolute := resolvePath(GetCwd(ctx), filePath)
	canonical, err := realpath(absolute)
	if err == nil {
		return MutationTarget{Canonical: canonical, Existed: true}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return MutationTarget{}, err
	}
	parent, err := RealPathSafe(ctx, filepath.Dir(absolute))
	if err != nil {
		return MutationTarget{}, err
	}
	return MutationTarget{Canonical: filepath.Join(parent, filepath.Base(absolute)), Existed: false}, nil
}
