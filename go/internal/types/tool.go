package types

import (
	"context"
	"encoding/json"
)

// ToolDefinition is one MCP tool: its listing metadata plus the handler.
// InputSchema and Annotations hold the exact JSON the TS tool object declares,
// so tools/list output matches TS without re-encoding.
// A handler returns an error where the TS handler throws; dispatch handles it.
// port of src/dispatch.ts:ToolDefinition
type ToolDefinition struct {
	Name        string          `json:"name"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Handler     ToolHandler     `json:"-"`
}

// ToolHandler runs a tool call. ctx carries the runtime call context
// (cwd, env, session) set up by dispatch.
type ToolHandler func(ctx context.Context, args map[string]any) (ToolResult, error)
