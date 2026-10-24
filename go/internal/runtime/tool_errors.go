// Privacy-safe persistent log of tool error outcomes (best-effort).
//
// What this module does:
//   - Records tool calls that end in a returned `isError` result ("soft"
//     error) or a handler throw ("thrown" error) at the shared dispatch
//     layer. Successful calls are never recorded.
//   - Never changes what the caller sees: results and throws pass through
//     untouched. Classification is transient (in memory only).
//   - Best-effort and bounded: a busy or unwritable store only drops
//     records, never breaks a tool call. There is no guarantee every
//     failure is durably recorded.
//
// Privacy rules (hard requirements, not aspirations):
//   - NEVER stored: raw error messages, stacks, prompts, tool result bodies,
//     file contents, commands, env values, full paths, arbitrary unknown
//     argument key strings, or arbitrary tool/mode strings supplied by the
//     caller (they could literally be secrets). Tool names pass through a
//     fixed known allowlist (anything else becomes "unknown_tool"); mode is
//     a finite set (anything else becomes "default").
//   - Stored per event: timestamp, event id, allowlisted tool name, outcome
//     (`soft`/`thrown`), a stable finite reason code, elapsedMs,
//     mode/operation shape, batch index parsed from anchored `edits[N]`
//     prefixes only, sha256-truncated session/file correlation hashes,
//     allowlisted argument keys plus their JS types/counts, and (for throws)
//     the allowlisted error kind.
//   - Agent attribution is never guessed: "unknown" unless the supported
//     coarse subagent signal (compression/utils isSubagentContext) fires.
//
// Reliability rules:
//   - Only bounded in-memory work (arg summary, hashes, one queue slot) runs
//     on the tool-call path. SQLite I/O happens on later turns: one bounded
//     batch (<=50 rows) per flush turn, no timers, no retry spin — a locked
//     store defers to the next tool event. Expect a small best-effort delay
//     (and possible loss under lock or shutdown), never a stall: lock wait is
//     zero and failures are counted with process-level give-up.
//   - Flush is scheduled asynchronously so the resolved tool result is
//     delivered before the sink does its first write. For short-lived CLI
//     processes, FlushToolErrorQueue is called at the controlled exit funnel
//     (cli-tools finish); tests drain explicitly.
//   - Disable policy (OR): a call is dropped when the caller env disables
//     logging (per-client override, so a daemon client's
//     CAVE_TOOLS_ERROR_LOG=0 always wins for that client) OR when the
//     process env disables it at flush time. A record gated-in at event
//     time may still be dropped if the process disables before flush; a
//     disabled call is never recorded. Raw env values are never stored
//     (mode is a finite allowlist).
//   - Storage: additive `tool_errors` table in the existing SQLite db.
//     The db file is pre-created 0600 and existing dirs/files/sidecars are
//     tightened to 0700/0600 without following symlinks; symlink or
//     non-regular db paths refuse logging for that directory.
//   - Retention: 7 days, 5000 rows max, enforced on successful flush.
//
// port of src/runtime/tool-errors.ts
package runtime

import (
	"context"
	"crypto/rand"
	cryptosha256 "crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"modernc.org/sqlite"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// port of src/runtime/tool-errors.ts:TOOL_ERROR_RETENTION_DAYS
const ToolErrorRetentionDays = 7

// port of src/runtime/tool-errors.ts:TOOL_ERROR_MAX_ROWS
const ToolErrorMaxRows = 5000

// port of src/runtime/tool-errors.ts:TOOL_ERROR_QUEUE_CAP
const ToolErrorQueueCap = 256

// port of src/runtime/tool-errors.ts:TOOL_ERROR_FLUSH_BATCH
const toolErrorFlushBatch = 50

// port of src/runtime/tool-errors.ts:TOOL_ERROR_MAX_FLUSH_FAILURES
const toolErrorMaxFlushFailures = 5

// port of src/runtime/tool-errors.ts:SESSION_HASH_LEN
const sessionHashLen = 16

// port of src/runtime/tool-errors.ts:FILE_HASH_LEN
const fileHashLen = 16

// ToolErrorOutcome is "soft" (returned isError) or "thrown" (handler throw).
// port of src/runtime/tool-errors.ts:ToolErrorOutcome
type ToolErrorOutcome string

const (
	// ToolErrorSoft is a returned isError result.
	ToolErrorSoft ToolErrorOutcome = "soft"
	// ToolErrorThrown is a thrown handler error.
	ToolErrorThrown ToolErrorOutcome = "thrown"
)

// ToolErrorReasonCode is the stable finite classification stored per event.
// port of src/runtime/tool-errors.ts:ToolErrorReasonCode
type ToolErrorReasonCode string

const (
	ReasonInvalidArgs ToolErrorReasonCode = "invalid_args"
	ReasonNotFound    ToolErrorReasonCode = "not_found"
	ReasonUnreadable  ToolErrorReasonCode = "unreadable"
	ReasonStaleHash   ToolErrorReasonCode = "stale_hash"
	ReasonStaleRange  ToolErrorReasonCode = "stale_range"
	ReasonStaleWrite  ToolErrorReasonCode = "stale_write"
	ReasonNotUnique   ToolErrorReasonCode = "not_unique"
	ReasonOverlap     ToolErrorReasonCode = "overlap"
	ReasonConflict    ToolErrorReasonCode = "conflict"
	ReasonExecFailed  ToolErrorReasonCode = "exec_failed"
	ReasonUpstream    ToolErrorReasonCode = "upstream"
	ReasonInternal    ToolErrorReasonCode = "internal"
	ReasonUnknown     ToolErrorReasonCode = "unknown"
)

// reasonCodes is the allowlist used when reading rows back.
// port of src/runtime/tool-errors.ts:REASON_CODES
var reasonCodes = map[string]bool{
	"invalid_args": true,
	"not_found":    true,
	"unreadable":   true,
	"stale_hash":   true,
	"stale_range":  true,
	"stale_write":  true,
	"not_unique":   true,
	"overlap":      true,
	"conflict":     true,
	"exec_failed":  true,
	"upstream":     true,
	"internal":     true,
	"unknown":      true,
}

// ToolErrorRecord is one stored tool-error event.
// port of src/runtime/tool-errors.ts:ToolErrorRecord
type ToolErrorRecord struct {
	ID              string              `json:"id"`
	CreatedAt       int64               `json:"createdAt"`
	Tool            string              `json:"tool"`
	Outcome         ToolErrorOutcome    `json:"outcome"`
	ReasonCode      ToolErrorReasonCode `json:"reasonCode"`
	ElapsedMs       int64               `json:"elapsedMs"`
	Mode            string              `json:"mode"`
	Operation       *string             `json:"operation"`
	BatchIndex      *int64              `json:"batchIndex"`
	Agent           string              `json:"agent"`
	SessionHash     string              `json:"sessionHash"`
	FileHash        *string             `json:"fileHash"`
	ArgKeys         []string            `json:"argKeys"`
	ArgTypes        map[string]string   `json:"argTypes"`
	ArgCount        int64               `json:"argCount"`
	UnknownArgCount int64               `json:"unknownArgCount"`
	ErrorKind       *string             `json:"errorKind"`
}

// queuedError is a record stamped with the data dir it belongs to.
// port of src/runtime/tool-errors.ts:QueuedError
type queuedError struct {
	ToolErrorRecord
	storeDir string
}

// storeBusy marks a locked (not broken) store: the flush defers without
// counting a failure.
// port of src/runtime/tool-errors.ts:StoreBusy
var errStoreBusy = errors.New("store busy")

// --- Caller-controlled string sanitization ---

// knownToolNames is the fixed allowlist, registered once by dispatch.
// port of src/runtime/tool-errors.ts:knownToolNames
var (
	knownToolNamesMu sync.Mutex
	knownToolNames   = map[string]bool{}
)

// SetKnownToolNames installs the dispatch tool-name allowlist. Called once by
// dispatch with its tool names. Not for general use.
// port of src/runtime/tool-errors.ts:setKnownToolNames
func SetKnownToolNames(names []string) {
	next := make(map[string]bool, len(names))
	for _, name := range names {
		next[name] = true
	}
	knownToolNamesMu.Lock()
	knownToolNames = next
	knownToolNamesMu.Unlock()
}

// SanitizeToolName keeps an allowlisted tool name and maps everything else to
// "unknown_tool", never storing the raw caller string.
// port of src/runtime/tool-errors.ts:sanitizeToolName
func SanitizeToolName(name any) string {
	value, ok := name.(string)
	if !ok {
		return "unknown_tool"
	}
	knownToolNamesMu.Lock()
	known := knownToolNames[value]
	knownToolNamesMu.Unlock()
	if known {
		return value
	}
	return "unknown_tool"
}

// knownModes is the finite mode set; anything else becomes "default".
// port of src/runtime/tool-errors.ts:KNOWN_MODES
var knownModes = map[string]bool{"off": true, "hint": true, "enforce": true, "strict": true}

// SanitizeMode keeps an allowlisted mode and maps everything else to
// "default" — the current mode is never guessed.
// port of src/runtime/tool-errors.ts:sanitizeMode
func SanitizeMode(raw any) string {
	value, ok := raw.(string)
	if ok && knownModes[value] {
		return value
	}
	return "default"
}

// disabledValues are the env spellings that turn logging off.
// port of src/runtime/tool-errors.ts:DISABLED_VALUES
var disabledValues = map[string]bool{"0": true, "false": true, "off": true, "no": true}

// envString resolves a variable with TS's `caller ?? process.env` precedence:
// a caller env entry (even empty) wins, otherwise the process env is read.
func envString(env map[string]string, key string) string {
	if env != nil {
		if value, ok := env[key]; ok {
			return value
		}
	}
	return os.Getenv(key)
}

// IsToolErrorLoggingEnabled reports whether logging is enabled. Caller env
// wins (daemon per-client env); falls back to process env.
// port of src/runtime/tool-errors.ts:isToolErrorLoggingEnabled
func IsToolErrorLoggingEnabled(env map[string]string) bool {
	raw := strings.ToLower(jsre.Trim(envString(env, "CAVE_TOOLS_ERROR_LOG")))
	return !disabledValues[raw]
}

// Regexes compiled from their TS sources through jsre.
var (
	// port of src/runtime/tool-errors.ts:stripErrorPrefix
	errorPrefixRE = jsre.MustCompile(`^\s*error\s*:\s*`, "i")
	// port of src/runtime/tool-errors.ts:parseBatchIndex
	batchIndexRE = jsre.MustCompile(`^edits\[(\d+)\]:`, "")
	// port of src/runtime/tool-errors.ts:classifySoftReasonCode
	batchRemainderRE = jsre.MustCompile(`^edits\[\d+\]:\s*`, "")
)

// firstText returns the first text block of a tool result, bounded to
// maxChars UTF-16 code units. The shape is caller-controlled, so every type
// assertion is checked.
// port of src/runtime/tool-errors.ts:firstText
func firstText(result any, maxChars ...int) string {
	limit := 1000
	if len(maxChars) > 0 {
		limit = maxChars[0]
	}
	text, ok := firstTextBlock(result)
	if !ok {
		return ""
	}
	return jsSliceUTF16(text, limit)
}

// firstTextBlock extracts the first text block from the supported result
// shapes (types.ToolResult, *types.ToolResult and a decoded map).
func firstTextBlock(result any) (string, bool) {
	switch value := result.(type) {
	case types.ToolResult:
		return textFromBlocks(value.Content)
	case *types.ToolResult:
		if value == nil {
			return "", false
		}
		return textFromBlocks(value.Content)
	case map[string]any:
		content, ok := value["content"].([]any)
		if !ok {
			return "", false
		}
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] != "text" {
				continue
			}
			text, ok := block["text"].(string)
			if ok {
				return text, true
			}
		}
	}
	return "", false
}

// textFromBlocks returns the first text content block.
func textFromBlocks(content []types.Content) (string, bool) {
	for _, block := range content {
		if text, ok := block.(types.TextContent); ok {
			return text.Text, true
		}
	}
	return "", false
}

// jsSliceUTF16 mirrors String.prototype.slice(0, end) over UTF-16 code units.
func jsSliceUTF16(s string, end int) string {
	if end <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		if units >= end {
			return s[:i]
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return s
}

// stripErrorPrefix removes a leading "error:" label (case-insensitive).
// port of src/runtime/tool-errors.ts:stripErrorPrefix
func stripErrorPrefix(text string) string {
	return errorPrefixRE.ReplaceAllString(text, "")
}

// ParseBatchIndex parses only the anchored generated batch prefix `edits[N]:`
// at the start of the (prefix-stripped) text. Anything else yields nil.
// port of src/runtime/tool-errors.ts:parseBatchIndex
func ParseBatchIndex(text string) *int64 {
	trimmed := jsre.TrimStart(stripErrorPrefix(text))
	match := batchIndexRE.FindStringSubmatch(trimmed)
	if match == nil {
		return nil
	}
	n, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil || n > 9007199254740991 {
		return nil
	}
	value := int64(n)
	return &value
}

// ClassifySoftReasonCode classifies a returned soft-error body. The input text
// is never persisted, only the returned finite code. Anchored fixed generated
// prefixes are checked before general substring text, so content windows
// embedded later in the message cannot change the classification.
// port of src/runtime/tool-errors.ts:classifySoftReasonCode
func ClassifySoftReasonCode(tool string, text string) ToolErrorReasonCode {
	t := jsre.TrimStart(stripErrorPrefix(text))
	if t == "" {
		return ReasonUnknown
	}
	anchored := strings.ToLower(t)

	// 1. Anchored batch remainder: `edits[N]: <inner>` classifies on <inner>.
	batch := batchRemainderRE.FindString(anchored)
	body := anchored
	if batch != "" {
		body = anchored[len(batch):]
	}
	isBash := tool == "cave__bash" || tool == "cave__bash_status"
	isNet := tool == "cave__webfetch" || tool == "cave__websearch"

	// 2. Anchored fixed generated prefixes (edit stale/not-unique/not-found).
	if strings.HasPrefix(body, "expected_hash mismatch") {
		return ReasonStaleHash
	}
	if strings.HasPrefix(body, "expected_range_checksum mismatch") {
		return ReasonStaleRange
	}
	if strings.HasPrefix(body, "hash failed") {
		return ReasonStaleHash
	}
	if strings.HasPrefix(body, "checksum failed") {
		return ReasonStaleRange
	}
	if strings.HasPrefix(body, "old_string is not unique") {
		return ReasonNotUnique
	}
	if strings.HasPrefix(body, "old_string not found") {
		return ReasonNotFound
	}
	if strings.HasPrefix(body, "edits overlap") || strings.HasPrefix(body, "cannot mix move and range") {
		return ReasonOverlap
	}
	if strings.HasPrefix(body, "file changed after it was read") {
		return ReasonStaleWrite
	}
	if strings.HasPrefix(body, "expected_hash must be") ||
		strings.HasPrefix(body, "expected_range_checksum must be") ||
		strings.HasPrefix(body, "expected_hash must be hex") ||
		strings.HasPrefix(body, "strict:") {
		return ReasonInvalidArgs
	}
	if strings.HasPrefix(body, "file not found") ||
		strings.HasPrefix(body, "path not found") ||
		strings.HasPrefix(body, "unknown job") ||
		strings.HasPrefix(body, "unknown archive") ||
		strings.HasPrefix(body, "no such file") ||
		strings.HasPrefix(body, "no such directory") ||
		strings.HasPrefix(body, "does not exist") ||
		strings.HasPrefix(body, "not a directory") {
		return ReasonNotFound
	}

	// 3. Bash exit markers are wrapper-generated, never user output.
	if isBash && (strings.Contains(body, "[exit:") ||
		strings.Contains(body, "[timed out") ||
		strings.Contains(body, "[killed by signal")) {
		return ReasonExecFailed
	}

	// 4. General substring checks (unanchored text).
	has := func(needles ...string) bool {
		for _, needle := range needles {
			if strings.Contains(body, needle) {
				return true
			}
		}
		return false
	}
	if has("expected_hash mismatch", "hash failed") {
		return ReasonStaleHash
	}
	if has("expected_range_checksum mismatch", "checksum failed") {
		return ReasonStaleRange
	}
	if has("changed after it was read") {
		return ReasonStaleWrite
	}
	if has("is not unique") {
		return ReasonNotUnique
	}
	if has("overlap", "cannot mix move and range") {
		return ReasonOverlap
	}
	if has(
		"not found",
		"unknown job",
		"unknown archive",
		"no such file",
		"no such directory",
		"does not exist",
		"not a directory",
		"enoent",
	) {
		return ReasonNotFound
	}
	if has(
		"is required",
		"must be a",
		"must provide",
		"must supply",
		"invalid",
		"malformed",
		"missing",
		"out of range",
		"not a valid",
		"cannot be empty",
		"must not be empty",
		"expected",
		"file_paths",
		"usage:",
		"must be hex",
		"must be integers",
		"array is empty",
		"strict proof",
	) {
		return ReasonInvalidArgs
	}
	if has(
		"not valid utf-8",
		"is a directory",
		"permission",
		"denied",
		"eacces",
		"eperm",
		"too large",
		"binary",
		"unsupported fetched",
	) {
		return ReasonUnreadable
	}
	if isNet && has("request failed", "http ", "status ", "cloudflare", "timed out", "network", "fetch") {
		return ReasonUpstream
	}
	if has("exit:", "timed out", "killed by signal", "non-zero", "command failed", "exited(") {
		return ReasonExecFailed
	}
	if has("already exists", "ambiguous", "multiple matches") {
		return ReasonConflict
	}
	if has("request failed", "cloudflare", "response too large", "network error") {
		return ReasonUpstream
	}
	return ReasonUnknown
}

// thrownKinds is the allowlisted error kind set.
// port of src/runtime/tool-errors.ts:THROWN_KINDS
var thrownKinds = map[string]bool{
	"Error":                    true,
	"TypeError":                true,
	"RangeError":               true,
	"SyntaxError":              true,
	"ReferenceError":           true,
	"CloudflareChallengeError": true,
}

// NormalizeThrownKind keeps an allowlisted error kind name.
// port of src/runtime/tool-errors.ts:normalizeThrownKind
func NormalizeThrownKind(name any) string {
	value, ok := name.(string)
	if ok && thrownKinds[value] {
		return value
	}
	return "Error"
}

// ClassifyThrownReasonCode classifies a handler throw. Transient only:
// name/message are never stored, only the returned finite code (plus the
// allowlisted kind separately).
// port of src/runtime/tool-errors.ts:classifyThrownReasonCode
func ClassifyThrownReasonCode(tool string, name string, message string) ToolErrorReasonCode {
	_ = tool
	t := strings.ToLower(name + " " + message)
	if strings.Contains(t, "cloudflare") {
		return ReasonUpstream
	}
	if strings.Contains(t, "unknown tool") {
		return ReasonInternal
	}
	if strings.Contains(t, "changed after it was read") {
		return ReasonStaleWrite
	}
	if strings.Contains(t, "cannot read propert") ||
		strings.Contains(t, "cannot read property") ||
		strings.Contains(t, "undefined") ||
		strings.Contains(t, "not a function") ||
		strings.Contains(t, "missing") ||
		strings.Contains(t, "required") ||
		strings.Contains(t, "invalid") ||
		strings.Contains(t, "malformed") ||
		strings.Contains(t, "out of range") ||
		strings.Contains(t, "expected") {
		// Covers the missing-args crash shape (TypeError on undefined).
		return ReasonInvalidArgs
	}
	return ReasonInternal
}

// jsType mirrors JavaScript's typeof/Array.isArray/null naming.
// port of src/runtime/tool-errors.ts:jsType
func jsType(value any) string {
	if value == nil {
		return "null"
	}
	switch value.(type) {
	case []any, []string, []int, []int64, []float64, []bool:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float32, float64, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return "number"
	case func():
		return "function"
	}
	kind := reflect.ValueOf(value).Kind()
	if kind == reflect.Slice || kind == reflect.Array {
		return "array"
	}
	return "object"
}

// ArgSummary is the allowlisted argument summary stored per event.
// port of src/runtime/tool-errors.ts:summarizeArgs
type ArgSummary struct {
	Keys         []string          `json:"keys"`
	Types        map[string]string `json:"types"`
	Count        int64             `json:"count"`
	UnknownCount int64             `json:"unknownCount"`
}

// SummarizeArgs summarizes call arguments against the tool's own inputSchema
// property names (the allowlist). Unknown key strings are counted, never
// stored.
// port of src/runtime/tool-errors.ts:summarizeArgs
func SummarizeArgs(args map[string]any, schemaProps []string) ArgSummary {
	out := ArgSummary{Keys: []string{}, Types: map[string]string{}}
	if args == nil {
		return out
	}
	allow := make(map[string]bool, len(schemaProps))
	for _, prop := range schemaProps {
		allow[prop] = true
	}
	for key := range args {
		if strings.HasPrefix(key, "__") {
			continue
		}
		out.Count++
	}
	for key, value := range args {
		if strings.HasPrefix(key, "__") {
			continue // dispatch-injected internals
		}
		if !allow[key] {
			out.UnknownCount++
			continue
		}
		out.Keys = append(out.Keys, key)
		out.Types[key] = jsType(value)
	}
	jsSortStrings(out.Keys)
	return out
}

// DeriveOperation returns the coarse operation shape for tools with
// meaningful sub-modes. Values only, never argument contents.
// port of src/runtime/tool-errors.ts:deriveOperation
func DeriveOperation(tool string, args map[string]any) *string {
	if args == nil {
		return nil
	}
	if tool == "cave__edit" {
		if _, ok := jsArrayValues(args["edits"]); ok {
			return stringPtr("batch")
		}
		if value, ok := args["delete"].(bool); ok && value {
			return stringPtr("delete")
		}
		if value, ok := args["insert_before"]; ok && value != nil {
			return stringPtr("move")
		}
		if _, ok := args["start_line"]; ok {
			return stringPtr("range")
		}
		if _, ok := args["end_line"]; ok {
			return stringPtr("range")
		}
		if _, ok := args["old_string"].(string); ok {
			return stringPtr("string")
		}
		return stringPtr("unknown")
	}
	if tool == "cave__read" {
		_, hasOffset := args["offset"]
		_, hasLimit := args["limit"]
		if hasOffset || hasLimit {
			return stringPtr("paged")
		}
		return stringPtr("full")
	}
	return nil
}

// jsArrayValues reports the elements of a JS array value.
func jsArrayValues(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	if values, ok := value.([]any); ok {
		return values, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	values := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		values[i] = rv.Index(i).Interface()
	}
	return values, true
}

func stringPtr(value string) *string { return &value }

// sha16 returns the first 16 hex chars of sha256(value).
// port of src/runtime/tool-errors.ts:sha16
func sha16(value string) string {
	sum := cryptosha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:sessionHashLen]
}

// SessionHashFor hashes the session id for correlation without storing it.
// port of src/runtime/tool-errors.ts:sessionHashFor
func SessionHashFor(sessionID string) string {
	return sha16(sessionID)
}

// FileHashFor hashes path-valued arguments only (file_path, path,
// file_paths[], edits[].file_path). Values are hashed, never stored.
// port of src/runtime/tool-errors.ts:fileHashFor
func FileHashFor(args map[string]any) *string {
	if args == nil {
		return nil
	}
	paths := make([]string, 0, 2)
	for _, key := range []string{"file_path", "path"} {
		if value, ok := args[key].(string); ok && value != "" {
			paths = append(paths, value)
		}
	}
	if multi, ok := jsArrayValues(args["file_paths"]); ok {
		for _, raw := range multi {
			if value, ok := raw.(string); ok && value != "" {
				paths = append(paths, value)
			}
		}
	}
	if edits, ok := jsArrayValues(args["edits"]); ok {
		for _, item := range edits {
			if item == nil {
				continue
			}
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := object["file_path"].(string); ok && value != "" {
				paths = append(paths, value)
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	jsSortStrings(paths)
	hash := cryptosha256.Sum256([]byte(strings.Join(paths, "\n")))
	value := hex.EncodeToString(hash[:])[:fileHashLen]
	return &value
}

// jsSortStrings sorts like Array.prototype.sort's default UTF-16 code unit
// order (Go's byte order differs for supplementary characters).
func jsSortStrings(values []string) {
	sort.SliceStable(values, func(i, j int) bool {
		return jsStringLess(values[i], values[j])
	})
}

func jsStringLess(a string, b string) bool {
	aUnits := utf16.Encode([]rune(a))
	bUnits := utf16.Encode([]rune(b))
	for i := 0; i < len(aUnits) && i < len(bUnits); i++ {
		if aUnits[i] != bUnits[i] {
			return aUnits[i] < bUnits[i]
		}
	}
	return len(aUnits) < len(bUnits)
}

// subagentDetector is the coarse subagent signal. runtime cannot import
// compression (compression imports runtime), so dispatch wires
// compression.IsSubagentContext here; nil keeps attribution at "unknown".
var (
	subagentDetectorMu sync.Mutex
	subagentDetector   func(context.Context) bool
)

// SetSubagentDetector installs the coarse subagent signal used for agent
// attribution. TS calls compression/utils isSubagentContext directly.
func SetSubagentDetector(detect func(context.Context) bool) {
	subagentDetectorMu.Lock()
	subagentDetector = detect
	subagentDetectorMu.Unlock()
}

// agentHint returns "subagent" when the supported env signal fires, otherwise
// "unknown". Never a model/agent identity (unavailable).
// port of src/runtime/tool-errors.ts:agentHint
func agentHint(ctx context.Context) string {
	subagentDetectorMu.Lock()
	detect := subagentDetector
	subagentDetectorMu.Unlock()
	if detect != nil && detect(ctx) {
		return "subagent"
	}
	return "unknown"
}

// --- Bounded best-effort SQLite sink ---

var (
	toolErrorsMu sync.Mutex
	// port of src/runtime/tool-errors.ts:queue
	toolErrorQueue []queuedError
	// port of src/runtime/tool-errors.ts:flushScheduled
	toolErrorFlushScheduled bool
	// port of src/runtime/tool-errors.ts:consecutiveFailures
	toolErrorConsecutiveFailures int
	// port of src/runtime/tool-errors.ts:storeDead
	toolErrorStoreDead bool
	// port of src/runtime/tool-errors.ts:connections
	toolErrorConnections = map[string]*sql.DB{}
	// toolErrorConnOrder is the FIFO eviction order for connections.
	toolErrorConnOrder []string

	// autoFlushDisabledForTests makes the asynchronous sink deterministic in
	// tests (Go has no setImmediate turn to synchronize with).
	autoFlushDisabledForTests bool
)

// closeConnections closes every cached connection. Best-effort.
// port of src/runtime/tool-errors.ts:closeConnections
func closeConnections() {
	for _, db := range toolErrorConnections {
		_ = db.Close()
	}
	toolErrorConnections = map[string]*sql.DB{}
	toolErrorConnOrder = nil
}

// isBusyError reports a locked (not broken) store.
// port of src/runtime/tool-errors.ts:isBusyError
func isBusyError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code() & 0xff
		return code == 5 || code == 6 // SQLITE_BUSY, SQLITE_LOCKED
	}
	return false
}

// loggingDSN opens the shared db with TS's logging pragmas: WAL, NORMAL sync
// and zero lock wait from the start.
func loggingDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	for _, pragma := range []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"busy_timeout(0)",
	} {
		query.Add("_pragma", pragma)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// readOnlyDSN opens the shared db for the CLI reader.
func readOnlyDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(0)")
	u.RawQuery = query.Encode()
	return u.String()
}

// toolErrorsSchemaSQL is byte-identical to the TS schema.
const toolErrorsSchemaSQL = `
      CREATE TABLE IF NOT EXISTS tool_errors (
        id TEXT PRIMARY KEY,
        created_at INTEGER NOT NULL,
        tool TEXT NOT NULL,
        outcome TEXT NOT NULL,
        reason_code TEXT NOT NULL,
        elapsed_ms INTEGER NOT NULL,
        mode TEXT NOT NULL DEFAULT 'unknown',
        operation TEXT,
        batch_index INTEGER,
        agent TEXT NOT NULL DEFAULT 'unknown',
        session_hash TEXT NOT NULL,
        file_hash TEXT,
        arg_keys TEXT NOT NULL DEFAULT '[]',
        arg_types TEXT NOT NULL DEFAULT '{}',
        arg_count INTEGER NOT NULL DEFAULT 0,
        unknown_arg_count INTEGER NOT NULL DEFAULT 0,
        error_kind TEXT
      );
      CREATE INDEX IF NOT EXISTS idx_tool_errors_created_at ON tool_errors(created_at);
      CREATE INDEX IF NOT EXISTS idx_tool_errors_tool ON tool_errors(tool);
    `

// ensureLoggingConn returns the cached logging connection for dir, creating
// the owner-only db when needed. A locked store returns errStoreBusy; any
// other failure returns nil.
// port of src/runtime/tool-errors.ts:ensureLoggingConn
func ensureLoggingConn(dir string) (*sql.DB, error) {
	if cached, ok := toolErrorConnections[dir]; ok {
		return cached, nil
	}
	path := filepath.Join(dir, "cave-tools.db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil
	}
	if !tightenIfOwned(dir, true, 0o700) {
		return nil, nil
	}
	if !platformSkipDBCreate() {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			// O_NOFOLLOW: a symlink at the db path (dangling or live) refuses
			// with ELOOP instead of creating/following the target.
			// O_CREAT|O_WRONLY with explicit 0600 for owner-only creation under
			// any umask. The fd is verified regular before use.
			file, err := openNoFollow(path)
			if err != nil {
				if file != nil {
					_ = file.Close()
				}
				return nil, nil
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				_ = file.Close()
				return nil, nil
			}
			_ = file.Close()
		}
	}
	if !tightenIfOwned(path, false, 0o600) {
		return nil, nil
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil && !tightenIfOwned(sidecar, false, 0o600) {
			return nil, nil
		}
	}
	db, err := sql.Open("sqlite", loggingDSN(path))
	if err != nil {
		return nil, nil
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		if isBusyError(err) {
			return nil, errStoreBusy
		}
		return nil, nil
	}
	if _, err := db.Exec(toolErrorsSchemaSQL); err != nil {
		_ = db.Close()
		if isBusyError(err) {
			return nil, errStoreBusy
		}
		return nil, nil
	}
	if len(toolErrorConnOrder) >= 4 {
		oldest := toolErrorConnOrder[0]
		toolErrorConnOrder = toolErrorConnOrder[1:]
		if old, ok := toolErrorConnections[oldest]; ok {
			_ = old.Close()
			delete(toolErrorConnections, oldest)
		}
	}
	toolErrorConnections[dir] = db
	toolErrorConnOrder = append(toolErrorConnOrder, dir)
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			tightenIfOwned(sidecar, false, 0o600)
		}
	}
	return db, nil
}

// PruneToolErrors enforces retention: 7-day window + 5000-row cap.
// Best-effort; errors stay inside the flush guard.
// port of src/runtime/tool-errors.ts:pruneToolErrors
func PruneToolErrors(conn *sql.DB, nowMillis ...int64) error {
	now := time.Now().UnixMilli()
	if len(nowMillis) > 0 {
		now = nowMillis[0]
	}
	cutoff := now - ToolErrorRetentionDays*24*60*60*1000
	if _, err := conn.Exec("DELETE FROM tool_errors WHERE created_at < ?", cutoff); err != nil {
		return err
	}
	_, err := conn.Exec(`DELETE FROM tool_errors WHERE id NOT IN (
        SELECT id FROM tool_errors ORDER BY created_at DESC, rowid DESC LIMIT ` + strconv.Itoa(ToolErrorMaxRows) + `
      )`)
	return err
}

// registerFlushFailureLocked counts one failed flush and gives up after the
// process-level threshold, like TS's outer catch.
func registerFlushFailureLocked() {
	toolErrorConsecutiveFailures++
	if toolErrorConsecutiveFailures >= toolErrorMaxFlushFailures {
		toolErrorStoreDead = true
		toolErrorQueue = nil
		closeConnections()
	}
}

// flushOneBatch drains one bounded batch (<=50 rows) from the leading same-dir
// run. Returns true when draining may continue, false when blocked by a lock
// (queue left intact for the next tool event). Never panics.
// port of src/runtime/tool-errors.ts:flushOneBatch
func flushOneBatch(nowMillis int64) bool {
	toolErrorsMu.Lock()
	defer toolErrorsMu.Unlock()
	return flushOneBatchLocked(nowMillis)
}

// flushOneBatchLocked implements flushOneBatch with toolErrorsMu held.
func flushOneBatchLocked(nowMillis int64) bool {
	if len(toolErrorQueue) == 0 {
		return true
	}
	if !IsToolErrorLoggingEnabled(nil) || toolErrorStoreDead {
		toolErrorQueue = nil
		return true
	}

	dir := toolErrorQueue[0].storeDir
	batch := make([]queuedError, 0, toolErrorFlushBatch)
	for len(batch) < toolErrorFlushBatch && len(toolErrorQueue) > 0 && toolErrorQueue[0].storeDir == dir {
		batch = append(batch, toolErrorQueue[0])
		toolErrorQueue = toolErrorQueue[1:]
	}

	conn, err := ensureLoggingConn(dir)
	if errors.Is(err, errStoreBusy) {
		// Locked: put the batch back, keep the queue for the next event.
		toolErrorQueue = append(batch, toolErrorQueue...)
		return false
	}
	if err != nil || conn == nil {
		toolErrorQueue = append(batch, toolErrorQueue...)
		registerFlushFailureLocked()
		return true
	}

	tx, err := conn.Begin()
	if err != nil {
		if isBusyError(err) {
			toolErrorQueue = append(batch, toolErrorQueue...)
			return false
		}
		// TS's outer catch drops the already-dequeued batch on an unexpected
		// error; only a lock restores it.
		registerFlushFailureLocked()
		return true
	}
	failed := false
	var execErr error
	for _, record := range batch {
		argKeys, _ := json.Marshal(record.ArgKeys)
		argTypes, _ := json.Marshal(record.ArgTypes)
		_, execErr = tx.Exec(`INSERT OR IGNORE INTO tool_errors (
        id, created_at, tool, outcome, reason_code, elapsed_ms,
        mode, operation, batch_index, agent, session_hash, file_hash,
        arg_keys, arg_types, arg_count, unknown_arg_count, error_kind
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID,
			record.CreatedAt,
			record.Tool,
			string(record.Outcome),
			string(record.ReasonCode),
			record.ElapsedMs,
			record.Mode,
			record.Operation,
			record.BatchIndex,
			record.Agent,
			record.SessionHash,
			record.FileHash,
			string(argKeys),
			string(argTypes),
			record.ArgCount,
			record.UnknownArgCount,
			record.ErrorKind,
		)
		if execErr != nil {
			failed = true
			break
		}
	}
	if failed {
		_ = tx.Rollback()
		if isBusyError(execErr) {
			toolErrorQueue = append(batch, toolErrorQueue...)
			return false
		}
		registerFlushFailureLocked()
		return true
	}
	if err := tx.Commit(); err != nil {
		if isBusyError(err) {
			toolErrorQueue = append(batch, toolErrorQueue...)
			return false
		}
		registerFlushFailureLocked()
		return true
	}
	toolErrorConsecutiveFailures = 0
	if err := PruneToolErrors(conn, nowMillis); err != nil {
		registerFlushFailureLocked()
	}
	return true
}

// scheduleFlush schedules one bounded flush pass on a later turn. No retry
// spin: the next tool event schedules again when the store is locked.
// port of src/runtime/tool-errors.ts:scheduleFlush
func scheduleFlush() {
	toolErrorsMu.Lock()
	if toolErrorFlushScheduled || autoFlushDisabledForTests {
		toolErrorsMu.Unlock()
		return
	}
	toolErrorFlushScheduled = true
	toolErrorsMu.Unlock()

	go func() {
		toolErrorsMu.Lock()
		toolErrorFlushScheduled = false
		toolErrorsMu.Unlock()
		if !flushOneBatch(time.Now().UnixMilli()) {
			return
		}
		toolErrorsMu.Lock()
		more := len(toolErrorQueue) > 0 && !toolErrorStoreDead && IsToolErrorLoggingEnabled(nil)
		toolErrorsMu.Unlock()
		if more {
			scheduleFlush()
		}
	}()
}

// FlushToolErrorQueue synchronously drains the queue (bounded: stops after
// maxBatches batches or when a lock blocks progress). Explicit flush point for
// the CLI controlled-exit funnel and for tests. Never panics.
// port of src/runtime/tool-errors.ts:flushToolErrorQueue
func FlushToolErrorQueue(maxBatches ...int) {
	limit := 8
	if len(maxBatches) > 0 {
		limit = maxBatches[0]
	}
	defer func() { _ = recover() }()
	for i := 0; i < limit; i++ {
		toolErrorsMu.Lock()
		if len(toolErrorQueue) == 0 {
			toolErrorsMu.Unlock()
			return
		}
		if toolErrorStoreDead || !IsToolErrorLoggingEnabled(nil) {
			toolErrorQueue = nil
			toolErrorsMu.Unlock()
			return
		}
		progress := flushOneBatchLocked(time.Now().UnixMilli())
		toolErrorsMu.Unlock()
		if !progress {
			return
		}
	}
}

// ThrownError is one thrown handler value. A nil pointer means the call did
// not throw (TS: no own `thrown` property); a non-nil pointer records a throw
// even when both fields are empty (`throw null` / `throw undefined`).
// port of src/runtime/tool-errors.ts:ObserveToolCallOptions
type ThrownError struct {
	Name    string
	Message string
}

// ObserveToolCallOptions is the dispatch hook input.
// port of src/runtime/tool-errors.ts:ObserveToolCallOptions
type ObserveToolCallOptions struct {
	Tool        string
	Args        map[string]any
	SchemaProps []string
	SessionID   string
	StartedAt   int64
	Env         map[string]string
	BatchIndex  *int64
	Result      any
	Thrown      *ThrownError
}

// ObserveToolCall records soft (returned isError) and thrown outcomes;
// successes are ignored. A throw is detected by Thrown being non-nil, so
// thrown null/undefined values are recorded without fabricating messages.
// Never panics, never mutates result/error.
// port of src/runtime/tool-errors.ts:observeToolCall
func ObserveToolCall(ctx context.Context, opts ObserveToolCallOptions) {
	defer func() { _ = recover() }()
	if !IsToolErrorLoggingEnabled(opts.Env) {
		return
	}
	hasThrown := opts.Thrown != nil
	isSoft := !hasThrown && resultIsErrorObject(opts.Result)
	if !hasThrown && !isSoft {
		return
	}

	now := time.Now().UnixMilli()
	summary := SummarizeArgs(opts.Args, opts.SchemaProps)
	var reasonCode ToolErrorReasonCode
	var errorKind *string
	var batchIndex *int64
	if hasThrown {
		name := opts.Thrown.Name
		if name == "" {
			name = "Error"
		}
		message := opts.Thrown.Message
		kind := NormalizeThrownKind(name)
		errorKind = &kind
		reasonCode = ClassifyThrownReasonCode(opts.Tool, name, message)
	} else {
		text := firstText(opts.Result)
		reasonCode = ClassifySoftReasonCode(opts.Tool, text)
		batchIndex = ParseBatchIndex(text)
	}
	if opts.BatchIndex != nil {
		value := *opts.BatchIndex
		batchIndex = &value
	}

	storeDir := storage.CaveToolsDataDir()
	elapsed := now - opts.StartedAt
	if elapsed < 0 {
		elapsed = 0
	}
	mode := envString(opts.Env, "CAVE_TOOLS_MODE")
	record := queuedError{
		ToolErrorRecord: ToolErrorRecord{
			ID:              newUUID(),
			CreatedAt:       now,
			Tool:            SanitizeToolName(opts.Tool),
			Outcome:         ToolErrorSoft,
			ReasonCode:      reasonCode,
			ElapsedMs:       elapsed,
			Mode:            SanitizeMode(mode),
			Operation:       DeriveOperation(opts.Tool, opts.Args),
			BatchIndex:      batchIndex,
			Agent:           agentHint(ctx),
			SessionHash:     SessionHashFor(opts.SessionID),
			FileHash:        FileHashFor(opts.Args),
			ArgKeys:         summary.Keys,
			ArgTypes:        summary.Types,
			ArgCount:        summary.Count,
			UnknownArgCount: summary.UnknownCount,
			ErrorKind:       errorKind,
		},
		storeDir: storeDir,
	}
	if hasThrown {
		record.Outcome = ToolErrorThrown
	}
	toolErrorsMu.Lock()
	if len(toolErrorQueue) >= ToolErrorQueueCap {
		toolErrorQueue = toolErrorQueue[len(toolErrorQueue)-ToolErrorQueueCap+1:]
	}
	toolErrorQueue = append(toolErrorQueue, record)
	toolErrorsMu.Unlock()
	scheduleFlush()
}

// resultIsErrorObject reports whether a result is an object with isError true.
func resultIsErrorObject(result any) bool {
	switch value := result.(type) {
	case types.ToolResult:
		return value.IsError
	case *types.ToolResult:
		return value != nil && value.IsError
	case map[string]any:
		isError, ok := value["isError"].(bool)
		return ok && isError
	}
	return false
}

// newUUID returns a random v4 UUID (TS randomUUID).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- Local read access (CLI) ---

// openReadDb opens the shared db read-only when it exists, is a regular file
// and already has the tool_errors table. Never creates anything.
// port of src/runtime/tool-errors.ts:openReadDb
func openReadDb() *sql.DB {
	path := storage.DBPath()
	info, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(1)
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'tool_errors'").Scan(&name)
	if err != nil {
		_ = db.Close()
		return nil
	}
	if name == "" {
		_ = db.Close()
		return nil
	}
	return db
}

// toolErrorRow is one raw tool_errors row.
// port of src/runtime/tool-errors.ts:ToolErrorRow
type toolErrorRow struct {
	id              string
	createdAt       int64
	tool            string
	outcome         string
	reasonCode      string
	elapsedMs       int64
	mode            string
	operation       sql.NullString
	batchIndex      sql.NullInt64
	agent           string
	sessionHash     string
	fileHash        sql.NullString
	argKeys         string
	argTypes        string
	argCount        int64
	unknownArgCount int64
	errorKind       sql.NullString
}

// rowToRecord converts a raw row into the shared record shape.
// port of src/runtime/tool-errors.ts:rowToRecord
func rowToRecord(row toolErrorRow) ToolErrorRecord {
	argKeys := []string{}
	var parsedKeys any
	if err := json.Unmarshal([]byte(row.argKeys), &parsedKeys); err == nil {
		if values, ok := parsedKeys.([]any); ok {
			for _, value := range values {
				if key, ok := value.(string); ok {
					argKeys = append(argKeys, key)
				}
			}
		}
	}
	argTypes := map[string]string{}
	var parsedTypes any
	if err := json.Unmarshal([]byte(row.argTypes), &parsedTypes); err == nil {
		if values, ok := parsedTypes.(map[string]any); ok {
			for key, value := range values {
				if text, ok := value.(string); ok {
					argTypes[key] = text
				}
			}
		}
	}
	outcome := ToolErrorSoft
	if row.outcome == "thrown" {
		outcome = ToolErrorThrown
	}
	reasonCode := ReasonUnknown
	if reasonCodes[row.reasonCode] {
		reasonCode = ToolErrorReasonCode(row.reasonCode)
	}
	var operation *string
	if row.operation.Valid {
		value := row.operation.String
		operation = &value
	}
	var batchIndex *int64
	if row.batchIndex.Valid {
		value := row.batchIndex.Int64
		batchIndex = &value
	}
	var fileHash *string
	if row.fileHash.Valid {
		value := row.fileHash.String
		fileHash = &value
	}
	var errorKind *string
	if row.errorKind.Valid {
		value := row.errorKind.String
		errorKind = &value
	}
	return ToolErrorRecord{
		ID:              row.id,
		CreatedAt:       row.createdAt,
		Tool:            row.tool,
		Outcome:         outcome,
		ReasonCode:      reasonCode,
		ElapsedMs:       row.elapsedMs,
		Mode:            row.mode,
		Operation:       operation,
		BatchIndex:      batchIndex,
		Agent:           row.agent,
		SessionHash:     row.sessionHash,
		FileHash:        fileHash,
		ArgKeys:         argKeys,
		ArgTypes:        argTypes,
		ArgCount:        row.argCount,
		UnknownArgCount: row.unknownArgCount,
		ErrorKind:       errorKind,
	}
}

// ListToolErrors returns the most recent records, newest first.
// port of src/runtime/tool-errors.ts:listToolErrors
func ListToolErrors(limit ...int) []ToolErrorRecord {
	n := 20
	if len(limit) > 0 {
		n = limit[0]
	}
	if n == 0 {
		n = 20
	}
	if n < 1 {
		n = 1
	}
	if n > 500 {
		n = 500
	}
	db := openReadDb()
	if db == nil {
		return []ToolErrorRecord{}
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT id, created_at, tool, outcome, reason_code, elapsed_ms,
		mode, operation, batch_index, agent, session_hash, file_hash,
		arg_keys, arg_types, arg_count, unknown_arg_count, error_kind
		FROM tool_errors ORDER BY created_at DESC, rowid DESC LIMIT ?`, n)
	if err != nil {
		return []ToolErrorRecord{}
	}
	defer func() { _ = rows.Close() }()
	out := make([]ToolErrorRecord, 0, n)
	for rows.Next() {
		var row toolErrorRow
		if err := rows.Scan(
			&row.id, &row.createdAt, &row.tool, &row.outcome, &row.reasonCode,
			&row.elapsedMs, &row.mode, &row.operation, &row.batchIndex, &row.agent,
			&row.sessionHash, &row.fileHash, &row.argKeys, &row.argTypes,
			&row.argCount, &row.unknownArgCount, &row.errorKind,
		); err != nil {
			return []ToolErrorRecord{}
		}
		out = append(out, rowToRecord(row))
	}
	if rows.Err() != nil {
		return []ToolErrorRecord{}
	}
	return out
}

// ToolErrorCounts is the aggregate the CLI reader prints.
// port of src/runtime/tool-errors.ts:ToolErrorCounts
type ToolErrorCounts struct {
	Total  int64            `json:"total"`
	ByCode map[string]int64 `json:"byCode"`
	ByTool map[string]int64 `json:"byTool"`

	// Query row order, kept so the CLI can reproduce TS's stable
	// Object.entries ordering on count ties.
	byCodeOrder []string
	byToolOrder []string
}

// CountToolErrors aggregates rows by reason code and tool.
// port of src/runtime/tool-errors.ts:countToolErrors
func CountToolErrors() ToolErrorCounts {
	out := ToolErrorCounts{ByCode: map[string]int64{}, ByTool: map[string]int64{}}
	db := openReadDb()
	if db == nil {
		return out
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow("SELECT COUNT(*) AS n FROM tool_errors").Scan(&out.Total); err != nil {
		return out
	}
	if rows, err := db.Query("SELECT reason_code AS k, COUNT(*) AS n FROM tool_errors GROUP BY reason_code"); err == nil {
		for rows.Next() {
			var key string
			var count int64
			if err := rows.Scan(&key, &count); err != nil {
				break
			}
			out.ByCode[key] = count
			out.byCodeOrder = append(out.byCodeOrder, key)
		}
		_ = rows.Close()
	}
	if rows, err := db.Query("SELECT tool AS k, COUNT(*) AS n FROM tool_errors GROUP BY tool"); err == nil {
		for rows.Next() {
			var key string
			var count int64
			if err := rows.Scan(&key, &count); err != nil {
				break
			}
			out.ByTool[key] = count
			out.byToolOrder = append(out.byToolOrder, key)
		}
		_ = rows.Close()
	}
	return out
}

// ToolErrorsDBPath is the shared SQLite file holding the log.
// port of src/runtime/tool-errors.ts:toolErrorsDbPath
func ToolErrorsDBPath() string {
	return filepath.Join(storage.CaveToolsDataDir(), "cave-tools.db")
}

// ResetToolErrorStateForTests drops cached connections/queue/failure state.
// Test-only hook.
// port of src/runtime/tool-errors.ts:resetToolErrorStateForTests
func ResetToolErrorStateForTests() {
	toolErrorsMu.Lock()
	closeConnections()
	toolErrorQueue = nil
	toolErrorFlushScheduled = false
	toolErrorConsecutiveFailures = 0
	toolErrorStoreDead = false
	toolErrorsMu.Unlock()
}

// ToolErrorQueueDepthForTests returns the pending queue length. Test-only.
// port of src/runtime/tool-errors.ts:toolErrorQueueDepthForTests
func ToolErrorQueueDepthForTests() int {
	toolErrorsMu.Lock()
	defer toolErrorsMu.Unlock()
	return len(toolErrorQueue)
}
