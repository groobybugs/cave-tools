// Package edit ports the cave__edit tool.
//
// Port of src/tools/edit.ts. The TS file re-exports applyRangeEdit,
// applyMoveLines, rangeChecksum and lineTag for tests; Go callers import
// go/internal/tools directly instead, so this package exports only the tool.
package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

var (
	// port of src/tools/edit.ts:checkExpectedHash (hex test)
	reHex = jsre.MustCompile(`^[0-9a-f]+$`, "")
	// port of src/tools/edit.ts:errorText (strip the err() prefix)
	reErrorPrefix = jsre.MustCompile(`^Error: `, "")
	// port of src/tools/edit.ts:findClosestLineHint (keyword split)
	reNonWord = jsre.MustCompile(`[^a-zA-Z0-9_]`, "")
)

// editModeRequirements is the str/range/delete/move anyOf block for batch
// items. The root schema stays a plain object (strict providers such as xAI
// reject a root anyOf); handle validates the single-edit and batch shapes.
// port of src/tools/edit.ts:editModeRequirements
const editModeRequirements = `"anyOf": [
				{"required": ["old_string", "new_string"], "properties": {"old_string": {"minLength": 1}}},
				{"required": ["start_line", "end_line", "content"]},
				{"required": ["start_line", "end_line", "delete"], "properties": {"delete": {"const": true}}},
				{"required": ["start_line", "end_line", "insert_before"]}
			]`

// editOperationKeys are the single-edit fields that may not sit next to edits[].
// port of src/tools/edit.ts:editOperationKeys
var editOperationKeys = []string{
	"old_string", "new_string", "replace_all", "start_line", "end_line",
	"content", "delete", "insert_before", "expected_range_checksum",
}

// Tool is the MCP tool definition for cave__edit.
// port of src/tools/edit.ts:editTool
var Tool = types.ToolDefinition{
	Name: "cave__edit",
	Description: "Edit a file; always supply file_path explicitly (never inferred from prior reads). " +
		"String edit: {file_path, old_string, new_string}; empty new_string deletes. " +
		"Or use inclusive 1-based lines from cave__read line_numbers=true " +
		"(start_line/end_line + content, delete, or insert_before to move). " +
		"expected_hash/expected_range_checksum reject stale edits; strict mode requires one for range/move. " +
		"Batch via edits[] with a top-level file_path default or explicit per-item paths; order ranges high→low.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"file_path": {
				"type": "string",
				"description": "Required for single edits; default target for batch items"
			},
			"old_string": {
				"type": "string",
				"description": "Not combinable with range/move"
			},
			"new_string": {
				"type": "string",
				"description": "Replacement text; an explicit empty string deletes the match"
			},
			"replace_all": {
				"type": "boolean",
				"description": "Replace all, not just a unique match"
			},
			"start_line": {
				"type": "integer"
			},
			"end_line": {
				"type": "integer",
				"description": "start_line-1 with content inserts before start_line"
			},
			"content": {
				"type": "string",
				"description": "New range body, no line prefixes; empty deletes"
			},
			"delete": {
				"type": "boolean",
				"description": "When true, delete the range regardless of content"
			},
			"insert_before": {
				"type": "integer",
				"description": "Move range before this line (lineCount+1 = end); takes precedence over content/delete"
			},
			"expected_hash": {
				"type": "string",
				"description": "File sha256 or ≥8-char prefix"
			},
			"expected_range_checksum": {
				"type": "string",
				"description": "range_checksum of the edited span"
			},
			"edits": {
				"type": "array",
				"minItems": 1,
				"items": {
					"type": "object",
					"properties": {
						"file_path": {"type": "string"},
						"old_string": {"type": "string"},
						"new_string": {"type": "string"},
						"replace_all": {"type": "boolean"},
						"start_line": {"type": "integer"},
						"end_line": {"type": "integer"},
						"content": {"type": "string"},
						"delete": {"type": "boolean"},
						"insert_before": {"type": "integer"},
						"expected_hash": {"type": "string"},
						"expected_range_checksum": {"type": "string"}
					},
					` + editModeRequirements + `
				},
				"description": "Non-empty batch; each item needs a target and an edit mode"
			}
		}
	}`),
	Handler: handle,
}

// strEdit is a string-replace batch item.
// port of src/tools/edit.ts:StrEdit
type strEdit struct {
	oldString  string
	newString  string
	replaceAll bool
}

// rangeEdit is a line-range batch item.
// port of src/tools/edit.ts:RangeEdit
type rangeEdit struct {
	index                 int
	startLine             int
	endLine               int
	content               string
	expectedRangeChecksum any
}

// moveEdit is a line-move batch item.
// port of src/tools/edit.ts:MoveEdit
type moveEdit struct {
	index                 int
	startLine             int
	endLine               int
	insertBefore          int
	expectedRangeChecksum any
}

// findClosestLineHint points at the nearest line sharing the search prefix.
// port of src/tools/edit.ts:findClosestLineHint
func findClosestLineHint(content, oldStr string) string {
	firstLine := jsre.Trim(strings.Split(oldStr, "\n")[0])
	if utf16Length(firstLine) < 4 {
		return ""
	}

	lines := strings.Split(content, "\n")
	bestNum := -1
	bestText := ""
	for i, line := range lines {
		if strings.Contains(line, firstLine) {
			bestNum, bestText = i+1, line
			break
		}
	}

	if bestNum < 0 {
		keywords := []string{}
		for _, word := range reNonWord.Split(firstLine, -1) {
			if utf16Length(word) >= 4 {
				keywords = append(keywords, word)
			}
		}
		if len(keywords) > 0 {
			for i, line := range lines {
				if strings.Contains(line, keywords[0]) {
					bestNum, bestText = i+1, line
					break
				}
			}
		}
	}

	if bestNum < 0 {
		return ""
	}
	trimmed := jsre.Trim(bestText)
	preview := trimmed
	if utf16Length(trimmed) > 100 {
		preview = utf16Slice(trimmed, 0, 97) + "..."
	}
	return "\nClosest match at line " + strconv.Itoa(bestNum) + ": `" + preview + "`\nHint: check indentation/whitespace differences."
}

// isMoveShape reports the start_line+insert_before shape.
// port of src/tools/edit.ts:isMoveShape
func isMoveShape(edit map[string]any) bool {
	return present(edit, "insert_before") && present(edit, "start_line")
}

// isRangeShape reports the start_line + content|delete shape.
// port of src/tools/edit.ts:isRangeShape
func isRangeShape(edit map[string]any) bool {
	if !present(edit, "start_line") || isMoveShape(edit) {
		return false
	}
	_, hasContent := edit["content"]
	return hasContent || edit["delete"] == true
}

// isStrShape reports a non-empty old_string.
// port of src/tools/edit.ts:isStrShape
func isStrShape(edit map[string]any) bool {
	value, ok := edit["old_string"].(string)
	return ok && len(value) > 0
}

// checkExpectedHash verifies the optional file hash proof.
// port of src/tools/edit.ts:checkExpectedHash
func checkExpectedHash(expected any, sourceBytes []byte) *types.ToolResult {
	if expected == nil {
		return nil
	}
	want := jsre.ToLowerJS(jsre.Trim(jsStringValue(expected)))
	if want == "" {
		return nil
	}
	if utf16Length(want) < 8 {
		return errResultPtr("expected_hash must be at least 8 hex characters")
	}
	if !reHex.MatchString(want) {
		return errResultPtr("expected_hash must be hexadecimal")
	}
	actual := compression.HashContent(sourceBytes)
	actualLc := strings.ToLower(actual)
	if actualLc == want || strings.HasPrefix(actualLc, want) {
		return nil
	}
	suffix := ""
	if len(want) > 16 {
		suffix = "…"
	}
	return errResultPtr("expected_hash mismatch — file changed since read. Re-run cave__read with line_numbers=true.\n" +
		"expected: " + sliceHex(want, 0, 16) + suffix + "\n" +
		"actual:   " + sliceHex(actualLc, 0, 16) + "…")
}

// checkRangeChecksumOrError verifies the optional range checksum proof.
// port of src/tools/edit.ts:checkRangeChecksumOrError
func checkRangeChecksumOrError(content string, startLine, endLine int, expected any) *types.ToolResult {
	if expected == nil || jsre.Trim(jsStringValue(expected)) == "" {
		return nil
	}
	result := tools.VerifyRangeChecksum(content, startLine, endLine, jsStringValue(expected))
	if result.OK {
		return nil
	}
	if result.Error == "" {
		return errResultPtr("checksum failed")
	}
	message := result.Error + "\n" +
		"expected: " + utf16Slice(jsre.ToLowerJS(jsre.Trim(jsStringValue(expected))), 0, 16) + "\n" +
		"actual:   " + result.Actual + "\n"
	if result.Window != "" {
		message += "window:\n" + result.Window
	}
	return errResultPtr(message)
}

// isRecord reports plain editable argument objects (not null or arrays).
// port of src/tools/edit.ts:isRecord
func isRecord(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// checkFieldTypes rejects present argument values whose JSON type is wrong.
// port of src/tools/edit.ts:checkFieldTypes
func checkFieldTypes(raw map[string]any, label string) *types.ToolResult {
	prefix := ""
	if label != "" {
		prefix = label + ": "
	}
	for _, key := range []string{"start_line", "end_line", "insert_before"} {
		if value, ok := raw[key]; ok && value != nil && !isNumber(value) {
			return errResultPtr(prefix + key + " must be a number")
		}
	}
	for _, key := range []string{"replace_all", "delete"} {
		if value, ok := raw[key]; ok && value != nil {
			if _, isBool := value.(bool); !isBool {
				return errResultPtr(prefix + key + " must be a boolean")
			}
		}
	}
	for _, key := range []string{"content", "old_string", "file_path"} {
		if value, ok := raw[key]; ok && value != nil {
			if _, isString := value.(string); !isString {
				return errResultPtr(prefix + key + " must be a string")
			}
		}
	}
	return nil
}

// hasProofValue reports a nonempty string strict proof.
// port of src/tools/edit.ts:hasProofValue
func hasProofValue(value any) bool {
	text, isString := value.(string)
	return isString && jsre.Trim(text) != ""
}

// requireProofInStrict enforces the strict-mode proof requirement.
// port of src/tools/edit.ts:requireProofInStrict
func requireProofInStrict(ctx context.Context, hasFileHash, hasRangeChecksum bool) *types.ToolResult {
	if tools.ActiveEditMode(ctx) != "strict" {
		return nil
	}
	if hasFileHash || hasRangeChecksum {
		return nil
	}
	return errResultPtr("STRICT: range/move edit requires expected_hash or expected_range_checksum " +
		"(from cave__read line_numbers footer)")
}

// writeEdited writes the updated text through writeIfUnchanged and refreshes caches.
// port of src/tools/edit.ts:writeEdited
func writeEdited(ctx context.Context, filePath string, sourceBytes []byte, bom bool, updated string) *types.ToolResult {
	result, writeErr := runtime.WriteIfUnchanged(ctx, filePath, sourceBytes, runtime.JoinBom(updated, bom))
	if writeErr != nil {
		return errResultPtr(writeErr.Error())
	}
	compression.InvalidateFileCache(ctx, result.Canonical)
	compression.RecordEdit(ctx, result.Canonical)
	compression.InvalidateFileCache(ctx, filePath)
	return nil
}

// loadFile reads and decodes one edit target.
// port of src/tools/edit.ts:loadFile
func loadFile(filePath string) (content string, sourceBytes []byte, bom bool, failure *types.ToolResult) {
	sourceBytes, readErr := os.ReadFile(filePath)
	if readErr != nil {
		return "", nil, false, errResultPtr("Cannot read file: " + filePath)
	}
	text, hasBom, decodeErr := runtime.DecodeUtf8PreserveBom(sourceBytes)
	if decodeErr != nil {
		if strings.Contains(decodeErr.Error(), "encoded data") {
			return "", nil, false, errResultPtr("File is not valid UTF-8: " + filePath)
		}
		return "", nil, false, errResultPtr("Cannot read file: " + filePath)
	}
	return text, sourceBytes, hasBom, nil
}

// isToolResult reports whether the value is already a ToolResult.
// port of src/tools/edit.ts:isToolResult
func isToolResult(value any) bool {
	result, ok := value.(types.ToolResult)
	return ok && result.Content != nil
}

// handle is the cave__edit handler.
// port of src/tools/edit.ts:editTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	if args == nil {
		return errResult("edit arguments must be an object"), nil
	}
	if rawEdits, ok := args["edits"]; ok {
		if _, isList := asAnySlice(rawEdits); !isList {
			return errResult("edits must be an array of edit objects"), nil
		}
		for _, key := range editOperationKeys {
			if _, present := args[key]; present {
				return errResult("batch operation fields belong inside edits[]; only file_path and expected_hash are top-level defaults"), nil
			}
		}
		return handleBatchEdits(ctx, args), nil
	}

	filePath := ""
	if value, ok := args["file_path"]; ok && value != nil {
		text, isString := value.(string)
		if !isString {
			return errResult("file_path must be a string"), nil
		}
		filePath = text
	}
	if filePath == "" {
		return errResult("file_path is required for a single edit; supply it explicitly (never inferred from a prior read). String edits use file_path, old_string, new_string."), nil
	}

	if fieldErr := checkFieldTypes(args, ""); fieldErr != nil {
		return *fieldErr, nil
	}

	hasMove := isMoveShape(args)
	hasRange := present(args, "start_line") && !hasMove &&
		(func() bool { _, ok := args["content"]; return ok }() || args["delete"] == true)
	hasStrKey := present(args, "old_string")
	hasStr := false
	if hasStrKey {
		hasStr = utf16Length(jsStringValue(args["old_string"])) > 0
	}

	if hasStrKey && !hasStr && !hasRange && !hasMove {
		return errResult("old_string cannot be empty (or use start_line/end_line/content for range mode)"), nil
	}

	modes := 0
	for _, active := range []bool{hasMove, hasRange, hasStr} {
		if active {
			modes++
		}
	}
	if modes > 1 {
		return errResult("pass either old_string/new_string or start_line/end_line/content (or delete/insert_before), not both"), nil
	}
	if modes == 0 {
		return errResult("need old_string/new_string, or start_line/end_line with content|delete|insert_before"), nil
	}
	if hasStr {
		if _, ok := args["new_string"].(string); !ok {
			return errResult(`new_string is required for string mode and must be a string (use "" to delete)`), nil
		}
	}

	target, resolveErr := runtime.ResolveMutationTarget(ctx, filePath)
	if resolveErr != nil {
		return errResult(resolveErr.Error()), nil
	}

	singleResult, lockErr := runtime.WithFileLocks(ctx, []string{target.Canonical}, func(lockedCtx context.Context) (types.ToolResult, error) {
		content, sourceBytes, bom, loaded := loadFile(filePath)
		if loaded != nil {
			return *loaded, nil
		}

		if hashErr := checkExpectedHash(args["expected_hash"], sourceBytes); hashErr != nil {
			return *hashErr, nil
		}

		if hasMove || hasRange {
			proofErr := requireProofInStrict(
				lockedCtx,
				hasProofValue(args["expected_hash"]),
				hasProofValue(args["expected_range_checksum"]),
			)
			if proofErr != nil {
				return *proofErr, nil
			}
		}

		if hasMove {
			startLine, startOK := integerField(args, "start_line")
			endLine, endOK := integerField(args, "end_line")
			insertBefore, insertOK := integerField(args, "insert_before")
			if !startOK || !endOK || !insertOK {
				return errResult("start_line, end_line, and insert_before must be integers"), nil
			}
			if csErr := checkRangeChecksumOrError(content, startLine, endLine, args["expected_range_checksum"]); csErr != nil {
				return *csErr, nil
			}

			result := tools.ApplyMoveLines(content, startLine, endLine, insertBefore)
			if !result.OK {
				message := result.Error
				if message == "" {
					message = "move failed"
				}
				return errResult(message), nil
			}
			compression.RecordRangeEchoSaved(lockedCtx, int64(result.MovedChars))

			if writeErr := writeEdited(lockedCtx, filePath, sourceBytes, bom, result.Content); writeErr != nil {
				return *writeErr, nil
			}
			return okResult("Edited " + filePath + " (move lines " + strconv.Itoa(startLine) + "-" + strconv.Itoa(endLine) +
				" → before " + strconv.Itoa(insertBefore) + ")"), nil
		}

		if hasRange {
			startLine, startOK := integerField(args, "start_line")
			endLine, endOK := integerField(args, "end_line")
			if !startOK || !endOK {
				return errResult("start_line and end_line must be integers for range mode"), nil
			}
			replacement := ""
			if args["delete"] != true {
				replacement = jsStringValue(args["content"])
			}
			if args["delete"] != true {
				if value, ok := args["content"]; !ok || value == nil {
					return errResult("content is required for range mode (or set delete:true)"), nil
				}
			}

			if csErr := checkRangeChecksumOrError(content, startLine, endLine, args["expected_range_checksum"]); csErr != nil {
				return *csErr, nil
			}

			result := tools.ApplyRangeEdit(content, startLine, endLine, replacement)
			if !result.OK {
				message := result.Error
				if message == "" {
					message = "range edit failed"
				}
				return errResult(message), nil
			}
			compression.RecordRangeEchoSaved(lockedCtx, int64(result.ReplacedChars))

			if writeErr := writeEdited(lockedCtx, filePath, sourceBytes, bom, result.Content); writeErr != nil {
				return *writeErr, nil
			}

			kind := "range"
			if endLine == startLine-1 {
				kind = "insert"
			} else if len(replacement) == 0 {
				kind = "delete"
			}
			return okResult("Edited " + filePath + " (" + kind + " lines " + strconv.Itoa(startLine) + "-" + strconv.Itoa(endLine) + ")"), nil
		}

		oldString, _ := args["old_string"].(string)
		newString, _ := args["new_string"].(string)
		replaceAll := args["replace_all"] == true

		if oldString == newString {
			return errResult("old_string and new_string are identical"), nil
		}
		if oldString == "" {
			return errResult("old_string cannot be empty"), nil
		}

		single := tools.ComputeEditResult(lockedCtx, content, oldString, newString, replaceAll)
		if !single.OK {
			if single.NonUnique {
				return errResult("old_string is not unique (2+ matches). Add more context or set replace_all."), nil
			}
			if strings.Contains(content, newString) {
				return errResult("old_string not found — new_string already present, edit already applied?"), nil
			}
			message := single.Error
			if message == "" {
				message = "edit failed"
			}
			return errResult(message + findClosestLineHint(content, oldString)), nil
		}

		if writeErr := writeEdited(lockedCtx, filePath, sourceBytes, bom, single.Updated); writeErr != nil {
			return *writeErr, nil
		}

		count := single.Replacements
		plural := "s"
		if count == 1 {
			plural = ""
		}
		return okResult("Edited " + filePath + " (" + strconv.Itoa(count) + " replacement" + plural + ")"), nil
	})
	if lockErr != nil {
		return errResult(lockErr.Error()), nil
	}
	return singleResult, nil
}

// applyEditsToContent plans and applies one file's batch items.
// port of src/tools/edit.ts:applyEditsToContent
func applyEditsToContent(
	ctx context.Context,
	filePath string,
	content string,
	edits []map[string]any,
	indices []int,
	fileHashProvided bool,
) (string, *types.ToolResult) {
	var ranges []rangeEdit
	var moves []moveEdit
	var strEdits []struct {
		index int
		edit  strEdit
	}

	for i, raw := range edits {
		globalIndex := i
		if indices != nil {
			globalIndex = indices[i]
		}
		move := isMoveShape(raw)
		rangeShape := isRangeShape(raw) || (raw["delete"] == true && keySet(raw, "start_line"))
		str := isStrShape(raw)

		kinds := 0
		for _, active := range []bool{move, rangeShape && !move, str} {
			if active {
				kinds++
			}
		}
		if kinds > 1 {
			return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: pass only one of str-replace, range/delete, or move")
		}
		if kinds == 0 {
			return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: need old_string/new_string, start_line+content|delete, or start_line+insert_before")
		}
		if str {
			if _, ok := raw["new_string"].(string); !ok {
				return "", errResultPtr(`edits[` + strconv.Itoa(globalIndex) + `]: new_string is required and must be a string (use "" to delete)`)
			}
		}

		if move {
			startLine, startOK := integerField(raw, "start_line")
			endLine, endOK := integerField(raw, "end_line")
			insertBefore, insertOK := integerField(raw, "insert_before")
			if !startOK || !endOK || !insertOK {
				return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: start_line, end_line, insert_before must be integers")
			}
			proofErr := requireProofInStrict(
				ctx,
				fileHashProvided || hasProofValue(raw["expected_hash"]),
				hasProofValue(raw["expected_range_checksum"]),
			)
			if proofErr != nil {
				return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: " + errorText(*proofErr))
			}
			moves = append(moves, moveEdit{
				index:                 globalIndex,
				startLine:             startLine,
				endLine:               endLine,
				insertBefore:          insertBefore,
				expectedRangeChecksum: rawExpectedRangeChecksum(raw),
			})
			continue
		}

		if rangeShape || raw["delete"] == true {
			startLine, startOK := integerField(raw, "start_line")
			endLine, endOK := integerField(raw, "end_line")
			if !startOK || !endOK {
				return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: start_line and end_line must be integers")
			}
			if raw["delete"] != true {
				if _, ok := raw["content"]; !ok {
					return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: content is required (or delete:true)")
				}
			}
			proofErr := requireProofInStrict(
				ctx,
				fileHashProvided || hasProofValue(raw["expected_hash"]),
				hasProofValue(raw["expected_range_checksum"]),
			)
			if proofErr != nil {
				return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: " + errorText(*proofErr))
			}
			replacement := ""
			if raw["delete"] != true {
				replacement = jsStringValue(raw["content"])
			}
			ranges = append(ranges, rangeEdit{
				index:                 globalIndex,
				startLine:             startLine,
				endLine:               endLine,
				content:               replacement,
				expectedRangeChecksum: rawExpectedRangeChecksum(raw),
			})
			continue
		}

		oldString, _ := raw["old_string"].(string)
		newString, _ := raw["new_string"].(string)
		if oldString == newString {
			return "", errResultPtr("edits[" + strconv.Itoa(globalIndex) + "]: old_string and new_string are identical")
		}
		strEdits = append(strEdits, struct {
			index int
			edit  strEdit
		}{index: globalIndex, edit: strEdit{
			oldString:  oldString,
			newString:  newString,
			replaceAll: raw["replace_all"] == true,
		}})
	}

	// More than one move per file batch would silently operate on stale
	// coordinates (each move renumbers the lines the next move names).
	if len(moves) > 1 {
		return "", errResultPtr("multiple moves in one batch for the same file are not supported; split into separate calls (each move changes line numbers for the next)")
	}

	// Overlap check for non-insert ranges on original lines.
	type interval struct {
		s int
		e int
		i int
	}
	intervals := []interval{}
	for _, r := range ranges {
		if r.endLine >= r.startLine {
			intervals = append(intervals, interval{s: r.startLine, e: r.endLine, i: r.index})
		}
	}
	sort.SliceStable(intervals, func(a, b int) bool { return intervals[a].s < intervals[b].s })
	for i := 1; i < len(intervals); i++ {
		if intervals[i].s <= intervals[i-1].e {
			return "", errResultPtr("edits overlap. Merge them into one edit or target disjoint regions.")
		}
	}

	// An insert whose point falls inside a replaced range is ambiguous.
	for _, ins := range ranges {
		if ins.endLine != ins.startLine-1 {
			continue
		}
		for _, r := range intervals {
			if ins.startLine >= r.s && ins.startLine <= r.e {
				return "", errResultPtr("edits[" + strconv.Itoa(ins.index) + "]: insert point " + strconv.Itoa(ins.startLine) +
					" lies inside replaced range " + strconv.Itoa(r.s) + "-" + strconv.Itoa(r.e) +
					" (edits[" + strconv.Itoa(r.i) + "]); merge them or move the insert outside the range")
			}
		}
	}

	updated := content

	// Moves first (on original coordinates), high start_line first.
	movesSorted := make([]moveEdit, len(moves))
	copy(movesSorted, moves)
	sort.SliceStable(movesSorted, func(a, b int) bool { return movesSorted[a].startLine > movesSorted[b].startLine })
	for _, m := range movesSorted {
		if csErr := checkRangeChecksumOrError(updated, m.startLine, m.endLine, m.expectedRangeChecksum); csErr != nil {
			return "", errResultPtr("edits[" + strconv.Itoa(m.index) + "]: " + errorText(*csErr))
		}
		result := tools.ApplyMoveLines(updated, m.startLine, m.endLine, m.insertBefore)
		if !result.OK {
			message := result.Error
			if message == "" {
				message = "move failed"
			}
			return "", errResultPtr("edits[" + strconv.Itoa(m.index) + "]: " + message)
		}
		compression.RecordRangeEchoSaved(ctx, int64(result.MovedChars))
		updated = result.Content
	}

	// No mix of move+range in the same file batch (simpler, safe).
	if len(moves) > 0 && len(ranges) > 0 {
		return "", errResultPtr("cannot mix move and range edits in one batch for the same file; split calls")
	}

	// High line numbers first so earlier spans keep original coordinates.
	rangesHighFirst := make([]rangeEdit, len(ranges))
	copy(rangesHighFirst, ranges)
	sort.SliceStable(rangesHighFirst, func(a, b int) bool {
		if rangesHighFirst[a].startLine != rangesHighFirst[b].startLine {
			return rangesHighFirst[a].startLine > rangesHighFirst[b].startLine
		}
		return rangesHighFirst[a].index < rangesHighFirst[b].index
	})
	for _, r := range rangesHighFirst {
		if csErr := checkRangeChecksumOrError(updated, r.startLine, r.endLine, r.expectedRangeChecksum); csErr != nil {
			return "", errResultPtr("edits[" + strconv.Itoa(r.index) + "]: " + errorText(*csErr))
		}
		result := tools.ApplyRangeEdit(updated, r.startLine, r.endLine, r.content)
		if !result.OK {
			message := result.Error
			if message == "" {
				message = "range edit failed"
			}
			return "", errResultPtr("edits[" + strconv.Itoa(r.index) + "]: " + message)
		}
		compression.RecordRangeEchoSaved(ctx, int64(result.ReplacedChars))
		updated = result.Content
	}

	if len(strEdits) > 0 && len(ranges) == 0 && len(moves) == 0 {
		// Match every applied span per item against the ORIGINAL snapshot, then
		// globally overlap-check source coordinates, then apply in reverse.
		var allSpans []tools.EditApplied
		for _, item := range strEdits {
			if item.edit.oldString == item.edit.newString {
				return "", errResultPtr("edits[" + strconv.Itoa(item.index) + "]: old_string and new_string are identical")
			}
			r := tools.ComputeEditResult(ctx, content, item.edit.oldString, item.edit.newString, item.edit.replaceAll)
			if !r.OK {
				if r.NonUnique {
					return "", errResultPtr("edits[" + strconv.Itoa(item.index) + "]: old_string is not unique (2+ matches). Add more context or set replace_all.")
				}
				message := r.Error
				if message == "" {
					message = "string edit failed"
				}
				return "", errResultPtr("edits[" + strconv.Itoa(item.index) + "]: " + message)
			}
			allSpans = append(allSpans, r.Applied...)
		}
		sort.SliceStable(allSpans, func(a, b int) bool { return allSpans[a].Start < allSpans[b].Start })
		for i := 1; i < len(allSpans); i++ {
			if allSpans[i-1].End > allSpans[i].Start {
				return "", errResultPtr("edits overlap. Merge them into one edit or target disjoint regions.")
			}
		}
		updated = content
		for i := len(allSpans) - 1; i >= 0; i-- {
			m := allSpans[i]
			updated = updated[:m.Start] + m.Replacement + updated[m.End:]
		}
	} else if len(strEdits) > 0 {
		for _, item := range strEdits {
			if item.edit.oldString == item.edit.newString {
				return "", errResultPtr("edits[" + strconv.Itoa(item.index) + "]: old_string and new_string are identical")
			}
			r := tools.ComputeEditResult(ctx, updated, item.edit.oldString, item.edit.newString, item.edit.replaceAll)
			if !r.OK {
				if r.NonUnique {
					return "", errResultPtr("edits[" + strconv.Itoa(item.index) + "]: old_string is not unique after prior edits.")
				}
				message := r.Error
				if message == "" {
					message = "string edit failed"
				}
				return "", errResultPtr("edits[" + strconv.Itoa(item.index) + "]: " + message)
			}
			updated = r.Updated
		}
	}

	_ = filePath // used by callers for messaging
	return updated, nil
}

// validateBatchItemShape checks one batch item's edit-mode shape.
// port of src/tools/edit.ts:validateBatchItemShape
func validateBatchItemShape(index int, value any) *types.ToolResult {
	raw, ok := value.(map[string]any)
	if !ok {
		return errResultPtr("edits[" + strconv.Itoa(index) + "]: must be an edit object")
	}
	move := isMoveShape(raw)
	rangeShape := isRangeShape(raw) || (raw["delete"] == true && keySet(raw, "start_line"))
	str := isStrShape(raw)
	kinds := 0
	for _, active := range []bool{move, rangeShape && !move, str} {
		if active {
			kinds++
		}
	}
	if kinds > 1 {
		return errResultPtr("edits[" + strconv.Itoa(index) + "]: pass only one of str-replace, range/delete, or move")
	}
	if kinds == 0 {
		return errResultPtr("edits[" + strconv.Itoa(index) + "]: need old_string/new_string, start_line+content|delete, or start_line+insert_before")
	}
	if str {
		if _, isString := raw["new_string"].(string); !isString {
			return errResultPtr(`edits[` + strconv.Itoa(index) + `]: new_string is required and must be a string (use "" to delete)`)
		}
	}
	return nil
}

// errorText strips the "Error: " prefix from a tool result's first text block.
// port of src/tools/edit.ts:errorText
func errorText(result types.ToolResult) string {
	if len(result.Content) == 0 {
		return "operation failed"
	}
	text, ok := result.Content[0].(types.TextContent)
	if !ok {
		return "operation failed"
	}
	return reErrorPrefix.ReplaceAllString(text.Text, "")
}

// batchItem is one edits[] entry with its original global index.
type batchItem struct {
	index int
	edit  map[string]any
}

// batchGroup is one file's items, in first-seen order (JS Map order).
type batchGroup struct {
	path  string
	items []batchItem
}

// handleBatchEdits plans and applies a multi-file batch.
// port of src/tools/edit.ts:handleBatchEdits
func handleBatchEdits(ctx context.Context, args map[string]any) types.ToolResult {
	rawEdits, _ := asAnySlice(args["edits"])
	if len(rawEdits) == 0 {
		return errResult("edits array is empty")
	}

	if topTypeErr := checkFieldTypes(args, ""); topTypeErr != nil {
		return *topTypeErr
	}

	defaultPath := ""
	if value, ok := args["file_path"]; ok && value != nil {
		text, isString := value.(string)
		if !isString {
			return errResult("file_path must be a string")
		}
		defaultPath = text
	}

	// Validate every item before any file read/write.
	for i, value := range rawEdits {
		if !isRecord(value) {
			return errResult("edits[" + strconv.Itoa(i) + "]: must be an edit object")
		}
		raw := value.(map[string]any)
		if typeErr := checkFieldTypes(raw, "edits["+strconv.Itoa(i)+"]"); typeErr != nil {
			return *typeErr
		}
		if shapeErr := validateBatchItemShape(i, raw); shapeErr != nil {
			return *shapeErr
		}
	}

	// Group by file_path, preserving first-seen order.
	var groups []batchGroup
	for i, value := range rawEdits {
		raw := value.(map[string]any)
		filePath := defaultPath
		if itemPath, ok := raw["file_path"].(string); ok && len(itemPath) > 0 {
			filePath = itemPath
		}
		if filePath == "" {
			return errResult("edits[" + strconv.Itoa(i) + "]: file_path required (set top-level file_path or per-item file_path)")
		}
		found := false
		for index := range groups {
			if groups[index].path == filePath {
				groups[index].items = append(groups[index].items, batchItem{index: i, edit: raw})
				found = true
				break
			}
		}
		if !found {
			groups = append(groups, batchGroup{path: filePath, items: []batchItem{{index: i, edit: raw}}})
		}
	}

	topLevelHash := false
	if value, ok := args["expected_hash"]; ok && value != nil {
		topLevelHash = jsre.Trim(jsStringValue(value)) != ""
	}
	topLevelHashProof := hasProofValue(args["expected_hash"])
	if len(groups) > 1 && topLevelHash {
		return errResult("top-level expected_hash applies only to single-file batches; " +
			"set expected_hash on each edit item for multi-file batches")
	}

	// Resolve every literal path to its canonical target BEFORE reading.
	canonicalFp := map[string]struct {
		fp    string
		index int
	}{}
	for _, group := range groups {
		target, resolveErr := runtime.ResolveMutationTarget(ctx, group.path)
		if resolveErr != nil {
			return errResult(resolveErr.Error())
		}
		clash, exists := canonicalFp[target.Canonical]
		if exists && clash.fp != group.path {
			return errResult("edits[" + strconv.Itoa(group.items[0].index) + "] and edits[" + strconv.Itoa(clash.index) +
				"] target the same file via different paths (\"" + clash.fp + "\" and \"" + group.path + "\"); use one path per file")
		}
		if !exists {
			canonicalFp[target.Canonical] = struct {
				fp    string
				index int
			}{fp: group.path, index: group.items[0].index}
		}
	}
	lockPaths := make([]string, 0, len(canonicalFp))
	for path := range canonicalFp {
		lockPaths = append(lockPaths, path)
	}
	sort.Strings(lockPaths)

	batchResult, lockErr := runtime.WithFileLocks(ctx, lockPaths, func(lockedCtx context.Context) (types.ToolResult, error) {
		// Load and validate every file before writing any of them.
		type loadedFile struct {
			filePath    string
			content     string
			sourceBytes []byte
			bom         bool
			items       []batchItem
		}
		loadedFiles := make([]loadedFile, 0, len(groups))
		for _, group := range groups {
			content, sourceBytes, bom, loaded := loadFile(group.path)
			if loaded != nil {
				return *loaded, nil
			}

			for _, item := range group.items {
				if hashErr := checkExpectedHash(item.edit["expected_hash"], sourceBytes); hashErr != nil {
					return errResult("edits[" + strconv.Itoa(item.index) + "]: " + errorText(*hashErr)), nil
				}
			}
			if len(groups) == 1 && topLevelHash {
				if hashErr := checkExpectedHash(args["expected_hash"], sourceBytes); hashErr != nil {
					return *hashErr, nil
				}
			}

			loadedFiles = append(loadedFiles, loadedFile{
				filePath:    group.path,
				content:     content,
				sourceBytes: sourceBytes,
				bom:         bom,
				items:       group.items,
			})
		}

		// Compute every file's new content before the first write.
		type plan struct {
			filePath    string
			sourceBytes []byte
			bom         bool
			updated     string
			itemCount   int
		}
		plans := make([]plan, 0, len(loadedFiles))
		for _, file := range loadedFiles {
			edits := make([]map[string]any, len(file.items))
			indices := make([]int, len(file.items))
			for i, item := range file.items {
				edits[i] = item.edit
				indices[i] = item.index
			}
			updated, failure := applyEditsToContent(lockedCtx, file.filePath, file.content, edits, indices, len(groups) == 1 && topLevelHashProof)
			if failure != nil {
				return *failure, nil
			}
			plans = append(plans, plan{
				filePath:    file.filePath,
				sourceBytes: file.sourceBytes,
				bom:         file.bom,
				updated:     updated,
				itemCount:   len(file.items),
			})
		}

		var summaries []string
		var written []string
		for i, plan := range plans {
			if writeErr := writeEdited(lockedCtx, plan.filePath, plan.sourceBytes, plan.bom, plan.updated); writeErr != nil {
				notAttempted := make([]string, 0, len(plans)-i-1)
				for _, rest := range plans[i+1:] {
					notAttempted = append(notAttempted, rest.filePath)
				}
				writtenText := "(none)"
				if len(written) > 0 {
					writtenText = strings.Join(written, ", ")
				}
				notAttemptedText := "(none)"
				if len(notAttempted) > 0 {
					notAttemptedText = strings.Join(notAttempted, ", ")
				}
				return errResult(errorText(*writeErr) + "\n" +
					"Failed file (may be partially modified): " + plan.filePath + "\n" +
					"Files written before failure: " + writtenText + "\n" +
					"Files not attempted: " + notAttemptedText), nil
			}
			written = append(written, plan.filePath)
			summaries = append(summaries, plan.filePath+" ("+strconv.Itoa(plan.itemCount)+")")
		}

		plural := "s"
		if len(summaries) == 1 {
			plural = ""
		}
		return okResult("Edited " + strconv.Itoa(len(summaries)) + " file" + plural + ": " + strings.Join(summaries, ", ")), nil
	})
	if lockErr != nil {
		return errResult(lockErr.Error())
	}
	return batchResult
}

// port of src/tools/edit.ts:ok
func okResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

// port of src/tools/edit.ts:err
func errResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text("Error: " + text)}, IsError: true}
}

func errResultPtr(text string) *types.ToolResult {
	result := errResult(text)
	return &result
}

// present reports a key that is set to anything but undefined/null.
func present(raw map[string]any, key string) bool {
	value, ok := raw[key]
	return ok && value != nil
}

// keySet reports a key that exists, even when its value is null (TS `!== undefined`).
func keySet(raw map[string]any, key string) bool {
	_, ok := raw[key]
	return ok
}

// asAnySlice accepts the slice shapes an edits[] argument can arrive in.
func asAnySlice(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	case []map[string]any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

// isNumber reports a value typeof "number" in JS terms.
func isNumber(value any) bool {
	_, ok := numberValue(value)
	return ok
}

// numberValue mirrors Number(value) for the JSON number shapes Go can hold.
func numberValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// integerValue mirrors Number.isInteger(Number(value)); nil is Number(null) === 0.
func integerValue(value any) (int, bool) {
	if value == nil {
		return 0, true
	}
	parsed, ok := numberValue(value)
	if !ok || math.IsInf(parsed, 0) || math.IsNaN(parsed) || parsed != math.Trunc(parsed) {
		return 0, false
	}
	return int(parsed), true
}

// rawExpectedRangeChecksum mirrors the batch planners' `!== undefined ?
// String(value) : undefined`, so an explicit null proves as "null" (and fails
// verification) instead of being dropped like an absent key.
func rawExpectedRangeChecksum(raw map[string]any) any {
	if value, ok := raw["expected_range_checksum"]; ok {
		return jsStringValue(value)
	}
	return nil
}

// integerField parses Number(raw[key]) for the edit handlers. A missing key is
// Number(undefined) === NaN (rejected); an explicit null is Number(null) === 0.
func integerField(raw map[string]any, key string) (int, bool) {
	if !keySet(raw, key) {
		return 0, false
	}
	return integerValue(raw[key])
}

// jsStringValue mirrors JavaScript's String(value), including null -> "null".
func jsStringValue(value any) string {
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
	case json.Number:
		return v.String()
	case float64:
		return jsNumberString(v)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case map[string]any:
		return "[object Object]"
	case []any:
		return jsArrayString(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// jsArrayString mirrors Array.prototype.toString: elements are joined by ","
// where null/undefined slots render empty and nested values stringify
// recursively (arrays join, objects print [object Object]).
func jsArrayString(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		if value != nil {
			parts[i] = jsStringValue(value)
		}
	}
	return strings.Join(parts, ",")
}

// jsNumberString mirrors Number.prototype.toString(10) as used by String():
// shortest round-trip digits, fixed notation for 1e-6 <= abs < 1e21, otherwise
// a normalized exponent, and zero always prints as "0" (including -0).
func jsNumberString(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 0):
		if value < 0 {
			return "-Infinity"
		}
		return "Infinity"
	case value == 0:
		return "0"
	}
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	mantissa, exponentText, found := strings.Cut(strconv.FormatFloat(value, 'e', -1, 64), "e")
	if !found {
		return sign + mantissa
	}
	exponent, err := strconv.Atoi(exponentText)
	if err != nil {
		return sign + mantissa + "e" + exponentText
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	k := len(digits)
	n := exponent + 1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	exp := n - 1
	expSign := "+"
	if exp < 0 {
		expSign = "-"
		exp = -exp
	}
	if k == 1 {
		return sign + digits + "e" + expSign + strconv.Itoa(exp)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + strconv.Itoa(exp)
}

// sliceHex is String.prototype.slice(0, n) for the ASCII hex strings the
// hash/checksum messages print.
func sliceHex(value string, start, end int) string {
	if start > len(value) {
		start = len(value)
	}
	if end > len(value) {
		end = len(value)
	}
	if end < start {
		end = start
	}
	return value[start:end]
}

// utf16Length mirrors String.prototype.length (UTF-16 code units).
func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

// utf16Slice mirrors String.prototype.slice over UTF-16 code units.
//
// Residual divergence: when the slice boundary splits a surrogate pair, TS
// keeps the lone surrogate (e.g. `slice(0, 16)` on 15 ASCII chars plus U+1F600
// ends "\ud83d") while utf16.Decode turns the unpaired unit into U+FFFD, and Go
// strings cannot hold a lone surrogate. CLI text output converges because both
// sides write U+FFFD there. JSON does not: the TS MCP server re-escapes the
// lone surrogate as "\ud83d" in its JSON, while Go emits U+FFFD. ASCII output
// (line numbers, hex hashes) is unaffected.
func utf16Slice(value string, start, end int) string {
	units := utf16.Encode([]rune(value))
	length := len(units)
	if start < 0 {
		start = length + start
		if start < 0 {
			start = 0
		}
	}
	if end < 0 {
		end = length + end
		if end < 0 {
			end = 0
		}
	}
	if start > length {
		start = length
	}
	if end > length {
		end = length
	}
	if end < start {
		end = start
	}
	return string(utf16.Decode(units[start:end]))
}
