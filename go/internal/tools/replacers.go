package tools

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
)

// Cave edit matching: upstream tiers (exact/unicode/trailing, LF-view + EOL)
// live in edit_match.go. This file keeps the legacy fuzzy replacer chain as a
// Cave extension fallback plus the findReplacement adapter used by edit.

// Similarity thresholds for block anchor fallback matching (from opencode).
// port of src/tools/replacers.ts:SINGLE_CANDIDATE_SIMILARITY_THRESHOLD and
// MULTIPLE_CANDIDATES_SIMILARITY_THRESHOLD
const (
	singleCandidateSimilarityThreshold    = 0.65
	multipleCandidatesSimilarityThreshold = 0.65
)

// port of src/tools/replacers.ts:levenshtein
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	aUnits := utf16.Encode([]rune(a))
	bUnits := utf16.Encode([]rune(b))
	if len(aUnits) == 0 {
		return len(bUnits)
	}
	if len(bUnits) == 0 {
		return len(aUnits)
	}
	prev := make([]int, len(bUnits)+1)
	curr := make([]int, len(bUnits)+1)
	for j := 0; j <= len(bUnits); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(aUnits); i++ {
		curr[0] = i
		for j := 1; j <= len(bUnits); j++ {
			cost := 1
			if aUnits[i-1] == bUnits[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(bUnits)]
}

// replacerFunc yields candidate substrings of content treated as a match for
// find (TS: Generator<string>).
// port of src/tools/replacers.ts:Replacer
type replacerFunc func(content, find string) []string

// port of src/tools/replacers.ts:SimpleReplacer
func simpleReplacer(_ string, find string) []string {
	out := []string{find}
	if strings.Contains(find, "\n") && !strings.Contains(find, "\r") {
		out = append(out, strings.ReplaceAll(find, "\n", "\r\n"))
	} else if strings.Contains(find, "\r\n") {
		out = append(out, strings.ReplaceAll(find, "\r\n", "\n"))
	}
	return out
}

// port of src/tools/replacers.ts:LineTrimmedReplacer
func lineTrimmedReplacer(content, find string) []string {
	originalLines := strings.Split(content, "\n")
	searchLines := strings.Split(find, "\n")
	if searchLines[len(searchLines)-1] == "" {
		searchLines = searchLines[:len(searchLines)-1]
	}
	if len(searchLines) == 0 {
		return nil
	}
	out := []string{}
	for i := 0; i <= len(originalLines)-len(searchLines); i++ {
		matches := true
		for j := 0; j < len(searchLines); j++ {
			if jsre.Trim(originalLines[i+j]) != jsre.Trim(searchLines[j]) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		matchStart := 0
		for k := 0; k < i; k++ {
			matchStart += len(originalLines[k]) + 1
		}
		matchEnd := matchStart
		for k := 0; k < len(searchLines); k++ {
			matchEnd += len(originalLines[i+k]) + 1
		}
		out = append(out, jsSlice(content, matchStart, matchEnd-1))
	}
	return out
}

// port of src/tools/replacers.ts:BlockAnchorReplacer
func blockAnchorReplacer(content, find string) []string {
	originalLines := strings.Split(content, "\n")
	searchLines := strings.Split(find, "\n")

	if len(searchLines) < 3 {
		return nil
	}
	if searchLines[len(searchLines)-1] == "" {
		searchLines = searchLines[:len(searchLines)-1]
	}

	firstLineSearch := jsre.Trim(searchLines[0])
	lastLineSearch := jsre.Trim(searchLines[len(searchLines)-1])
	searchBlockSize := len(searchLines)
	maxLineDelta := max(1, searchBlockSize/4)

	// Collect all candidate positions where both anchors match.
	type candidate struct {
		startLine int
		endLine   int
	}
	candidates := []candidate{}
	for i := 0; i < len(originalLines); i++ {
		if jsre.Trim(originalLines[i]) != firstLineSearch {
			continue
		}
		// Look for the matching last line after this first line.
		for j := i + 2; j < len(originalLines); j++ {
			if jsre.Trim(originalLines[j]) == lastLineSearch {
				actualBlockSize := j - i + 1
				if abs(actualBlockSize-searchBlockSize) <= maxLineDelta {
					candidates = append(candidates, candidate{startLine: i, endLine: j})
				}
				break // Only match the first occurrence of the last line.
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	spanOf := func(startLine, endLine int) string {
		matchStartIndex := 0
		for k := 0; k < startLine; k++ {
			matchStartIndex += len(originalLines[k]) + 1
		}
		matchEndIndex := matchStartIndex
		for k := startLine; k <= endLine; k++ {
			matchEndIndex += len(originalLines[k])
			if k < endLine {
				matchEndIndex++
			}
		}
		return jsSubstring(content, matchStartIndex, matchEndIndex)
	}

	similarityOf := func(startLine, endLine int) float64 {
		actualBlockSize := endLine - startLine + 1
		similarity := 0.0
		linesToCheck := min(searchBlockSize-2, actualBlockSize-2) // Middle lines only.
		if linesToCheck > 0 {
			for j := 1; j < searchBlockSize-1 && j < actualBlockSize-1; j++ {
				originalLine := jsre.Trim(originalLines[startLine+j])
				searchLine := jsre.Trim(searchLines[j])
				maxLen := max(utf16Len(originalLine), utf16Len(searchLine))
				if maxLen == 0 {
					continue
				}
				distance := levenshtein(originalLine, searchLine)
				similarity += (1 - float64(distance)/float64(maxLen)) / float64(linesToCheck)
			}
			return similarity
		}
		// No middle lines to compare, just accept based on anchors.
		return 1.0
	}

	// Handle single candidate scenario (using relaxed threshold).
	if len(candidates) == 1 {
		c := candidates[0]
		if similarityOf(c.startLine, c.endLine) >= singleCandidateSimilarityThreshold {
			return []string{spanOf(c.startLine, c.endLine)}
		}
		return nil
	}

	// Calculate similarity for multiple candidates.
	bestMatch := candidate{startLine: -1, endLine: -1}
	maxSimilarity := -1.0
	for _, c := range candidates {
		actualBlockSize := c.endLine - c.startLine + 1
		linesToCheck := min(searchBlockSize-2, actualBlockSize-2)
		similarity := 0.0
		if linesToCheck > 0 {
			for j := 1; j < searchBlockSize-1 && j < actualBlockSize-1; j++ {
				originalLine := jsre.Trim(originalLines[c.startLine+j])
				searchLine := jsre.Trim(searchLines[j])
				maxLen := max(utf16Len(originalLine), utf16Len(searchLine))
				if maxLen == 0 {
					continue
				}
				distance := levenshtein(originalLine, searchLine)
				similarity += 1 - float64(distance)/float64(maxLen)
			}
			similarity /= float64(linesToCheck)
		} else {
			// No middle lines to compare, just accept based on anchors.
			similarity = 1.0
		}
		if similarity > maxSimilarity {
			maxSimilarity = similarity
			bestMatch = c
		}
	}
	if maxSimilarity >= multipleCandidatesSimilarityThreshold && bestMatch.startLine >= 0 {
		return []string{spanOf(bestMatch.startLine, bestMatch.endLine)}
	}
	return nil
}

var (
	reWhitespaceRun = jsre.MustCompile(`\s+`, "g")
	// port of src/tools/replacers.ts:WhitespaceNormalizedReplacer (metachar escape)
	reRegexMetachars = jsre.MustCompile(`[.*+?^${}()|[\]\\]`, "g")
)

// port of src/tools/replacers.ts:WhitespaceNormalizedReplacer
func whitespaceNormalizedReplacer(content, find string) []string {
	normalize := func(s string) string {
		return jsre.Trim(reWhitespaceRun.ReplaceAllString(s, " "))
	}
	normFind := normalize(find)
	if len(normFind) == 0 {
		return nil
	}

	out := []string{}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if normalize(line) == normFind {
			out = append(out, line)
			continue
		}
		if strings.Contains(normalize(line), normFind) {
			words := jsre.SplitWS(jsre.Trim(find))
			if len(words) == 0 {
				continue
			}
			parts := make([]string, len(words))
			for i, w := range words {
				parts[i] = reRegexMetachars.ReplaceAllStringFunc(w, func(match string) string {
					return "\\" + match
				})
			}
			pattern := strings.Join(parts, `\s+`)
			re, err := jsre.Compile(pattern, "")
			if err != nil {
				continue // Invalid regex, skip.
			}
			if loc := re.FindStringIndex(line); loc != nil {
				out = append(out, line[loc[0]:loc[1]])
			}
		}
	}

	findLines := strings.Split(find, "\n")
	if len(findLines) > 1 {
		for i := 0; i <= len(lines)-len(findLines); i++ {
			block := strings.Join(lines[i:i+len(findLines)], "\n")
			if normalize(block) == normFind {
				out = append(out, block)
			}
		}
	}
	return out
}

// removeIndent strips the common minimum indent from text.
// port of src/tools/replacers.ts:IndentationFlexibleReplacer (removeIndent)
func removeIndent(text string) string {
	lines := strings.Split(text, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, l := range lines {
		if len(jsre.Trim(l)) > 0 {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return text
	}
	minIndent := -1
	for _, l := range nonEmpty {
		indent := 0
		for _, r := range l {
			if !jsre.IsSpace(r) {
				break
			}
			indent++
		}
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = jsSliceRunes(l, minIndent)
	}
	return strings.Join(out, "\n")
}

// port of src/tools/replacers.ts:IndentationFlexibleReplacer
func indentationFlexibleReplacer(content, find string) []string {
	normFind := removeIndent(find)
	contentLines := strings.Split(content, "\n")
	findLines := strings.Split(find, "\n")
	out := []string{}
	for i := 0; i <= len(contentLines)-len(findLines); i++ {
		block := strings.Join(contentLines[i:i+len(findLines)], "\n")
		if removeIndent(block) == normFind {
			out = append(out, block)
		}
	}
	return out
}

// port of src/tools/replacers.ts:EscapeNormalizedReplacer (unescape)
func unescapeFind(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, "\t")
	s = strings.ReplaceAll(s, `\r`, "\r")
	s = strings.ReplaceAll(s, `\'`, "'")
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, "\\`", "`")
	s = strings.ReplaceAll(s, `\$`, "$")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

// port of src/tools/replacers.ts:EscapeNormalizedReplacer
func escapeNormalizedReplacer(content, find string) []string {
	unescapedFind := unescapeFind(find)
	if unescapedFind == find {
		return nil
	}
	out := []string{}
	if strings.Contains(content, unescapedFind) {
		out = append(out, unescapedFind)
	}

	lines := strings.Split(content, "\n")
	findLines := strings.Split(unescapedFind, "\n")
	for i := 0; i <= len(lines)-len(findLines); i++ {
		block := strings.Join(lines[i:i+len(findLines)], "\n")
		if unescapeFind(block) == unescapedFind {
			out = append(out, block)
		}
	}
	return out
}

// port of src/tools/replacers.ts:TrimmedBoundaryReplacer
func trimmedBoundaryReplacer(content, find string) []string {
	trimmedFind := jsre.Trim(find)
	if trimmedFind == find {
		return nil
	}
	out := []string{}
	if strings.Contains(content, trimmedFind) {
		out = append(out, trimmedFind)
	}
	lines := strings.Split(content, "\n")
	findLines := strings.Split(find, "\n")
	for i := 0; i <= len(lines)-len(findLines); i++ {
		block := strings.Join(lines[i:i+len(findLines)], "\n")
		if jsre.Trim(block) == trimmedFind {
			out = append(out, block)
		}
	}
	return out
}

// port of src/tools/replacers.ts:ContextAwareReplacer
func contextAwareReplacer(content, find string) []string {
	findLines := strings.Split(find, "\n")
	if findLines[len(findLines)-1] == "" {
		findLines = findLines[:len(findLines)-1]
	}
	if len(findLines) < 3 {
		return nil
	}

	contentLines := strings.Split(content, "\n")
	first := jsre.Trim(findLines[0])
	last := jsre.Trim(findLines[len(findLines)-1])
	middle := []string{}
	for _, l := range findLines[1 : len(findLines)-1] {
		if len(jsre.Trim(l)) > 0 {
			middle = append(middle, l)
		}
	}

	out := []string{}
	for i := 0; i < len(contentLines); i++ {
		if jsre.Trim(contentLines[i]) != first {
			continue
		}
		for j := i + 2; j < len(contentLines); j++ {
			if jsre.Trim(contentLines[j]) != last {
				continue
			}
			block := contentLines[i : j+1]
			matches := 0
			for _, fl := range middle {
				for _, bl := range block {
					if jsre.Trim(bl) == jsre.Trim(fl) {
						matches++
						break
					}
				}
			}
			if len(middle) == 0 || float64(matches)/float64(len(middle)) >= 0.5 {
				out = append(out, strings.Join(block, "\n"))
			}
			break
		}
	}
	return out
}

// port of src/tools/replacers.ts:MultiOccurrenceReplacer
func multiOccurrenceReplacer(content, find string) []string {
	out := []string{}
	startIndex := 0
	for {
		index := strings.Index(content[startIndex:], find)
		if index == -1 {
			break
		}
		index += startIndex
		out = append(out, find)
		startIndex = index + len(find)
	}
	return out
}

// replacers mirrors REPLACERS in source order.
// port of src/tools/replacers.ts:REPLACERS
var replacers = []replacerFunc{
	simpleReplacer,
	lineTrimmedReplacer,
	blockAnchorReplacer,
	whitespaceNormalizedReplacer,
	indentationFlexibleReplacer,
	escapeNormalizedReplacer,
	trimmedBoundaryReplacer,
	contextAwareReplacer,
	multiOccurrenceReplacer,
}

// isDisproportionateMatch guards against a short find matching a wildly larger
// span (e.g. a single normalized line collapsing onto a giant region).
// port of src/tools/replacers.ts:isDisproportionateMatch
func isDisproportionateMatch(search, oldString string) bool {
	oldLines := strings.Count(oldString, "\n") + 1
	searchLines := strings.Count(search, "\n") + 1
	if searchLines >= max(oldLines+3, oldLines*2) {
		return true
	}
	if oldLines == 1 {
		return false
	}
	oldTrim := utf16Len(jsre.Trim(oldString))
	return utf16Len(jsre.Trim(search)) > max(oldTrim+500, oldTrim*4)
}

// MatchResult is the findReplacement outcome: the actual substring of content
// to replace plus its first index.
// port of src/tools/replacers.ts:MatchResult
type MatchResult struct {
	Search    *string
	Index     *int
	Error     string
	NonUnique bool
}

func matchFound(search string, index int) MatchResult {
	return MatchResult{Search: &search, Index: &index}
}

// FindReplacement returns the actual substring of content to replace plus its
// first index. `search` is the literal text present in content (it may differ
// from find when a fuzzy replacer matched). For replaceAll the caller replaces
// every occurrence of search; otherwise uniqueness is enforced here.
// port of src/tools/replacers.ts:findReplacement
func FindReplacement(ctx context.Context, content, find string, replaceAll bool) MatchResult {
	if find == "" {
		return MatchResult{Error: "not-found: old_string cannot be empty."}
	}
	// Tiers 1-3 via the canonical LF-view matcher (matches carry source offsets).
	// FindReplacement takes no newString, so the replacement half is unused here.
	spans := MatchView(content, find, find).Matches
	if len(spans) > 0 {
		if replaceAll || len(spans) == 1 {
			m := spans[0]
			return matchFound(content[m.Start:m.End], m.Start)
		}
		return MatchResult{Error: "non-unique", NonUnique: true}
	}

	// Tier 4 (cave extension): legacy fuzzy chain.
	return findFuzzyCandidate(ctx, content, find, replaceAll)
}

// port of src/tools/replacers.ts:findFuzzyCandidate
func findFuzzyCandidate(ctx context.Context, content, find string, replaceAll bool) MatchResult {
	if runtime.GetEnv(ctx)["CAVE_TOOLS_FUZZY_EDIT"] == "0" {
		return MatchResult{Error: "not-found: Could not find oldString in the file. It must match exactly, including whitespace, indentation, and line endings. Re-read the file and provide the full exact oldString."}
	}

	nonUniqueSeen := false

	for _, replacer := range replacers {
		for _, candidate := range replacer(content, find) {
			if candidate == "" {
				continue
			}
			index := strings.Index(content, candidate)
			if index == -1 {
				continue
			}
			if isDisproportionateMatch(candidate, find) {
				continue
			}

			if replaceAll {
				return matchFound(candidate, index)
			}

			if strings.Index(content, candidate) == strings.LastIndex(content, candidate) {
				return matchFound(candidate, index)
			}
			nonUniqueSeen = true
		}
	}

	if nonUniqueSeen {
		return MatchResult{Error: "non-unique", NonUnique: true}
	}
	return MatchResult{Error: "not-found: Could not find oldString in the file. It must match exactly (or via fuzzy match), including whitespace, indentation, and line endings. Re-read the file and provide the full exact oldString."}
}

// FuzzyEditResult is the ComputeEditResult outcome: an EditResult plus the
// fuzzy marker that distinguishes the Cave extension from upstream tiers.
// port of src/tools/replacers.ts:computeEditResult
type FuzzyEditResult struct {
	EditResult
	Fuzzy bool
}

// ComputeEditResult is the complete edit computation for the edit handler.
// Pure upstream semantics first (ComputeEdit); only a pure not-found falls
// through to the legacy fuzzy chain, and only when CAVE_TOOLS_FUZZY_EDIT is not
// "0". Ambiguity, identical/empty, and unchanged rejects are final.
// port of src/tools/replacers.ts:computeEditResult
func ComputeEditResult(ctx context.Context, source, oldString, newString string, replaceAll bool) FuzzyEditResult {
	pure := ComputeEdit(source, oldString, newString, replaceAll)
	if pure.OK {
		return FuzzyEditResult{EditResult: pure}
	}
	// Final rejects: ambiguity carries spans; identical/empty/unchanged and the
	// endingsOnly raw miss never fall through to fuzzy.
	if pure.Replacements > 0 || pure.EndingsOnly {
		return FuzzyEditResult{EditResult: pure}
	}
	if pure.NonUnique {
		return FuzzyEditResult{EditResult: pure}
	}
	if oldString == "" || oldString == newString {
		return FuzzyEditResult{EditResult: pure}
	}
	if runtime.GetEnv(ctx)["CAVE_TOOLS_FUZZY_EDIT"] == "0" {
		return FuzzyEditResult{EditResult: pure}
	}
	fb := findFuzzyCandidate(ctx, source, oldString, replaceAll)
	if fb.NonUnique {
		// Zero upstream spans but ambiguous legacy-fuzzy candidates: keep the
		// historical non-unique semantics (not not-found).
		return FuzzyEditResult{
			EditResult: EditResult{
				Error:     "old_string is not unique (2+ matches). Add more surrounding context or set replace_all.",
				NonUnique: true,
			},
			Fuzzy: true,
		}
	}
	if fb.Error != "" || fb.Search == nil || fb.Index == nil {
		return FuzzyEditResult{EditResult: pure}
	}
	spans := FindOccurrences(source, *fb.Search)
	if len(spans) == 0 {
		return FuzzyEditResult{EditResult: pure}
	}
	if len(spans) > 1 && !replaceAll {
		return FuzzyEditResult{
			EditResult: EditResult{
				Error:        "Found " + strconv.Itoa(len(spans)) + " matches for oldString, but expected exactly one. Add more surrounding context to make oldString unique, or set replaceAll to true to replace every occurrence.",
				NonUnique:    true,
				Matches:      spans,
				Replacements: len(spans),
			},
			Fuzzy: true,
		}
	}
	// Per-span region-aware EOL (same rule as upstream): each fuzzy span keeps
	// its own region endings instead of sharing the first span's bytes.
	fileEnding := LineEnding(source, "\n")
	replacementView := strings.ReplaceAll(newString, "\r\n", "\n")
	targets := spans
	if !replaceAll {
		targets = spans[:1]
	}
	applied := make([]EditApplied, len(targets))
	for i, m := range targets {
		region := source[m.Start:m.End]
		applied[i] = EditApplied{
			EditSpan:    m,
			Replacement: WithRegionEndings(region, replacementView, LineEnding(region, fileEnding)),
		}
	}
	updated := source
	for i := len(applied) - 1; i >= 0; i-- {
		item := applied[i]
		updated = updated[:item.Start] + item.Replacement + updated[item.End:]
	}
	if updated == source {
		return FuzzyEditResult{
			EditResult: EditResult{
				Error:        "No changes to apply: the edit leaves the file unchanged. Line endings follow the file; to change them, write \\r explicitly in oldString or newString.",
				Matches:      spans,
				Replacements: len(spans),
			},
			Fuzzy: true,
		}
	}
	return FuzzyEditResult{
		EditResult: EditResult{
			OK:           true,
			Matches:      spans,
			Applied:      applied,
			Replacements: len(spans),
			Updated:      updated,
		},
		Fuzzy: true,
	}
}

// utf16Len mirrors JS String.prototype.length (UTF-16 code units).
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// jsSlice mirrors String.prototype.slice for non-negative bounds; JS clamps
// out-of-range bounds instead of panicking.
func jsSlice(s string, start, end int) string {
	n := len(s)
	if start < 0 {
		start += n
		if start < 0 {
			start = 0
		}
	} else if start > n {
		start = n
	}
	if end < 0 {
		end += n
		if end < 0 {
			end = 0
		}
	} else if end > n {
		end = n
	}
	if end < start {
		end = start
	}
	return s[start:end]
}

// jsSubstring mirrors String.prototype.substring (clamp and swap).
func jsSubstring(s string, start, end int) string {
	n := len(s)
	start = min(max(start, 0), n)
	end = min(max(end, 0), n)
	if start > end {
		start, end = end, start
	}
	return s[start:end]
}

// jsSliceRunes drops up to count leading runes (JS slice by code units; every
// whitespace rune is a single UTF-16 unit).
func jsSliceRunes(s string, count int) string {
	if count <= 0 {
		return s
	}
	index := 0
	removed := 0
	for index < len(s) && removed < count {
		_, size := utf8.DecodeRuneInString(s[index:])
		index += size
		removed++
	}
	return s[index:]
}

// abs is the integer absolute value.
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
