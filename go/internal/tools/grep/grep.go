// Package grep ports src/tools/grep.ts: the cave__grep tool.
package grep

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// port of src/tools/grep.ts:DEFAULT_LIMIT
const defaultLimit = 100

// port of src/tools/grep.ts:PER_FILE_LINE_CAP
const perFileLineCap = 10

// Tool is the cave__grep MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__grep",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Use instead of Grep (ripgrep). Respects .gitignore, caps hits per file, trims output to a budget.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex (or literal with literal=true)"},"path":{"type":"string"},"glob":{"type":"string","description":"File filter, e.g. '*.ts'"},"ignoreCase":{"type":"boolean"},"literal":{"type":"boolean"},"context":{"type":"number","description":"Lines around each match"},"limit":{"type":"number","description":"Max matches (default 100)"}},"required":["pattern"]}`),
	Handler:     handle,
}

// handle is the cave__grep handler.
// port of src/tools/grep.ts:grepTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	pattern := jsArgString(args, "pattern")
	searchDir := "."
	if raw, ok := args["path"]; ok && jsTruthy(raw) {
		searchDir = jsString(raw)
	}
	glob := ""
	if raw, ok := args["glob"]; ok && jsTruthy(raw) {
		glob = jsString(raw)
	}
	ignoreCase := isTrue(args["ignoreCase"])
	literal := isTrue(args["literal"])
	contextValue := 0.0
	if raw, ok := args["context"]; ok {
		if number, isNumber := numberValue(raw); isNumber && number > 0 {
			contextValue = number
		}
	}
	effectiveLimit := float64(defaultLimit)
	if raw, ok := args["limit"]; ok && raw != nil {
		effectiveLimit = jsNumber(raw)
	}
	effectiveLimit = math.Max(1, effectiveLimit)

	searchPath := resolvePath(rt.GetCwd(ctx), searchDir)
	info, err := os.Stat(searchPath)
	if err != nil {
		return errorResult("Path not found: " + searchPath), nil
	}
	isDirectory := info.IsDir()

	cwd := searchPath
	file := ""
	if !isDirectory {
		cwd = filepath.Dir(searchPath)
		file = filepath.Base(searchPath)
	}
	result, err := rt.RgGrep(ctx, rt.RgGrepInput{
		Cwd:        cwd,
		Pattern:    pattern,
		File:       file,
		Include:    glob,
		Limit:      effectiveLimit,
		IgnoreCase: ignoreCase,
		Literal:    literal,
	})
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}
	truncated := result.Truncated
	matchesPath := cwd
	if isDirectory {
		matchesPath = searchPath
	}
	output, full := formatMatches(result.Matches, cwd, matchesPath, contextValue, truncated, effectiveLimit)

	if output == "" {
		return textResult("No matches found"), nil
	}

	compressed := compression.ApplyBudget(ctx, output, "grep")
	handle := compression.ArchiveHandle(full, compressed, "grep:"+pattern)
	return textResult(compressed + handle), nil
}

// formatter accumulates formatMatches' output lines and its per-file cap.
// port of src/tools/grep.ts:formatMatches (pushLine / flushOverflow closures)
type formatter struct {
	outputLines    []string
	allLines       []string
	linesTruncated bool
	currentFile    string
	fileLineCount  int
	fileOverflow   int
}

// flushOverflow emits the "+N more in <file>" marker for the current file.
func (f *formatter) flushOverflow() {
	if f.fileOverflow > 0 && f.currentFile != "" {
		f.outputLines = append(f.outputLines, "+"+strconv.Itoa(f.fileOverflow)+" more in "+f.currentFile)
	}
}

// pushLine records a line for the archive and (up to the per-file cap) for the
// shown output.
func (f *formatter) pushLine(relative, line string) {
	f.allLines = append(f.allLines, line)
	if relative != f.currentFile {
		f.flushOverflow()
		f.currentFile = relative
		f.fileLineCount = 0
		f.fileOverflow = 0
	}
	if f.fileLineCount >= perFileLineCap {
		f.fileOverflow++
		return
	}
	f.fileLineCount++
	f.outputLines = append(f.outputLines, line)
}

// formatMatches renders matches with context windows and the per-file cap.
// port of src/tools/grep.ts:formatMatches
func formatMatches(matches []rt.GrepMatch, cwd, searchPath string, contextValue float64, truncated bool, effectiveLimit float64) (string, string) {
	if len(matches) == 0 {
		return "", ""
	}
	f := &formatter{
		outputLines: []string{},
		allLines:    []string{},
	}

	for _, match := range matches {
		absolute := resolvePath(cwd, match.Path)
		relative := relativePath(searchPath, absolute)
		if relative == "" {
			relative = filepath.Base(absolute)
		}
		if contextValue == 0 {
			if strings.Contains(match.Text, "...") {
				f.linesTruncated = true
			}
			f.pushLine(relative, relative+":"+strconv.FormatInt(match.Line, 10)+": "+match.Text)
			continue
		}
		window, err := rt.ReadLineWindow(absolute, float64(match.Line), contextValue)
		if err != nil {
			f.pushLine(relative, relative+":"+strconv.FormatInt(match.Line, 10)+": "+match.Text)
			continue
		}
		for _, item := range window {
			if strings.Contains(item.Text, "... (truncated)") {
				f.linesTruncated = true
			}
			lineNumber := jsNumberString(item.Line)
			if item.Match {
				f.pushLine(relative, relative+":"+lineNumber+": "+item.Text)
			} else {
				f.pushLine(relative, relative+"-"+lineNumber+"- "+item.Text)
			}
		}
	}
	f.flushOverflow()

	output := compression.CollapseRepetition(strings.Join(f.outputLines, "\n"))
	full := strings.Join(f.allLines, "\n")
	notices := make([]string, 0, 2)
	if truncated {
		notices = append(notices, jsNumberString(effectiveLimit)+" matches limit. Use limit="+
			jsNumberString(effectiveLimit*2)+" for more, or refine pattern")
	}
	if f.linesTruncated {
		notices = append(notices, "Some lines truncated. Use cave__read to see full lines")
	}
	if len(notices) > 0 {
		notice := "\n\n[" + strings.Join(notices, ". ") + "]"
		output += notice
		full += notice
	}
	return output, full
}

// resolvePath mirrors Node's path.resolve(cwd, value): absolute values win,
// everything else is joined to cwd and cleaned.
func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

// relativePath mirrors path.relative(...).replace(/\\/g, "/").
func relativePath(from, to string) string {
	relative, err := filepath.Rel(from, to)
	if err != nil {
		// Node returns the absolute target when the paths have no relative
		// form (different Windows volumes).
		return filepath.ToSlash(to)
	}
	if relative == "." {
		// path.relative returns "" for the same path.
		return ""
	}
	return filepath.ToSlash(relative)
}

func textResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

func isTrue(value any) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}
