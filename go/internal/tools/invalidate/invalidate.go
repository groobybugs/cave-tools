// Package invalidate ports src/tools/invalidate.ts: the cave__invalidate tool.
package invalidate

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Tool is the cave__invalidate MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__invalidate",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Drop cave__read cache entries (no disk write). Call after editing files outside Cave Tools.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"file_paths":{"type":"array","items":{"type":"string"},"description":"Absolute paths"}},"required":["file_paths"]}`),
	Handler:     handle,
}

// handle is the cave__invalidate handler.
// port of src/tools/invalidate.ts:invalidateTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	paths := make([]string, 0, 4)
	switch raw := args["file_paths"].(type) {
	case []any:
		for _, item := range raw {
			value := jsString(item)
			if len(value) > 0 {
				paths = append(paths, value)
			}
		}
	case []string:
		for _, item := range raw {
			if len(item) > 0 {
				paths = append(paths, item)
			}
		}
	}

	if len(paths) == 0 {
		return types.ToolResult{
			Content: []types.Content{types.Text("Error: `file_paths` must be a non-empty array")},
			IsError: true,
		}, nil
	}

	for _, filePath := range paths {
		compression.InvalidateFileCache(ctx, filePath)
	}

	return types.ToolResult{
		Content: []types.Content{types.Text("Invalidated cache for " + strconv.Itoa(len(paths)) + " file(s)")},
	}, nil
}
