// Package compress ports src/tools/compress.ts: the cave__compress tool that
// shrinks pasted text with the shared budget/structured extractors and expands
// previously archived output back to its original bytes.
package compress

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Tool is the cave__compress tool definition. Annotations, description and
// inputSchema hold the exact JSON src/tools/compress.ts declares.
// port of src/tools/compress.ts:compressTool
var Tool = types.ToolDefinition{
	Name:        "cave__compress",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Compress text to fewer tokens (ANSI strip, line budget, JSON/XML extraction). action='expand' + archive_id retrieves archived output.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"command_hint":{"type":"string","description":"Command that produced the text"},"structured":{"type":"boolean","description":"JSON/XML extraction","default":true},"tool_name":{"type":"string","description":"Budget to apply (bash, read, grep)","default":"bash"},"action":{"type":"string","enum":["compress","expand"],"default":"compress"},"archive_id":{"type":"string","description":"For action='expand'"}},"required":["text"]}`),
	Handler:     handler,
}

// handler is compressTool.handler.
// port of src/tools/compress.ts:compressTool.handler
func handler(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	action := jsString(jsOr(args["action"], "compress"))

	if action == "expand" {
		archiveID := jsString(jsOr(args["archive_id"], ""))
		if archiveID == "" {
			return errorResult("Error: archive_id required"), nil
		}
		expanded, ok := compression.ExpandArchive(archiveID)
		if !ok {
			return errorResult("Archive not found: " + archiveID), nil
		}
		return types.ToolResult{Content: []types.Content{types.Text(expanded)}}, nil
	}

	text := jsStringArg(args, "text")
	var commandHint *string
	if hint := args["command_hint"]; jsTruthy(hint) {
		value := jsString(hint)
		commandHint = &value
	}
	structured := !jsIsFalse(args["structured"])
	toolName := jsString(jsOr(args["tool_name"], "bash"))

	result := text
	if structured {
		hint := ""
		if commandHint != nil {
			hint = *commandHint
		}
		result = compression.ExtractStructuredData(result, hint)
	}
	result = compression.ApplyBudget(ctx, result, toolName)

	return types.ToolResult{Content: []types.Content{types.Text(result)}}, nil
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

// jsOr is JavaScript's `value || fallback`.
func jsOr(value any, fallback any) any {
	if jsTruthy(value) {
		return value
	}
	return fallback
}

// jsIsFalse is `value === false`.
func jsIsFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

// jsTruthy mirrors JavaScript truthiness for the JSON value shapes handlers
// receive.
func jsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	case float32:
		return v != 0 && !math.IsNaN(float64(v))
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return true
	}
}

// jsStringArg mirrors String(args[key]): a missing key is undefined, an
// explicit JSON null is null, and the presence flag in the map tells them
// apart.
func jsStringArg(args map[string]any, key string) string {
	value, present := args[key]
	if !present {
		return "undefined"
	}
	if value == nil {
		return "null"
	}
	return jsString(value)
}

// jsString mirrors String(value) for the JSON value shapes handlers receive.
// Explicit nulls must be resolved through jsStringArg first; nil here means
// the caller already knows the JS value is undefined.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return "undefined"
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberToString(v)
	case float32:
		return jsNumberToString(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return v.String()
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item == nil {
				continue
			}
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// jsNumberToString formats a float64 like ECMAScript Number::toString(10).
func jsNumberToString(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 0) {
		if value > 0 {
			return "Infinity"
		}
		return "-Infinity"
	}
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}

	repr := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentPart, _ := strings.Cut(repr, "e")
	exponent, _ := strconv.Atoi(exponentPart)
	digits := strings.Replace(mantissa, ".", "", 1)
	n := len(digits)
	k := exponent + 1

	var out string
	switch {
	case k > 0 && k <= 21:
		if n <= k {
			out = digits + strings.Repeat("0", k-n)
		} else {
			out = digits[:k] + "." + digits[k:]
		}
	case k <= 0 && k > -6:
		out = "0." + strings.Repeat("0", -k) + digits
	default:
		if n == 1 {
			out = digits
		} else {
			out = digits[:1] + "." + digits[1:]
		}
		exponentOut := k - 1
		if exponentOut >= 0 {
			out += "e+" + strconv.Itoa(exponentOut)
		} else {
			out += "e-" + strconv.Itoa(-exponentOut)
		}
	}
	if negative {
		out = "-" + out
	}
	return out
}
