package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// toolsByName is the dispatch loader table. ToolNames keeps its insertion
// order; this map is only the O(1) lookup TS gets from `loaders`.
var toolsByName = map[string]types.ToolDefinition{
	"cave__read":        AllTools[0],
	"cave__bash":        AllTools[1],
	"cave__bash_start":  AllTools[2],
	"cave__bash_status": AllTools[3],
	"cave__bash_stop":   AllTools[4],
	"cave__grep":        AllTools[5],
	"cave__find":        AllTools[6],
	"cave__ls":          AllTools[7],
	"cave__write":       AllTools[8],
	"cave__invalidate":  AllTools[9],
	"cave__edit":        AllTools[10],
	"cave__apply_patch": AllTools[11],
	"cave__websearch":   AllTools[12],
	"cave__webfetch":    AllTools[13],
	"cave__compress":    AllTools[14],
	"cave__status":      AllTools[15],
	"cave__configure":   AllTools[16],
}

// sessionTools are the tools whose handlers read the caller session id (dedup
// cache, job scoping).
// port of src/dispatch.ts:SESSION_TOOLS
var sessionTools = map[string]struct{}{
	"cave__read":        {},
	"cave__bash_start":  {},
	"cave__bash_status": {},
	"cave__bash_stop":   {},
}

// pathParams are made absolute against the caller's cwd so cache, snapshot and
// edit-registry keys never depend on which client (or cwd) made the call.
// port of src/dispatch.ts:PATH_PARAMS
var pathParams = map[string][]string{
	"cave__read":  {"file_path"},
	"cave__write": {"file_path"},
	"cave__edit":  {"file_path"},
	"cave__grep":  {"path"},
	"cave__find":  {"path"},
	"cave__ls":    {"path"},
}

// nilArgsProperty keeps TS's TypeError when dispatch passes null/undefined
// args to a handler that immediately reads a property. Go map lookups are
// nil-safe, so without this shim the same call would silently run with an
// empty argument object. Tools that validate nil themselves or never read args
// are absent from this table.
var nilArgsProperty = map[string]string{
	"cave__bash":        "command",
	"cave__write":       "file_path",
	"cave__invalidate":  "file_paths",
	"cave__apply_patch": "patchText",
	"cave__grep":        "pattern",
	"cave__find":        "pattern",
	"cave__ls":          "path",
	"cave__websearch":   "query",
	"cave__webfetch":    "url",
	"cave__compress":    "action",
	"cave__configure":   "action",
}

// argsState distinguishes an absent/null MCP arguments value from an empty
// object, because TS's TypeError text differs and Go's nil map cannot.
type argsState int

const (
	argsPresent argsState = iota
	argsUndefined
	argsNull
)

// jsThrownError carries the JavaScript error name so ObserveToolCall keeps
// TS's errorKind (TypeError vs Error).
type jsThrownError struct {
	name    string
	message string
}

func (e *jsThrownError) Error() string { return e.message }

// ErrorName implements the optional naming hook used below.
func (e *jsThrownError) ErrorName() string { return e.name }

// init wires the shared runtime hooks at dispatch initialization. runtime
// cannot import compression, so dispatch supplies IsSubagentContext here.
// port of src/dispatch.ts:setKnownToolNames (module initialization)
func init() {
	runtime.SetKnownToolNames(ToolNames)
	runtime.SetSubagentDetector(compression.IsSubagentContext)
}

// IsToolName reports whether name is one of the 17 registered tools.
// port of src/dispatch.ts:isToolName
func IsToolName(name string) bool {
	_, ok := toolsByName[name]
	return ok
}

// LoadTool returns one tool definition or the same unknown-tool error TS
// throws. Raw caller-supplied names stay only in this returned error.
// port of src/dispatch.ts:loadTool
func LoadTool(name string) (types.ToolDefinition, error) {
	tool, ok := toolsByName[name]
	if !ok {
		return types.ToolDefinition{}, fmt.Errorf("Unknown tool: %s", name)
	}
	return tool, nil
}

// CallTool is the shared dispatch for the MCP server and the CLI.
// port of src/dispatch.ts:callTool
func CallTool(ctx context.Context, name string, args map[string]any, partial *runtime.CallContext) (types.ToolResult, error) {
	state := argsPresent
	if args == nil {
		state = argsUndefined
	}
	return callTool(ctx, name, args, partial, state)
}

// callTool is CallTool plus the absent/null distinction the MCP layer knows.
func callTool(ctx context.Context, name string, args map[string]any, partial *runtime.CallContext, state argsState) (types.ToolResult, error) {
	call := resolveCallContext(partial)
	namespace := call.SessionNamespace
	if namespace == "" {
		namespace = call.ClientID
	}
	sessionID := call.SessionID
	if namespace != "local" {
		sessionID = namespace + ":" + call.SessionID
	}
	startedAt := time.Now().UnixMilli()

	tool, err := LoadTool(name)
	if err != nil {
		unknownCall := *call
		unknownCall.SessionID = sessionID
		// Unknown tool: no handler ran. Observe with the same caller context
		// (env + namespaced session id) as normal failures, so a daemon
		// client's CAVE_TOOLS_ERROR_LOG=0 is honored here too. Throw unchanged.
		runtime.RunWithContext(ctx, &unknownCall, func(runCtx context.Context) struct{} {
			runtime.ObserveToolCall(runCtx, runtime.ObserveToolCallOptions{
				Tool:        name,
				Args:        args,
				SchemaProps: nil,
				SessionID:   sessionID,
				StartedAt:   startedAt,
				Env:         call.Env,
				Thrown:      &runtime.ThrownError{Name: "Error", Message: err.Error()},
			})
			return struct{}{}
		})
		return types.ToolResult{}, err
	}

	normalized := normalizePaths(name, args, call.Cwd)
	if state != argsPresent {
		if property, throws := nilArgsProperty[name]; throws {
			kind := "undefined"
			if state == argsNull {
				kind = "null"
			}
			thrown := &jsThrownError{
				name:    "TypeError",
				message: "Cannot read properties of " + kind + " (reading '" + property + "')",
			}
			callContext := *call
			callContext.SessionID = sessionID
			runtime.RunWithContext(ctx, &callContext, func(runCtx context.Context) struct{} {
				runtime.ObserveToolCall(runCtx, runtime.ObserveToolCallOptions{
					Tool:        name,
					Args:        args,
					SchemaProps: schemaPropertyNames(tool.InputSchema),
					SessionID:   sessionID,
					StartedAt:   startedAt,
					Env:         call.Env,
					Thrown:      &runtime.ThrownError{Name: "TypeError", Message: thrown.message},
				})
				return struct{}{}
			})
			return types.ToolResult{}, thrown
		}
	}
	toolArgs := normalized
	if _, inject := sessionTools[name]; inject {
		if toolArgs == nil {
			toolArgs = map[string]any{}
		} else {
			toolArgs = cloneMap(toolArgs)
		}
		toolArgs["__sessionId"] = sessionID
	}
	schemaProps := schemaPropertyNames(tool.InputSchema)

	callContext := *call
	callContext.SessionID = sessionID
	outcome := runtime.RunWithContext(ctx, &callContext, func(runCtx context.Context) handlerOutcome {
		result, handlerErr := tool.Handler(runCtx, toolArgs)
		options := runtime.ObserveToolCallOptions{
			Tool:        name,
			Args:        toolArgs,
			SchemaProps: schemaProps,
			SessionID:   sessionID,
			StartedAt:   startedAt,
			Env:         call.Env,
		}
		if handlerErr != nil {
			options.Thrown = &runtime.ThrownError{Name: thrownName(handlerErr), Message: handlerErr.Error()}
			runtime.ObserveToolCall(runCtx, options)
			// TS handlers throw; no partial result is observable.
			return handlerOutcome{err: handlerErr}
		}
		options.Result = result
		runtime.ObserveToolCall(runCtx, options)
		return handlerOutcome{result: result}
	})
	return outcome.result, outcome.err
}

// handlerOutcome carries the two handler return values through RunWithContext,
// whose single type parameter cannot express a Go multi-value return.
type handlerOutcome struct {
	result types.ToolResult
	err    error
}

// resolveCallContext applies TS's defaults for omitted partial fields. A nil
// Env means omitted; a non-nil empty map stays empty.
func resolveCallContext(partial *runtime.CallContext) *runtime.CallContext {
	if partial == nil {
		return &runtime.CallContext{
			Cwd:      processCwd(),
			Env:      runtime.EnvFromEnviron(os.Environ()),
			ClientID: "local",
		}
	}
	call := *partial
	if call.Cwd == "" {
		call.Cwd = processCwd()
	}
	if call.Env == nil {
		call.Env = runtime.EnvFromEnviron(os.Environ())
	}
	if call.ClientID == "" {
		call.ClientID = "local"
	}
	return &call
}

// processCwd is process.cwd(); Go returns "" when the process cwd is gone
// (the documented W0 deviation).
func processCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// absolutize mirrors `path.isAbsolute(value) ? value : path.resolve(cwd, value)`
// for string values. Non-strings and the empty string pass through untouched.
// port of src/dispatch.ts:absolutize
func absolutize(value any, cwd string) any {
	text, ok := value.(string)
	if !ok || text == "" || filepath.IsAbs(text) {
		return value
	}
	return filepath.Join(cwd, text)
}

// normalizePaths returns a shallow copy of args with cwd-relative path params
// made absolute. The caller's map is never mutated.
// port of src/dispatch.ts:normalizePaths
func normalizePaths(name string, args map[string]any, cwd string) map[string]any {
	if args == nil {
		return nil
	}
	out := cloneMap(args)
	for _, key := range pathParams[name] {
		if value, ok := out[key]; ok {
			out[key] = absolutize(value, cwd)
		}
	}
	if name == "cave__edit" {
		if edits, ok := out["edits"].([]any); ok {
			normalized := make([]any, len(edits))
			for i, item := range edits {
				record, isRecord := item.(map[string]any)
				if !isRecord {
					normalized[i] = item
					continue
				}
				if _, hasPath := record["file_path"]; !hasPath {
					normalized[i] = item
					continue
				}
				copied := cloneMap(record)
				copied["file_path"] = absolutize(record["file_path"], cwd)
				normalized[i] = copied
			}
			out["edits"] = normalized
		}
	}
	if name == "cave__invalidate" {
		switch paths := out["file_paths"].(type) {
		case []any:
			normalized := make([]any, len(paths))
			for i, item := range paths {
				normalized[i] = absolutize(item, cwd)
			}
			out["file_paths"] = normalized
		case []string:
			normalized := make([]string, len(paths))
			for i, item := range paths {
				normalized[i] = absolutize(item, cwd).(string)
			}
			out["file_paths"] = normalized
		}
	}
	return out
}

// cloneMap copies one JSON object shallowly.
func cloneMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// schemaPropertyNames lists inputSchema.properties keys in declaration order,
// matching Object.keys() over the decoded schema object.
func schemaPropertyNames(schema json.RawMessage) []string {
	var root map[string]json.RawMessage
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil {
		return nil
	}
	properties, ok := root["properties"]
	if !ok || len(properties) == 0 || string(properties) == "null" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(properties))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}
	names := make([]string, 0, 4)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return names
		}
		name, ok := key.(string)
		if !ok {
			return names
		}
		names = append(names, name)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return names
		}
	}
	return names
}

// jsString mirrors the ECMAScript ToString abstract operation for the values
// JSON can carry. _meta.sessionID is coerced with String(...) in TS.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(v)
	case float32:
		return jsNumberString(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return string(v)
		}
		return jsNumberString(number)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item == nil {
				continue
			}
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(v, ",")
	default:
		return "[object Object]"
	}
}

// jsNumberString mirrors ECMAScript Number::toString(10) for JSON numbers.
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

// thrownName reports the JS error constructor name a Go error stands in for.
func thrownName(err error) string {
	var named interface{ ErrorName() string }
	if errors.As(err, &named) {
		return named.ErrorName()
	}
	return "Error"
}
