// Package find ports src/tools/find.ts: the cave__find tool.
package find

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// port of src/tools/find.ts:DEFAULT_LIMIT
const defaultLimit = 1000

// Tool is the cave__find MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__find",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Use instead of Glob/find (fd or Node fallback). Returns paths relative to path, trimmed to a budget.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"e.g. '**/*.ts'"},"path":{"type":"string"},"limit":{"type":"number","description":"Max results (default 1000)"},"hidden":{"type":"boolean","description":"Include dotfiles; false matches opencode glob","default":true}},"required":["pattern"]}`),
	Handler:     handle,
}

// handle is the cave__find handler.
// port of src/tools/find.ts:findTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	pattern := jsString(args["pattern"])
	limit := float64(defaultLimit)
	if raw, ok := args["limit"]; ok {
		if number := jsNumber(raw); !math.IsNaN(number) && number != 0 {
			limit = number
		}
	}
	limit = math.Max(1, limit)
	hidden := !isFalse(args["hidden"])

	pathArg := "."
	if raw, ok := args["path"]; ok && jsTruthy(raw) {
		pathArg = jsString(raw)
	}

	searchPath, err := rt.ResolveExistingDirectory(ctx, pathArg)
	if err != nil {
		return errorResult("Path not found or not a directory: " + resolvePath(rt.GetCwd(ctx), pathArg)), nil
	}

	lines, err := rt.RgFiles(ctx, searchPath, pattern, limitInt(limit), hidden)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}

	if len(lines) == 0 {
		return textResult("No files found matching pattern"), nil
	}

	output := strings.Join(lines, "\n")
	if float64(len(lines)) >= limit {
		output += "\n\n[" + jsNumberString(limit) + " results limit reached. Use limit=" +
			jsNumberString(limit*2) + " for more]"
	}
	return textResult(compression.ApplyBudget(ctx, output, "find")), nil
}

// resolvePath mirrors Node's path.resolve(cwd, value).
func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

// limitInt converts the JS number limit to an int, saturating instead of
// overflowing.
func limitInt(limit float64) int {
	if math.IsNaN(limit) {
		return 0
	}
	if limit >= math.MaxInt32 {
		return math.MaxInt32
	}
	return int(limit)
}

func textResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

func isFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

// jsNumberString formats a float like ECMAScript Number::toString(10) for the
// values tool arguments carry. strconv.Itoa would print 1e+21 as an integer.
func jsNumberString(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		return "0"
	}
	abs := math.Abs(value)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	formatted := strconv.FormatFloat(value, 'e', -1, 64)
	if index := strings.IndexByte(formatted, 'e'); index >= 0 {
		mantissa, exponent := formatted[:index], formatted[index+1:]
		exponentSign := ""
		if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
			exponentSign, exponent = exponent[:1], exponent[1:]
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent == "" {
			exponent = "0"
		}
		formatted = mantissa + "e" + exponentSign + exponent
	}
	return formatted
}
