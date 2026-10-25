// Package server is the MCP entry layer: tool list, dispatch, profile and the
// stdio server.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/daemon"
	callruntime "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/statusline"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

const (
	serverName = "cave-tools"
	// bootLine is the exact stderr line TS prints after connecting stdio.
	bootLine = "Cave Tools MCP server running on stdio"
)

// mcpToolListResult keeps the TS tool order and the ToolDefinition field order
// (name, annotations, description, inputSchema). The SDK's own tools/list sorts
// by name, which would break the published order.
type mcpToolListResult struct {
	mcp.ResultBase
	Tools []types.ToolDefinition `json:"tools"`
}

// mcpToolCallResult marshals content blocks directly from types.Content, so
// image/resource payloads stay byte-identical to TS (the SDK re-encodes []byte
// and uses a different key order).
type mcpToolCallResult struct {
	mcp.ResultBase
	Content []types.Content `json:"content"`
	IsError bool            `json:"isError,omitempty"`
}

// MarshalJSON keeps the TS ToolResult field order: content, isError, _meta.
func (r *mcpToolCallResult) MarshalJSON() ([]byte, error) {
	wire := struct {
		Content []types.Content `json:"content"`
		IsError bool            `json:"isError,omitempty"`
		Meta    map[string]any  `json:"_meta,omitempty"`
	}{Content: r.Content, IsError: r.IsError, Meta: r.Meta}
	if wire.Content == nil {
		wire.Content = []types.Content{}
	}
	return marshalJSONNoEscape(wire)
}

// marshalJSONNoEscape mirrors the SDK transport's JSON options: TS
// JSON.stringify leaves <, > and & literal, so the custom result marshaller
// must not HTML-escape them either.
func marshalJSONNoEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// newMCPServer builds the MCP server and installs the list/call handlers.
// port of src/index.ts:new Server(...)
func newMCPServer(version string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: version},
		&mcp.ServerOptions{
			// TS advertises capabilities.tools = {} (no listChanged).
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcpToolListResult{Tools: listToolsForRequest(ctx)}, nil
			case "tools/call":
				result, err := callToolForRequest(ctx, req)
				if err == nil {
					// Keep the statusline badge fresh in-process instead of
					// spawning `status --emit-statusline` per tool call.
					// Debounced and off the response path.
					statusline.Schedule()
				}
				return result, err
			default:
				return next(ctx, method, req)
			}
		}
	})
	return server
}

// listToolsForRequest mirrors the TS list handler: daemon list first when
// enabled (nil falls back), then the process profile filter.
// port of src/index.ts:server.setRequestHandler(ListToolsRequestSchema)
func listToolsForRequest(ctx context.Context) []types.ToolDefinition {
	tools := ListedTools()
	if daemon.DaemonEnabled() {
		if remote, err := daemon.GetDaemonClient().List(ctx); err == nil && remote != nil {
			tools = FilterByProfile(remote, func(tool types.ToolDefinition) string { return tool.Name })
		}
	}
	if tools == nil {
		return []types.ToolDefinition{}
	}
	return tools
}

// callToolForRequest mirrors the TS call handler: daemon first only when
// enabled; fall back locally only when the request was never sent.
// port of src/index.ts:server.setRequestHandler(CallToolRequestSchema)
func callToolForRequest(ctx context.Context, req mcp.Request) (mcp.Result, error) {
	params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
	if !ok || params == nil {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInvalidParams,
			Message: "missing tool call parameters",
		}
	}
	sessionID := sessionIDFromMeta(params.Meta)
	args, state, argsErr := decodeCallArguments(params.Arguments)
	if argsErr != nil {
		return nil, argsErr
	}

	if daemon.DaemonEnabled() {
		call := &callruntime.CallContext{
			SessionID: sessionID,
			Cwd:       processCwd(),
			Env:       callruntime.EnvFromEnviron(os.Environ()),
		}
		outcome, err := daemon.GetDaemonClient().Call(ctx, params.Name, args, call)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
		}
		if outcome.Sent {
			return toolCallResult(outcome.Result), nil
		}
	}
	result, err := callTool(ctx, params.Name, args, &callruntime.CallContext{SessionID: sessionID}, state)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return toolCallResult(result), nil
}

// toolCallResult converts one dispatch result without touching its blocks.
func toolCallResult(result types.ToolResult) *mcpToolCallResult {
	content := result.Content
	if content == nil {
		content = []types.Content{}
	}
	return &mcpToolCallResult{ResultBase: mcp.ResultBase{Meta: result.Meta}, Content: content, IsError: result.IsError}
}

// sessionIDFromMeta mirrors String(_meta?.sessionID ?? "default").
func sessionIDFromMeta(meta mcp.Meta) string {
	if meta == nil {
		return "default"
	}
	value, ok := meta["sessionID"]
	if !ok || value == nil {
		return "default"
	}
	return jsString(value)
}

// decodeCallArguments distinguishes an absent arguments value from an explicit
// null (TS undefined vs null) and decodes JSON objects into the dispatch map.
// TS's MCP SDK validates `arguments` as z.record() before dispatch, so a
// present non-record value is rejected with the same zod message.
func decodeCallArguments(raw json.RawMessage) (map[string]any, argsState, error) {
	if len(raw) == 0 {
		return nil, argsUndefined, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, argsUndefined, nil
	}
	switch trimmed[0] {
	case 'n':
		return nil, argsPresent, zodArgumentsError("null")
	case '[':
		return nil, argsPresent, zodArgumentsError("array")
	case '"':
		return nil, argsPresent, zodArgumentsError("string")
	case 't', 'f':
		return nil, argsPresent, zodArgumentsError("boolean")
	case '{':
		var args map[string]any
		if err := json.Unmarshal(trimmed, &args); err != nil || args == nil {
			return map[string]any{}, argsPresent, nil
		}
		return args, argsPresent, nil
	default:
		return nil, argsPresent, zodArgumentsError("number")
	}
}

// zodArgumentsError reproduces the TS MCP SDK's zod issue JSON for a
// non-record `arguments` value.
func zodArgumentsError(received string) error {
	issues := []struct {
		Expected string   `json:"expected"`
		Code     string   `json:"code"`
		Path     []string `json:"path"`
		Message  string   `json:"message"`
	}{{
		Expected: "record",
		Code:     "invalid_type",
		Path:     []string{"params", "arguments"},
		Message:  "Invalid input: expected record, received " + received,
	}}
	message, err := json.MarshalIndent(issues, "", "  ")
	if err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "Invalid arguments"}
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: string(message)}
}

// StartServer runs the MCP stdio server until the transport closes or ctx is
// canceled. stderr receives the same one-line boot message as TS; nothing is
// written to stdout except JSON-RPC frames.
// port of src/index.ts:startServer
func StartServer(ctx context.Context, version string, stdin io.Reader, stdout, stderr io.Writer) error {
	// A pending badge write must land when the transport closes.
	defer statusline.Flush()
	// With the daemon, housekeeping runs there; this process stays thin.
	if !daemon.DaemonEnabled() {
		// Probe env once so unknown Claude Code / subagent keys log at boot.
		compression.IsSubagentContext(context.Background())
		compression.PruneDeadSessions()
		_, _ = callruntime.PruneJobs()
		// Archive retention (24h), fire-and-forget like TS: a large first-run
		// backlog must not delay startup.
		go func() {
			defer func() { _ = recover() }()
			compression.CleanupArchives()
		}()
	}
	server := newMCPServer(version)
	transport := &mcp.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: nopWriteCloser{stdout},
	}
	fmt.Fprintln(stderr, bootLine)
	if err := server.Run(ctx, transport); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil
		}
		return err
	}
	return nil
}

// nopWriteCloser lets an io.Writer stand in for the transport's WriteCloser.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
