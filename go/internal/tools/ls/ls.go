// Package ls ports src/tools/ls.ts: the cave__ls tool.
package ls

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// port of src/tools/ls.ts:DEFAULT_LIMIT
const defaultLimit = 500

// Tool is the cave__ls MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__ls",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Use instead of shell ls. Sorted entries, '/' suffix on dirs, dotfiles included, trimmed to a budget.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"number","description":"Max entries (default 500)"},"offset":{"type":"number","description":"1-based entry offset","default":1}}}`),
	Handler:     handle,
}

// entry is one listed directory member.
type entry struct {
	name        string
	isDirectory bool
}

// handle is the cave__ls handler.
// port of src/tools/ls.ts:lsTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	pathArg := "."
	if raw, ok := args["path"]; ok && jsTruthy(raw) {
		pathArg = jsString(raw)
	}
	requested := resolvePath(rt.GetCwd(ctx), pathArg)
	limit := float64(defaultLimit)
	if raw, ok := args["limit"]; ok {
		if number := jsNumber(raw); !math.IsNaN(number) && number != 0 {
			limit = number
		}
	}
	limit = math.Max(1, limit)
	offset := 1.0
	if raw, ok := args["offset"]; ok {
		if number := jsNumber(raw); !math.IsNaN(number) && number != 0 {
			offset = number
		}
	}
	offset = math.Max(1, offset)

	dirPath, err := rt.ResolveExistingDirectory(ctx, requested)
	if err != nil {
		return errorResult("Path not found or not a directory: " + requested), nil
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return errorResult("Cannot read directory: " + readdirErrorMessage(err, dirPath)), nil
	}
	results := make([]entry, 0, len(entries))
	for _, directoryEntry := range entries {
		fullPath := filepath.Join(dirPath, directoryEntry.Name())
		target, err := realpath(fullPath)
		if err != nil {
			continue
		}
		if !rt.ContainsPath(dirPath, target) {
			continue
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			// Skip entries that disappeared or cannot be statted.
			continue
		}
		switch {
		case info.IsDir():
			results = append(results, entry{name: directoryEntry.Name() + "/", isDirectory: true})
		case info.Mode().IsRegular():
			results = append(results, entry{name: directoryEntry.Name(), isDirectory: false})
		}
	}
	sortEntries(results)

	// results.slice(offset - 1, offset - 1 + limit): JS slice clamps to the
	// array, and Infinity is a legal JS limit, so clamp before converting.
	startFloat := offset - 1
	if startFloat < 0 {
		startFloat = 0
	}
	if startFloat > float64(len(results)) {
		startFloat = float64(len(results))
	}
	endFloat := startFloat + limit
	if endFloat < startFloat {
		endFloat = startFloat
	}
	if endFloat > float64(len(results)) {
		endFloat = float64(len(results))
	}
	start := int(startFloat)
	end := int(endFloat)
	selected := make([]string, 0, end-start)
	for _, item := range results[start:end] {
		selected = append(selected, item.name)
	}
	limitReached := offset-1+float64(len(selected)) < float64(len(results))

	if len(selected) == 0 {
		return textResult("(empty directory)"), nil
	}

	output := strings.Join(selected, "\n")
	if limitReached {
		output += "\n\n[Directory listing truncated. Use offset=" +
			jsNumberString(offset+float64(len(selected))) + " to continue]"
	}
	return textResult(compression.ApplyBudget(ctx, output, "ls")), nil
}

// sortEntries orders directories first, then by localeCompare on the name,
// mirroring `a.type === b.type ? a.name.localeCompare(b.name) : ...`. Two
// stable passes: locale-sort by name, then move directories to the front.
func sortEntries(entries []entry) {
	rt.SortStableByLocale(entries, func(value entry) string { return value.name })
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].isDirectory && !entries[j].isDirectory
	})
}

// readdirErrorMessage renders the Node fs error message for a failed readdir.
func readdirErrorMessage(err error, dirPath string) string {
	if os.IsPermission(err) {
		return "EACCES: permission denied, scandir '" + dirPath + "'"
	}
	if os.IsNotExist(err) {
		return "ENOENT: no such file or directory, scandir '" + dirPath + "'"
	}
	if errors.Is(err, syscall.ENOTDIR) {
		return "ENOTDIR: not a directory, scandir '" + dirPath + "'"
	}
	if errors.Is(err, syscall.ELOOP) {
		return "ELOOP: too many symbolic links encountered, scandir '" + dirPath + "'"
	}
	return err.Error()
}

// realpath mirrors fs.realpath: always an absolute, symlink-free path.
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

// resolvePath mirrors Node's path.resolve(cwd, value).
func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

func textResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}
