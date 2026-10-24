// cave__configure: set per-tool budgets, reset the cache or reset stats.
//
// port of src/tools/configure.ts
package configure

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/tools/status"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// ConfigAction is the supported action union.
// port of src/tools/configure.ts:ConfigAction
type ConfigAction string

// Tool is the MCP definition of cave__configure.
// port of src/tools/configure.ts:configureTool
var Tool = types.ToolDefinition{
	Name:        "cave__configure",
	Description: "Set per-tool budgets, reset cache, or reset stats.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["set_budget","reset_cache","reset_stats"]},"tool_name":{"type":"string","description":"set_budget target (bash, read, grep, web)"},"max_lines":{"type":"number"},"head_lines":{"type":"number"},"tail_lines":{"type":"number"},"max_chars":{"type":"number","description":"0 disables; omit to keep"}},"required":["action"]}`),
	Handler:     handleConfigure,
}

// handleConfigure dispatches one configure action.
// port of src/tools/configure.ts:configureTool.handler
func handleConfigure(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	action := ConfigAction(jsString(args, "action"))

	switch action {
	case "set_budget":
		toolName := "bash"
		if value, ok := args["tool_name"]; ok && jsTruthy(value) {
			toolName = jsStringValue(value)
		}
		maxLines := jsNumberOr(args["max_lines"], 100)
		headLines := jsNumberOr(args["head_lines"], 50)
		tailLines := jsNumberOr(args["tail_lines"], 50)
		var maxChars *int64
		if value, ok := args["max_chars"]; ok {
			if number, ok := jsNumber(value); ok && number != 0 {
				truncated := int64(math.Trunc(number))
				maxChars = &truncated
			}
		} else {
			maxChars = compression.GetBudget(toolName).MaxChars
		}

		compression.SetBudget(ctx, toolName, int64(math.Trunc(maxLines)), int64(math.Trunc(headLines)), int64(math.Trunc(tailLines)), maxChars)
		status.RecordBudgetSet(toolName)

		text := "Set budget for " + toolName + ": max=" + jsNumberToString(maxLines) +
			", head=" + jsNumberToString(headLines) + ", tail=" + jsNumberToString(tailLines)
		if maxChars != nil && *maxChars != 0 {
			text += ", chars=" + strconv.FormatInt(*maxChars, 10)
		}
		return types.ToolResult{Content: []types.Content{types.Text(text)}}, nil

	case "reset_cache":
		compression.ResetCache(ctx)
		return types.ToolResult{Content: []types.Content{types.Text("Cache reset")}}, nil

	case "reset_stats":
		compression.ResetStats(ctx)
		return types.ToolResult{Content: []types.Content{types.Text("Statistics reset")}}, nil

	default:
		return types.ToolResult{
			Content: []types.Content{types.Text("Unknown action: " + string(action))},
			IsError: true,
		}, nil
	}
}

// jsString mirrors TS String(args[key]) including missing keys ("undefined").
func jsString(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok {
		return "undefined"
	}
	return jsStringValue(value)
}

// jsStringValue mirrors JS String(value).
func jsStringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	}
	if number, ok := jsNumber(value); ok {
		return jsNumberToString(number)
	}
	switch typed := value.(type) {
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				parts = append(parts, "")
				continue
			}
			parts = append(parts, jsStringValue(item))
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// jsTruthy mirrors JS ToBoolean.
func jsTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0 && !math.IsNaN(typed)
	case float32:
		return typed != 0 && !math.IsNaN(float64(typed))
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return jsNumberMust(typed) != 0
	}
	return true
}

// jsNumberOr mirrors `Number(value) || fallback`.
func jsNumberOr(value any, fallback float64) float64 {
	number, ok := jsNumber(value)
	if !ok || number == 0 {
		return fallback
	}
	return number
}

// jsNumber mirrors JS Number(value); ok is false for NaN.
// port of src/tools/configure.ts:Number
func jsNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case nil:
		number = 0
	case bool:
		if typed {
			number = 1
		}
	case string:
		return jsNumberFromString(typed)
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int8:
		number = float64(typed)
	case int16:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint8:
		number = float64(typed)
	case uint16:
		number = float64(typed)
	case uint32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case []any:
		switch len(typed) {
		case 0:
			number = 0
		case 1:
			return jsNumber(typed[0])
		default:
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(number) {
		return 0, false
	}
	return number, true
}

// jsNumberMust converts a Go integer to a number (always finite).
func jsNumberMust(value any) float64 {
	number, _ := jsNumber(value)
	return number
}

// jsNumberFromString mirrors JS Number(string).
func jsNumberFromString(raw string) (float64, bool) {
	text := jsre.Trim(raw)
	if text == "" {
		return 0, true
	}
	switch text {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		parsed, err := strconv.ParseUint(text[2:], 16, 64)
		if err != nil {
			return 0, false
		}
		return float64(parsed), true
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// jsNumberToString mirrors the Number::toString output for budget values.
// port of src/tools/configure.ts:configureTool.handler
func jsNumberToString(number float64) string {
	if math.IsNaN(number) {
		return "NaN"
	}
	if math.IsInf(number, 1) {
		return "Infinity"
	}
	if math.IsInf(number, -1) {
		return "-Infinity"
	}
	if number == math.Trunc(number) && math.Abs(number) < 1e21 {
		return strconv.FormatFloat(number, 'f', 0, 64)
	}
	return strconv.FormatFloat(number, 'g', -1, 64)
}
