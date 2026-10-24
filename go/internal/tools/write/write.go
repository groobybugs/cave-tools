// Package write ports the cave__write tool.
//
// Port of src/tools/write.ts.
package write

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf16"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Tool is the MCP tool definition for cave__write.
// port of src/tools/write.ts:writeTool
var Tool = types.ToolDefinition{
	Name: "cave__write",
	Description: "Create or overwrite one file and refresh the read cache. Empty content or truncate=true writes an empty file. " +
		"For cache-only refresh use cave__invalidate.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_path": {
				"type": "string",
				"description": "Absolute path"
			},
			"content": {
				"type": "string",
				"description": "Omit only with truncate"
			},
			"truncate": {
				"type": "boolean",
				"description": "Write a 0-byte file",
				"default": false
			}
		},
		"required": ["file_path"]
	}`),
	Handler: handle,
}

// handle is the cave__write handler.
// port of src/tools/write.ts:writeTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	filePath := jsString(args["file_path"])
	if len(filePath) == 0 {
		return errResult("`file_path` is required"), nil
	}

	truncate := args["truncate"] == true
	_, hasContent := args["content"].(string)

	if !truncate && !hasContent {
		return errResult("provide `content` to write, or set `truncate: true`. For cache-only invalidation use cave__invalidate."), nil
	}

	content := ""
	if !truncate {
		content = jsString(args["content"])
	}
	result, writeErr := runtime.WriteTextPreservingBom(ctx, filePath, content)
	if writeErr != nil {
		return errResult(fmt.Sprintf("cannot write %s", filePath)), nil
	}
	compression.InvalidateFileCache(ctx, result.Canonical)
	compression.InvalidateFileCache(ctx, filePath)
	compression.RecordEdit(ctx, result.Canonical)
	if truncate {
		return okResult(fmt.Sprintf("Truncated %s (0 bytes)", result.Canonical)), nil
	}
	state := "created new file"
	if result.Existed {
		state = "overwrote existing file"
	}
	return okResult(fmt.Sprintf("Wrote %d chars to %s (%s)", utf16Length(content), result.Canonical, state)), nil
}

// port of src/tools/write.ts:ok
func okResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

// port of src/tools/write.ts:err
func errResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text("Error: " + text)}, IsError: true}
}

// utf16Length mirrors String.prototype.length: UTF-16 code units, not runes.
func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

// jsString mirrors JavaScript's String(value) for the JSON values a handler
// can receive; TS write.ts stringifies args.file_path and args.content.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}
