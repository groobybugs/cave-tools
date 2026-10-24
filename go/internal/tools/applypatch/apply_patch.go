// Package applypatch ports the cave__apply_patch tool.
//
// Port of src/tools/apply-patch.ts.
package applypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

var (
	// port of src/tools/apply-patch.ts:normalizeUnicode (single quotes)
	reSingleQuotes = jsre.MustCompile(`[‘’‚‛]`, "g")
	// port of src/tools/apply-patch.ts:normalizeUnicode (double quotes)
	reDoubleQuotes = jsre.MustCompile(`[“”„‟]`, "g")
	// port of src/tools/apply-patch.ts:normalizeUnicode (dashes)
	reDashes = jsre.MustCompile(`[‐‑‒–—―]`, "g")
	// port of src/tools/apply-patch.ts:normalizeUnicode (ellipsis)
	reEllipsis = jsre.MustCompile(`…`, "g")
	// port of src/tools/apply-patch.ts:normalizeUnicode (NBSP)
	reNbsp = jsre.MustCompile(`\u00A0`, "g")
	// port of src/tools/apply-patch.ts:parsePatch (CRLF)
	reCRLF = jsre.MustCompile(`\r\n`, "g")
)

// Tool is the MCP tool definition for cave__apply_patch.
// port of src/tools/apply-patch.ts:applyPatchTool
var Tool = types.ToolDefinition{
	Name:        "cave__apply_patch",
	Description: "Apply a multi-file add/update/delete/move patch. All ops are verified before writing; a write-time error may leave partial state.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"patchText": {
				"type": "string"
			}
		},
		"required": ["patchText"]
	}`),
	Handler: handle,
}

// updateFileChunk is one @@ context block (or the whole-file legacy blob).
// port of src/tools/apply-patch.ts:UpdateFileChunk
type updateFileChunk struct {
	oldLines      []string
	newLines      []string
	changeContext *string
	isEndOfFile   bool
}

// hunk is one parsed file section of a patch.
// port of src/tools/apply-patch.ts:Hunk
type hunk struct {
	kind     string // "add" | "delete" | "update"
	path     string
	contents string
	movePath *string
	chunks   []updateFileChunk
}

// plannedChange is one mutation, fully resolved and computed.
// port of src/tools/apply-patch.ts:PlannedChange
type plannedChange struct {
	kind        string // "add" | "delete" | "update" | "move"
	path        string
	target      string
	content     string
	movePath    string
	moveTarget  string
	sourceBytes []byte
	bom         bool
}

// updateBucket accumulates chunks for one canonical source path.
// port of src/tools/apply-patch.ts:UpdateBucket
type updateBucket struct {
	sourceBytes []byte
	bom         bool
	decodedText string
	firstPath   string
	movePath    *string
}

// normalizeUnicode maps Unicode punctuation to ASCII equivalents.
// port of src/tools/apply-patch.ts:normalizeUnicode
func normalizeUnicode(value string) string {
	value = reSingleQuotes.ReplaceAllString(value, "'")
	value = reDoubleQuotes.ReplaceAllString(value, `"`)
	value = reDashes.ReplaceAllString(value, "-")
	value = reEllipsis.ReplaceAllString(value, "...")
	return reNbsp.ReplaceAllString(value, " ")
}

// comparator reports whether two lines match under one seek tier.
// port of src/tools/apply-patch.ts:Comparator
type comparator func(a, b string) bool

// tryMatch searches one tier, optionally anchoring at end of file first.
// port of src/tools/apply-patch.ts:tryMatch
func tryMatch(lines, pattern []string, startIndex int, compare comparator, eof bool) int {
	if eof {
		fromEnd := len(lines) - len(pattern)
		if fromEnd >= startIndex {
			matches := true
			for j := 0; j < len(pattern); j++ {
				if !compare(lines[fromEnd+j], pattern[j]) {
					matches = false
					break
				}
			}
			if matches {
				return fromEnd
			}
		}
	}

	for i := startIndex; i <= len(lines)-len(pattern); i++ {
		matches := true
		for j := 0; j < len(pattern); j++ {
			if !compare(lines[i+j], pattern[j]) {
				matches = false
				break
			}
		}
		if matches {
			return i
		}
	}

	return -1
}

// seekSequence finds pattern in lines through four progressively looser tiers.
// port of src/tools/apply-patch.ts:seekSequence
func seekSequence(lines, pattern []string, startIndex int, eof bool) int {
	if len(pattern) == 0 {
		return -1
	}

	if exact := tryMatch(lines, pattern, startIndex, func(a, b string) bool { return a == b }, eof); exact != -1 {
		return exact
	}
	if rstrip := tryMatch(lines, pattern, startIndex, func(a, b string) bool {
		return jsre.TrimEnd(a) == jsre.TrimEnd(b)
	}, eof); rstrip != -1 {
		return rstrip
	}
	if trim := tryMatch(lines, pattern, startIndex, func(a, b string) bool {
		return jsre.Trim(a) == jsre.Trim(b)
	}, eof); trim != -1 {
		return trim
	}
	return tryMatch(lines, pattern, startIndex, func(a, b string) bool {
		return normalizeUnicode(jsre.Trim(a)) == normalizeUnicode(jsre.Trim(b))
	}, eof)
}

// parsePatchHeader reads one "*** ... File:" header line.
// port of src/tools/apply-patch.ts:parsePatchHeader
func parsePatchHeader(lines []string, startIdx int) (filePath string, movePath *string, nextIdx int, ok bool) {
	line := lines[startIdx]

	if strings.HasPrefix(line, "*** Add File:") {
		filePath = jsre.Trim(line[len("*** Add File:"):])
		if filePath == "" {
			return "", nil, 0, false
		}
		return filePath, nil, startIdx + 1, true
	}

	if strings.HasPrefix(line, "*** Delete File:") {
		filePath = jsre.Trim(line[len("*** Delete File:"):])
		if filePath == "" {
			return "", nil, 0, false
		}
		return filePath, nil, startIdx + 1, true
	}

	if strings.HasPrefix(line, "*** Update File:") {
		filePath = jsre.Trim(line[len("*** Update File:"):])
		nextIdx = startIdx + 1
		if nextIdx < len(lines) && strings.HasPrefix(lines[nextIdx], "*** Move to:") {
			target := jsre.Trim(lines[nextIdx][len("*** Move to:"):])
			movePath = &target
			nextIdx++
		}
		if filePath == "" {
			return "", nil, 0, false
		}
		return filePath, movePath, nextIdx, true
	}

	return "", nil, 0, false
}

// parseUpdateFileChunks parses @@ blocks, or the whole-file legacy blob.
// port of src/tools/apply-patch.ts:parseUpdateFileChunks
func parseUpdateFileChunks(lines []string, startIdx int) ([]updateFileChunk, int) {
	chunks := []updateFileChunk{}
	i := startIdx

	// Backward-compatible whole-file blob: no @@ context headers. Cave extension.
	if i < len(lines) && !strings.HasPrefix(lines[i], "***") && !strings.HasPrefix(lines[i], "@@") {
		oldLines := []string{}
		newLines := []string{}
		isEndOfFile := false
		for i < len(lines) && !strings.HasPrefix(lines[i], "***") {
			line := lines[i]
			if line == "*** End of File" {
				isEndOfFile = true
				i++
				break
			}
			if strings.HasPrefix(line, " ") {
				content := line[1:]
				oldLines = append(oldLines, content)
				newLines = append(newLines, content)
			} else if strings.HasPrefix(line, "-") {
				oldLines = append(oldLines, line[1:])
			} else if strings.HasPrefix(line, "+") {
				newLines = append(newLines, line[1:])
			}
			i++
		}
		chunks = append(chunks, updateFileChunk{oldLines: oldLines, newLines: newLines, isEndOfFile: isEndOfFile})
		return chunks, i
	}

	for i < len(lines) && !strings.HasPrefix(lines[i], "***") {
		if strings.HasPrefix(lines[i], "@@") {
			contextLine := jsre.Trim(lines[i][2:])
			i++

			oldLines := []string{}
			newLines := []string{}
			isEndOfFile := false

			for i < len(lines) && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "***") {
				changeLine := lines[i]

				if changeLine == "*** End of File" {
					isEndOfFile = true
					i++
					break
				}

				if strings.HasPrefix(changeLine, " ") {
					content := changeLine[1:]
					oldLines = append(oldLines, content)
					newLines = append(newLines, content)
				} else if strings.HasPrefix(changeLine, "-") {
					oldLines = append(oldLines, changeLine[1:])
				} else if strings.HasPrefix(changeLine, "+") {
					newLines = append(newLines, changeLine[1:])
				}

				i++
			}

			var changeContext *string
			if contextLine != "" {
				changeContext = &contextLine
			}
			chunks = append(chunks, updateFileChunk{
				oldLines:      oldLines,
				newLines:      newLines,
				changeContext: changeContext,
				isEndOfFile:   isEndOfFile,
			})
		} else {
			i++
		}
	}

	return chunks, i
}

// parseAddFileContent collects the "+" body lines of an Add File hunk.
// port of src/tools/apply-patch.ts:parseAddFileContent
func parseAddFileContent(lines []string, startIdx int) (string, int) {
	content := ""
	i := startIdx

	for i < len(lines) && !strings.HasPrefix(lines[i], "***") {
		if strings.HasPrefix(lines[i], "+") {
			content += lines[i][1:] + "\n"
		}
		i++
	}

	if strings.HasSuffix(content, "\n") {
		content = content[:len(content)-1]
	}

	return content, i
}

// stripHeredoc unwraps `cat <<'EOF' ... EOF` patch text.
//
// The TS regex is /^(?:cat\s+)?<<['"]?(\w+)['"]?\s*\n([\s\S]*?)\n\1\s*$/,
// whose backreference RE2 cannot express, so the delimiter is compared by hand.
// port of src/tools/apply-patch.ts:stripHeredoc
func stripHeredoc(input string) string {
	i := 0

	if strings.HasPrefix(input, "cat") {
		j := 3
		if j >= len(input) {
			return input
		}
		r, size := utf8.DecodeRuneInString(input[j:])
		if !jsre.IsSpace(r) {
			return input
		}
		for j < len(input) {
			r, size = utf8.DecodeRuneInString(input[j:])
			if !jsre.IsSpace(r) {
				break
			}
			j += size
		}
		i = j
	}

	if !strings.HasPrefix(input[i:], "<<") {
		return input
	}
	i += 2

	if i < len(input) && (input[i] == '\'' || input[i] == '"') {
		i++
	}

	delimStart := i
	for i < len(input) {
		c := input[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == delimStart {
		return input
	}
	delim := input[delimStart:i]

	if i < len(input) && (input[i] == '\'' || input[i] == '"') {
		i++
	}

	// \s* then a literal \n: the greedy \s* may consume the newline itself and
	// backtrack, so take the first \n reached through whitespace only.
	newline := -1
	for j := i; j < len(input); {
		r, size := utf8.DecodeRuneInString(input[j:])
		if r == '\n' {
			newline = j
			j += size
			break
		}
		if !jsre.IsSpace(r) {
			break
		}
		j += size
	}
	if newline < 0 {
		return input
	}
	i = newline + 1
	bodyStart := i

	// The closing marker is the first "\n" + delimiter whose remainder is all
	// JS whitespace; the TS regex is anchored at end of input.
	needle := "\n" + delim
	for k := bodyStart; k+len(needle) <= len(input); k++ {
		if !strings.HasPrefix(input[k:], needle) {
			continue
		}
		rest := input[k+len(needle):]
		if allJSSpace(rest) {
			return input[bodyStart:k]
		}
	}

	return input
}

// allJSSpace reports whether every rune is JavaScript whitespace.
func allJSSpace(value string) bool {
	for _, r := range value {
		if !jsre.IsSpace(r) {
			return false
		}
	}
	return true
}

// parsePatch parses the patch text into hunks.
// port of src/tools/apply-patch.ts:parsePatch
func parsePatch(patchText string) ([]hunk, error) {
	cleaned := stripHeredoc(jsre.Trim(reCRLF.ReplaceAllString(patchText, "\n")))
	lines := strings.Split(cleaned, "\n")
	hunks := []hunk{}

	beginIdx := -1
	endIdx := -1
	for i, line := range lines {
		if beginIdx == -1 && jsre.Trim(line) == "*** Begin Patch" {
			beginIdx = i
		}
		if endIdx == -1 && jsre.Trim(line) == "*** End Patch" {
			endIdx = i
		}
	}

	if beginIdx == -1 || endIdx == -1 || beginIdx >= endIdx {
		return nil, errors.New("Invalid patch format: missing Begin/End markers")
	}

	i := beginIdx + 1

	for i < endIdx {
		filePath, movePath, nextIdx, ok := parsePatchHeader(lines, i)
		if !ok {
			i++
			continue
		}

		if strings.HasPrefix(lines[i], "*** Add File:") {
			content, next := parseAddFileContent(lines, nextIdx)
			hunks = append(hunks, hunk{kind: "add", path: filePath, contents: content})
			i = next
		} else if strings.HasPrefix(lines[i], "*** Delete File:") {
			hunks = append(hunks, hunk{kind: "delete", path: filePath})
			i = nextIdx
		} else if strings.HasPrefix(lines[i], "*** Update File:") {
			chunks, next := parseUpdateFileChunks(lines, nextIdx)
			hunks = append(hunks, hunk{kind: "update", path: filePath, movePath: movePath, chunks: chunks})
			i = next
		} else {
			i++
		}
	}

	return hunks, nil
}

// replacement is one [startIdx, oldLen, newLines] splice.
// port of src/tools/apply-patch.ts:computeReplacements (tuple)
type replacement struct {
	start    int
	oldLen   int
	newLines []string
}

// computeReplacements locates every chunk in the original lines.
// port of src/tools/apply-patch.ts:computeReplacements
func computeReplacements(originalLines []string, filePath string, chunks []updateFileChunk) ([]replacement, error) {
	replacements := []replacement{}
	lineIndex := 0

	for _, chunk := range chunks {
		if chunk.changeContext != nil {
			contextIdx := seekSequence(originalLines, []string{*chunk.changeContext}, lineIndex, false)
			if contextIdx == -1 {
				return nil, fmt.Errorf("Failed to find context '%s' in %s", *chunk.changeContext, filePath)
			}
			lineIndex = contextIdx + 1
		}

		if len(chunk.oldLines) == 0 {
			insertionIdx := len(originalLines)
			if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
				insertionIdx = len(originalLines) - 1
			}
			replacements = append(replacements, replacement{start: insertionIdx, oldLen: 0, newLines: chunk.newLines})
			continue
		}

		pattern := chunk.oldLines
		newSlice := chunk.newLines
		found := seekSequence(originalLines, pattern, lineIndex, chunk.isEndOfFile)

		if found == -1 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}
			found = seekSequence(originalLines, pattern, lineIndex, chunk.isEndOfFile)
		}

		if found != -1 {
			replacements = append(replacements, replacement{start: found, oldLen: len(pattern), newLines: newSlice})
			lineIndex = found + len(pattern)
		} else {
			return nil, errors.New("Failed to find expected lines in " + filePath + ":\n" + strings.Join(chunk.oldLines, "\n"))
		}
	}

	sortReplacements(replacements)

	return replacements, nil
}

// sortReplacements orders splices by start index (stable, ascending).
func sortReplacements(replacements []replacement) {
	sort.SliceStable(replacements, func(a, b int) bool {
		return replacements[a].start < replacements[b].start
	})
}

// applyReplacements applies splices in reverse order to avoid index shifting.
// port of src/tools/apply-patch.ts:applyReplacements
func applyReplacements(lines []string, replacements []replacement) []string {
	result := make([]string, len(lines))
	copy(result, lines)

	for i := len(replacements) - 1; i >= 0; i-- {
		item := replacements[i]
		next := make([]string, 0, len(result)-item.oldLen+len(item.newLines))
		next = append(next, result[:item.start]...)
		next = append(next, item.newLines...)
		next = append(next, result[item.start+item.oldLen:]...)
		result = next
	}

	return result
}

// deriveNewContents computes the patched text of one file.
// port of src/tools/apply-patch.ts:deriveNewContents
func deriveNewContents(filePath string, chunks []updateFileChunk, originalText string) (string, error) {
	originalLines := strings.Split(originalText, "\n")

	// Drop trailing empty element for consistent line counting.
	if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}

	replacements, err := computeReplacements(originalLines, filePath, chunks)
	if err != nil {
		return "", err
	}
	newLines := applyReplacements(originalLines, replacements)

	// Ensure trailing newline.
	if len(newLines) == 0 || newLines[len(newLines)-1] != "" {
		newLines = append(newLines, "")
	}

	return strings.Join(newLines, "\n"), nil
}

// port of src/tools/apply-patch.ts:ok
func okResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

// port of src/tools/apply-patch.ts:err
func errResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text("Error: " + text)}, IsError: true}
}

// verificationError wraps inner errors with the opencode-style prefix.
// port of src/tools/apply-patch.ts:verificationError
func verificationError(reason string) error {
	return errors.New("apply_patch verification failed: " + reason)
}

// ensureUpdateBucket reads the source once per canonical path and stacks
// further chunk-sets on the planned buffer.
// port of src/tools/apply-patch.ts:ensureUpdateBucket
func ensureUpdateBucket(existing *updateBucket, canonical string, chunks []updateFileChunk, firstPath string, movePath *string) (*updateBucket, error) {
	if existing == nil {
		sourceBytes, err := os.ReadFile(canonical)
		if err != nil {
			return nil, verificationError("Failed to read file to update: " + canonical + " (" + err.Error() + ")")
		}
		decoded, bom, decodeErr := runtime.DecodeUtf8PreserveBom(sourceBytes)
		if decodeErr != nil {
			return nil, verificationError("File is not valid UTF-8: " + canonical)
		}
		derived, deriveErr := deriveNewContents(canonical, chunks, decoded)
		if deriveErr != nil {
			return nil, verificationError(deriveErr.Error())
		}
		return &updateBucket{
			sourceBytes: sourceBytes,
			bom:         bom,
			decodedText: derived,
			firstPath:   firstPath,
			movePath:    movePath,
		}, nil
	}

	derived, deriveErr := deriveNewContents(canonical, chunks, existing.decodedText)
	if deriveErr != nil {
		return nil, verificationError(deriveErr.Error())
	}
	existing.decodedText = derived
	if movePath != nil {
		existing.movePath = movePath
	}
	return existing, nil
}

// planPatch resolves paths, reads sources and computes the resulting contents.
// port of src/tools/apply-patch.ts:planPatch
func planPatch(ctx context.Context, hunks []hunk) ([]plannedChange, error) {
	changes := []plannedChange{}
	updateBuckets := map[string]*updateBucket{}
	bucketOrder := []string{}
	addByPath := map[string]struct{}{}
	deleteByPath := map[string]struct{}{}
	updateInPlace := map[string]struct{}{}
	moveSources := map[string]struct{}{}
	moveDestinations := map[string]struct{}{}

	has := func(set map[string]struct{}, key string) bool {
		_, ok := set[key]
		return ok
	}
	reserveWrite := func(canonical, conflictLabel string) error {
		if has(addByPath, canonical) {
			return verificationError(conflictLabel + " " + canonical + ": already added by patch")
		}
		if has(deleteByPath, canonical) {
			return verificationError(conflictLabel + " " + canonical + ": already deleted by patch")
		}
		if has(updateInPlace, canonical) {
			return verificationError(conflictLabel + " " + canonical + ": already updated by patch")
		}
		if has(moveDestinations, canonical) {
			return verificationError(conflictLabel + " " + canonical + ": already a move destination")
		}
		if has(moveSources, canonical) {
			return verificationError(conflictLabel + " " + canonical + ": already a move source")
		}
		return nil
	}

	for _, item := range hunks {
		if item.kind == "add" {
			target, err := runtime.ResolveMutationTarget(ctx, item.path)
			if err != nil {
				return nil, err
			}
			canonical := target.Canonical
			if err := reserveWrite(canonical, "Patch adds"); err != nil {
				return nil, err
			}
			if _, ok := updateBuckets[canonical]; ok {
				return nil, verificationError("Patch adds same path as a planned update: " + canonical)
			}
			if target.Existed {
				info, statErr := os.Stat(canonical)
				if statErr != nil {
					return nil, verificationError("Failed to read file for add: " + canonical + " (" + statErr.Error() + ")")
				}
				if info.IsDir() {
					return nil, verificationError("Cannot write file over directory: " + canonical)
				}
			}
			addByPath[canonical] = struct{}{}
			content := item.contents
			if !strings.HasSuffix(content, "\n") && content != "" {
				content += "\n"
			}
			changes = append(changes, plannedChange{kind: "add", path: item.path, target: canonical, content: content})
			continue
		}

		if item.kind == "delete" {
			target, err := runtime.ResolveMutationTarget(ctx, item.path)
			if err != nil {
				return nil, err
			}
			canonical := target.Canonical
			if has(addByPath, canonical) {
				return nil, verificationError("Patch adds and deletes same path: " + canonical)
			}
			if has(deleteByPath, canonical) {
				return nil, verificationError("Patch deletes same path more than once: " + canonical)
			}
			if _, ok := updateBuckets[canonical]; ok {
				return nil, verificationError("Patch updates and deletes same path: " + canonical)
			}
			if has(moveSources, canonical) {
				return nil, verificationError("Patch moves and deletes same path: " + canonical)
			}
			if has(moveDestinations, canonical) {
				return nil, verificationError("Patch deletes a path that is also a move destination: " + canonical)
			}
			if !target.Existed {
				return nil, verificationError("Failed to read file for deletion: " + canonical)
			}
			info, statErr := os.Stat(canonical)
			if statErr != nil {
				return nil, verificationError("Failed to read file for deletion: " + canonical + " (" + statErr.Error() + ")")
			}
			if info.IsDir() {
				return nil, verificationError("Cannot delete directory with Delete File: " + canonical)
			}
			deleteByPath[canonical] = struct{}{}
			changes = append(changes, plannedChange{kind: "delete", path: item.path, target: canonical})
			continue
		}

		// update / move
		target, err := runtime.ResolveMutationTarget(ctx, item.path)
		if err != nil {
			return nil, err
		}
		canonical := target.Canonical
		if has(addByPath, canonical) {
			return nil, verificationError("Patch adds and updates same path: " + canonical)
		}
		if has(deleteByPath, canonical) {
			return nil, verificationError("Patch updates and deletes same path: " + canonical)
		}
		if has(moveSources, canonical) {
			if _, ok := updateBuckets[canonical]; !ok {
				return nil, verificationError("Patch updates another move's source: " + canonical)
			}
		}
		if item.movePath != nil && *item.movePath != "" && has(moveDestinations, canonical) {
			return nil, verificationError("Patch moves a path that is also a move destination: " + canonical)
		}
		if !target.Existed {
			return nil, verificationError("Failed to read file to update: " + canonical)
		}

		if item.movePath != nil && *item.movePath != "" {
			moveTarget, moveErr := runtime.ResolveMutationTarget(ctx, *item.movePath)
			if moveErr != nil {
				return nil, verificationError(moveErr.Error())
			}
			if moveTarget.Canonical == canonical {
				return nil, verificationError("Move source and destination resolve to same path: " + canonical)
			}
			if has(moveDestinations, moveTarget.Canonical) {
				return nil, verificationError("Patch moves multiple sources to same destination: " + moveTarget.Canonical)
			}
			if has(moveSources, moveTarget.Canonical) {
				return nil, verificationError("Patch move destination is also a move source: " + moveTarget.Canonical)
			}
			if has(addByPath, moveTarget.Canonical) {
				return nil, verificationError("Patch move destination is also added: " + moveTarget.Canonical)
			}
			if has(updateInPlace, moveTarget.Canonical) {
				return nil, verificationError("Patch move destination is also updated: " + moveTarget.Canonical)
			}
			if has(deleteByPath, moveTarget.Canonical) {
				return nil, verificationError("Patch move destination is also deleted: " + moveTarget.Canonical)
			}
			if moveTarget.Existed {
				info, statErr := os.Stat(moveTarget.Canonical)
				if statErr != nil {
					return nil, verificationError("Failed to read move destination: " + moveTarget.Canonical + " (" + statErr.Error() + ")")
				}
				if info.IsDir() {
					return nil, verificationError("Cannot write file over directory: " + moveTarget.Canonical)
				}
			}
			existing, ok := updateBuckets[canonical]
			if ok && existing.movePath != nil && *existing.movePath != "" && *existing.movePath != *item.movePath {
				return nil, verificationError("Patch moves same source to different destinations: " + canonical)
			}
			moveSources[canonical] = struct{}{}
			moveDestinations[moveTarget.Canonical] = struct{}{}
			bucket, bucketErr := ensureUpdateBucket(existing, canonical, item.chunks, item.path, item.movePath)
			if bucketErr != nil {
				return nil, bucketErr
			}
			if !ok {
				bucketOrder = append(bucketOrder, canonical)
			}
			updateBuckets[canonical] = bucket
			continue
		}

		// in-place update: must not collide with move destinations.
		if has(moveDestinations, canonical) {
			return nil, verificationError("Patch updates a path that is also a move destination: " + canonical)
		}
		updateInPlace[canonical] = struct{}{}
		existing, ok := updateBuckets[canonical]
		bucket, bucketErr := ensureUpdateBucket(existing, canonical, item.chunks, item.path, nil)
		if bucketErr != nil {
			return nil, bucketErr
		}
		if !ok {
			bucketOrder = append(bucketOrder, canonical)
		}
		updateBuckets[canonical] = bucket
	}

	// Materialize one planned update per canonical source path.
	for _, canonical := range bucketOrder {
		bucket := updateBuckets[canonical]
		if bucket.movePath != nil && *bucket.movePath != "" {
			moveTarget, err := runtime.ResolveMutationTarget(ctx, *bucket.movePath)
			if err != nil {
				return nil, err
			}
			if moveTarget.Canonical == canonical {
				return nil, verificationError("Move source and destination resolve to same path: " + canonical)
			}
			changes = append(changes, plannedChange{
				kind:        "move",
				path:        bucket.firstPath,
				target:      canonical,
				movePath:    *bucket.movePath,
				moveTarget:  moveTarget.Canonical,
				sourceBytes: bucket.sourceBytes,
				content:     bucket.decodedText,
				bom:         bucket.bom,
			})
		} else {
			changes = append(changes, plannedChange{
				kind:        "update",
				path:        bucket.firstPath,
				target:      canonical,
				sourceBytes: bucket.sourceBytes,
				content:     bucket.decodedText,
				bom:         bucket.bom,
			})
		}
	}

	return changes, nil
}

// applyPlannedChanges applies every planned mutation through the file locks.
// port of src/tools/apply-patch.ts:applyPlannedChanges
func applyPlannedChanges(ctx context.Context, changes []plannedChange) error {
	for _, change := range changes {
		if change.kind == "add" {
			if _, err := runtime.LockedWrite(ctx, change.target, change.content); err != nil {
				return err
			}
			compression.InvalidateFileCache(ctx, change.target)
			compression.InvalidateFileCache(ctx, resolveFromCwd(runtime.GetCwd(ctx), change.path))
			compression.RecordEdit(ctx, change.target)
			continue
		}

		if change.kind == "delete" {
			if err := os.Remove(change.target); err != nil {
				return err
			}
			compression.InvalidateFileCache(ctx, change.target)
			compression.InvalidateFileCache(ctx, resolveFromCwd(runtime.GetCwd(ctx), change.path))
			continue
		}

		// update / move share BOM-preserving write semantics.
		payload := runtime.JoinBom(change.content, change.bom)

		if change.kind == "update" {
			if _, err := runtime.WriteIfUnchanged(ctx, change.target, change.sourceBytes, payload); err != nil {
				return err
			}
			compression.InvalidateFileCache(ctx, change.target)
			compression.InvalidateFileCache(ctx, resolveFromCwd(runtime.GetCwd(ctx), change.path))
			compression.RecordEdit(ctx, change.target)
			continue
		}

		// move: re-check source unchanged before writing destination + removing
		// source.
		current, err := os.ReadFile(change.target)
		if err != nil {
			return err
		}
		if !runtime.SameBytes(current, change.sourceBytes) {
			return errors.New("File changed after it was read: " + change.target)
		}
		if _, err := runtime.LockedWrite(ctx, change.moveTarget, payload); err != nil {
			return err
		}
		if err := os.Remove(change.target); err != nil {
			return err
		}
		compression.InvalidateFileCache(ctx, change.target)
		compression.InvalidateFileCache(ctx, resolveFromCwd(runtime.GetCwd(ctx), change.path))
		compression.InvalidateFileCache(ctx, change.moveTarget)
		compression.InvalidateFileCache(ctx, resolveFromCwd(runtime.GetCwd(ctx), change.movePath))
		compression.RecordEdit(ctx, change.moveTarget)
	}
	return nil
}

// relativize prints a path relative to the call cwd when it is inside it.
// port of src/tools/apply-patch.ts:relativize
func relativize(ctx context.Context, absolute string) string {
	rel, err := filepath.Rel(runtime.GetCwd(ctx), absolute)
	if err != nil {
		rel = ""
	}
	if rel == "." {
		rel = ""
	}
	if rel != "" && !strings.HasPrefix(rel, "..") {
		return runtime.ToPosixPath(rel)
	}
	return runtime.ToPosixPath(absolute)
}

// resolveFromCwd mirrors Node's path.resolve(cwd, value).
func resolveFromCwd(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

// handle is the cave__apply_patch handler.
// port of src/tools/apply-patch.ts:applyPatchTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	patchText := ""
	if value, ok := args["patchText"]; ok {
		patchText = jsTruthyString(value)
	}
	if jsre.Trim(patchText) == "" {
		return errResult("patchText is required"), nil
	}

	hunks, parseErr := parsePatch(patchText)
	if parseErr != nil {
		return errResult("apply_patch verification failed: " + parseErr.Error()), nil
	}
	if len(hunks) == 0 {
		normalized := jsre.Trim(reCRLF.ReplaceAllString(patchText, "\n"))
		if normalized == "*** Begin Patch\n*** End Patch" {
			return errResult("patch rejected: empty patch"), nil
		}
		return errResult("apply_patch verification failed: no hunks found"), nil
	}

	changes, planErr := planPatch(ctx, hunks)
	if planErr != nil {
		return errResult(planErr.Error()), nil
	}

	if applyErr := applyPlannedChanges(ctx, changes); applyErr != nil {
		return errResult("apply_patch apply failed: " + applyErr.Error()), nil
	}

	summary := make([]string, len(changes))
	for i, change := range changes {
		switch change.kind {
		case "add":
			summary[i] = "A " + relativize(ctx, change.target)
		case "delete":
			summary[i] = "D " + relativize(ctx, change.target)
		case "move":
			summary[i] = "M " + relativize(ctx, change.moveTarget)
		default:
			summary[i] = "M " + relativize(ctx, change.target)
		}
	}

	return okResult("Success. Updated the following files:\n" + strings.Join(summary, "\n")), nil
}

// jsTruthyString mirrors `value || ""` followed by String() for JSON values.
func jsTruthyString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return ""
	case json.Number:
		if v.String() == "0" {
			return ""
		}
		return v.String()
	case float64:
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case int:
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	case int64:
		if v == 0 {
			return ""
		}
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}
