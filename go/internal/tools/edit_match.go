package tools

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// Canonical OpenCode string-edit matching + EOL semantics.
//
// Port of src/tools/edit-match.ts, itself ported from OpenCode v2.1.2
// packages/core/src/tool/plugin/edit.ts. Tier priority mirrors upstream
// exactly: exact wins outright (unicode/trailing skipped when exact is
// non-empty); unicode wins over trailing. A multi-match tier without
// replaceAll is an immediate uniqueness error — no fall-through.

// crlf is the CRLF sequence as a plain string.
const crlf = "\r\n"

var (
	// port of src/tools/edit-match.ts:normalizeForMatch (first rule)
	reNormalizeSingleQuotes = jsre.MustCompile(`[‘’‚‛]`, "g")
	// port of src/tools/edit-match.ts:normalizeForMatch (second rule)
	reNormalizeDoubleQuotes = jsre.MustCompile(`[“”„‟]`, "g")
	// port of src/tools/edit-match.ts:normalizeForMatch (third rule)
	reNormalizeDashes = jsre.MustCompile(`[‐‑‒–—―−]`, "g")
	// port of src/tools/edit-match.ts:normalizeForMatch (fourth rule)
	reNormalizeUnicodeSpaces = jsre.MustCompile(`[\u00A0\u2002-\u200A\u202F\u205F\u3000]`, "g")
	// port of src/tools/edit-match.ts:matchView (CRLF positions)
	reCRLF = jsre.MustCompile(`\r\n`, "g")
	// port of src/tools/edit-match.ts:withRegionEndings (region split)
	reRegionNewline = jsre.MustCompile(`\r?\n`, "g")
)

// EditSpan is a half-open [start, end) byte span in the source string.
// port of src/tools/edit-match.ts:EditSpan
type EditSpan struct {
	Start int
	End   int
}

// EditApplied is one span plus its computed replacement text.
// port of src/tools/edit-match.ts:EditApplied
type EditApplied struct {
	EditSpan
	Replacement string
}

// EditResult is the ComputeEdit outcome: the TS success/error union.
// port of src/tools/edit-match.ts:EditResult
type EditResult struct {
	OK           bool
	Matches      []EditSpan
	Applied      []EditApplied
	Replacements int
	Updated      string
	EndingsOnly  bool
	Error        string
	NonUnique    bool
}

// NormalizeForMatch applies one-char-to-one-char punctuation/space
// normalization; offsets stay valid because every rule replaces one character
// with one character.
// port of src/tools/edit-match.ts:normalizeForMatch
func NormalizeForMatch(value string) string {
	value = reNormalizeSingleQuotes.ReplaceAllLiteralString(value, "'")
	value = reNormalizeDoubleQuotes.ReplaceAllLiteralString(value, `"`)
	value = reNormalizeDashes.ReplaceAllLiteralString(value, "-")
	value = reNormalizeUnicodeSpaces.ReplaceAllLiteralString(value, " ")
	return value
}

// normalizeForMatchWithOffsets normalizes value and returns, for every byte of
// the normalized result plus one final sentinel, the byte offset it came from
// in value. JS offsets are UTF-16 code units, so they survive normalization
// untouched; Go's byte offsets do not, and the unicode tier must map them back
// before slicing the source.
func normalizeForMatchWithOffsets(value string) (string, []int) {
	norm := NormalizeForMatch(value)
	offsets := make([]int, 0, len(norm)+1)
	vi := 0
	ni := 0
	for vi < len(value) && ni < len(norm) {
		_, valueSize := utf8.DecodeRuneInString(value[vi:])
		_, normSize := utf8.DecodeRuneInString(norm[ni:])
		for k := 0; k < normSize; k++ {
			offsets = append(offsets, vi)
		}
		vi += valueSize
		ni += normSize
	}
	for len(offsets) < len(norm) {
		offsets = append(offsets, vi)
	}
	offsets = append(offsets, len(value))
	return norm, offsets
}

// FindOccurrences returns the non-overlapping occurrences of search (upstream
// stride: offset += search.length).
// port of src/tools/edit-match.ts:findOccurrences
func FindOccurrences(content, search string) []EditSpan {
	result := []EditSpan{}
	if search == "" {
		return result
	}
	offset := 0
	for {
		at := strings.Index(content[offset:], search)
		if at == -1 {
			break
		}
		at += offset
		result = append(result, EditSpan{Start: at, End: at + len(search)})
		offset = at + len(search)
	}
	return result
}

// lineWindow is one content line for the trailing-whitespace window tier.
type lineWindow struct {
	start      int
	end        int
	text       string
	contentEnd int
	newline    bool
}

// FindLineOccurrences is the sliding line window comparing trimEnd+normalized
// lines. It tolerates trailing-whitespace drift, nothing else. Non-overlap
// dedup counting is upstream behavior (intentional).
// port of src/tools/edit-match.ts:findLineOccurrences
func FindLineOccurrences(content, search string) []EditSpan {
	trailingNewline := strings.HasSuffix(search, "\n")
	expected := strings.Split(search, "\n")
	if trailingNewline {
		expected = expected[:len(expected)-1]
	}
	if len(expected) == 0 {
		return []EditSpan{}
	}

	lines := []lineWindow{}
	for start := 0; start < len(content); {
		index := strings.IndexByte(content[start:], '\n')
		if index < 0 {
			text := content[start:]
			lines = append(lines, lineWindow{
				start:      start,
				end:        len(content),
				text:       text,
				contentEnd: start + len(text),
				newline:    false,
			})
			break
		}
		end := start + index + 1
		text := content[start : start+index]
		contentEnd := start + len(text)
		if strings.HasSuffix(text, "\r") {
			contentEnd--
		}
		lines = append(lines, lineWindow{
			start:      start,
			end:        end,
			text:       text,
			contentEnd: contentEnd,
			newline:    true,
		})
		start = end
	}

	candidates := []EditSpan{}
	for index := 0; index < len(lines); index++ {
		if index+len(expected) > len(lines) {
			continue
		}
		actual := lines[index : index+len(expected)]
		allMatch := true
		for lineIndex := range actual {
			if NormalizeForMatch(jsre.TrimEnd(actual[lineIndex].text)) !=
				NormalizeForMatch(jsre.TrimEnd(expected[lineIndex])) {
				allMatch = false
				break
			}
		}
		if !allMatch {
			continue
		}
		last := actual[len(actual)-1]
		if trailingNewline && !last.newline {
			continue
		}
		end := last.contentEnd
		if trailingNewline {
			end = last.end
		}
		candidates = append(candidates, EditSpan{Start: actual[0].start, End: end})
	}

	deduped := []EditSpan{}
	for _, candidate := range candidates {
		overlaps := false
		for _, kept := range deduped {
			if kept.End > candidate.Start && kept.Start < candidate.End {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		deduped = append(deduped, candidate)
	}
	return deduped
}

// LineEnding is the dominant line ending in text, or fallback when there is
// none/tie.
// port of src/tools/edit-match.ts:lineEnding
func LineEnding(text, fallback string) string {
	crlfCount := strings.Count(text, crlf)
	lfCount := strings.Count(text, "\n") - crlfCount
	if crlfCount == lfCount {
		return fallback
	}
	if crlfCount > lfCount {
		return crlf
	}
	return "\n"
}

// AlignLines returns, for each line of after, the index of the before line it
// keeps (longest common subsequence) or, between kept lines, the line it takes
// the place of; -1 means no counterpart (TS undefined).
// port of src/tools/edit-match.ts:alignLines
func AlignLines(before, after []string) []int {
	width := len(after) + 1
	table := make([]uint32, (len(before)+1)*width)
	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				table[i*width+j] = table[(i+1)*width+j+1] + 1
			} else {
				table[i*width+j] = max(table[(i+1)*width+j], table[i*width+j+1])
			}
		}
	}
	result := make([]int, len(after))
	for i := range result {
		result[i] = -1
	}
	gapBefore := []int{}
	gapAfter := []int{}
	flush := func() {
		for k, j := range gapAfter {
			if k < len(gapBefore) {
				result[j] = gapBefore[k]
			}
		}
		gapBefore = nil
		gapAfter = nil
	}
	i := 0
	j := 0
	for i < len(before) && j < len(after) {
		if before[i] == after[j] {
			flush()
			result[j] = i
			j++
			i++
			continue
		}
		if table[(i+1)*width+j] >= table[i*width+j+1] {
			gapBefore = append(gapBefore, i)
			i++
		} else {
			gapAfter = append(gapAfter, j)
			j++
		}
	}
	for i < len(before) {
		gapBefore = append(gapBefore, i)
		i++
	}
	for j < len(after) {
		gapAfter = append(gapAfter, j)
		j++
	}
	flush()
	return result
}

// WithRegionEndings joins the replacement's lines with line endings taken from
// the matched source region. A replacement line that keeps a region line
// (longest common subsequence, compared like the trailing-whitespace tier)
// reuses that line's ending; a line that changes in place reuses the ending of
// the line it replaces; only lines beyond those take fallback.
// port of src/tools/edit-match.ts:withRegionEndings
func WithRegionEndings(region, replacement, fallback string) string {
	before := reRegionNewline.Split(region, -1)
	endings := reRegionNewline.FindAllString(region, -1)
	after := strings.Split(replacement, "\n")
	key := func(line string) string {
		return NormalizeForMatch(jsre.TrimEnd(line))
	}
	beforeKeys := make([]string, len(before))
	for i, line := range before {
		beforeKeys[i] = key(line)
	}
	afterKeys := make([]string, len(after))
	for i, line := range after {
		afterKeys[i] = key(line)
	}
	source := AlignLines(beforeKeys, afterKeys)
	var out strings.Builder
	for index, line := range after {
		out.WriteString(line)
		if index == len(after)-1 {
			continue
		}
		ending := fallback
		if src := source[index]; src >= 0 && src < len(endings) {
			ending = endings[src]
		}
		out.WriteString(ending)
	}
	return out.String()
}

// CountBefore is the number of sorted positions strictly before offset.
// port of src/tools/edit-match.ts:countBefore
func CountBefore(positions []int, offset int) int {
	low := 0
	high := len(positions)
	for low < high {
		middle := (low + high) >> 1
		if positions[middle] < offset {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

// IsEndingsOnlyChange reports whether old/new differ only in \r (a
// line-ending-only change request).
// port of src/tools/edit-match.ts:isEndingsOnlyChange
func IsEndingsOnlyChange(oldString, newString string) bool {
	return strings.ReplaceAll(oldString, "\r", "") == strings.ReplaceAll(newString, "\r", "")
}

// MatchViewResult is the MatchView outcome: matched source spans plus the
// per-span replacement function.
// port of src/tools/edit-match.ts:matchView
type MatchViewResult struct {
	Matches []EditSpan
	Replace func(match EditSpan) string
}

// MatchView matches against the LF view Read shows the model, then maps
// offsets back to the source, so CRLF and mixed line endings neither block
// matches nor leak into untouched regions. Tier priority is exact, then
// unicode-normalized, then trailing-whitespace line window.
// port of src/tools/edit-match.ts:matchView
func MatchView(source, oldString, newString string) MatchViewResult {
	view := strings.ReplaceAll(source, crlf, "\n")
	removed := []int{}
	for index, match := range reCRLF.FindAllStringIndex(source, -1) {
		removed = append(removed, match[0]-index)
	}
	fileEnding := LineEnding(source, "\n")
	search := strings.ReplaceAll(oldString, crlf, "\n")
	replacement := strings.ReplaceAll(newString, crlf, "\n")

	exact := FindOccurrences(view, search)
	// These one-to-one mappings preserve offsets into the LF view.
	unicode := []EditSpan{}
	if len(exact) == 0 {
		normView, offsets := normalizeForMatchWithOffsets(view)
		normSearch := NormalizeForMatch(search)
		unicode = FindOccurrences(normView, normSearch)
		for i := range unicode {
			unicode[i] = EditSpan{Start: offsets[unicode[i].Start], End: offsets[unicode[i].End]}
		}
	}
	trailing := []EditSpan{}
	if len(exact) == 0 && len(unicode) == 0 {
		trailing = FindLineOccurrences(view, search)
	}
	picked := exact
	if len(picked) == 0 {
		picked = unicode
	}
	if len(picked) == 0 {
		picked = trailing
	}

	matches := make([]EditSpan, len(picked))
	for i, match := range picked {
		matches[i] = EditSpan{
			Start: match.Start + CountBefore(removed, match.Start),
			End:   match.End + CountBefore(removed, match.End),
		}
	}
	return MatchViewResult{
		Matches: matches,
		Replace: func(match EditSpan) string {
			region := source[match.Start:match.End]
			return WithRegionEndings(region, replacement, LineEnding(region, fileEnding))
		},
	}
}

// ComputeEdit is the pure upstream edit computation (no I/O, no fuzzy, no
// env). It returns every matched source span plus the per-span replacements
// and the computed updated text.
// port of src/tools/edit-match.ts:computeEdit
func ComputeEdit(source, oldString, newString string, replaceAll bool) EditResult {
	if oldString == newString {
		return EditResult{
			Error: "No changes to apply: oldString and newString are identical.",
		}
	}
	if oldString == "" {
		return EditResult{
			Error: "oldString must not be empty. Use write to create or overwrite a file.",
		}
	}
	// Strings that differ only in \r ask to change line endings themselves, so
	// they apply to the exact bytes. Every other edit matches the LF view Read
	// shows the model and keeps the file's endings.
	endingsOnly := IsEndingsOnlyChange(oldString, newString)
	var matches []EditSpan
	var replace func(EditSpan) string
	if endingsOnly {
		matches = FindOccurrences(source, oldString)
		replace = func(EditSpan) string { return newString }
	} else {
		view := MatchView(source, oldString, newString)
		matches = view.Matches
		replace = view.Replace
	}
	replacements := len(matches)
	if replacements == 0 {
		return EditResult{
			Error:       "Could not find oldString in the file. It must match exactly, including whitespace and indentation.",
			EndingsOnly: endingsOnly,
		}
	}
	if replacements > 1 && !replaceAll {
		return EditResult{
			Error:        "Found " + strconv.Itoa(replacements) + " matches for oldString, but expected exactly one. Add more surrounding context to make oldString unique, or set replaceAll to true to replace every occurrence.",
			NonUnique:    true,
			Matches:      matches,
			Replacements: replacements,
			EndingsOnly:  endingsOnly,
		}
	}
	targets := matches
	if !replaceAll {
		targets = matches[:1]
	}
	applied := make([]EditApplied, len(targets))
	for i, match := range targets {
		applied[i] = EditApplied{EditSpan: match, Replacement: replace(match)}
	}
	updated := source
	for i := len(applied) - 1; i >= 0; i-- {
		item := applied[i]
		updated = updated[:item.Start] + item.Replacement + updated[item.End:]
	}
	if updated == source {
		if endingsOnly {
			return EditResult{
				Error:        "No changes to apply: the file already has those line endings.",
				Matches:      matches,
				Replacements: replacements,
				EndingsOnly:  endingsOnly,
			}
		}
		return EditResult{
			Error:        "No changes to apply: the edit leaves the file unchanged. Line endings follow the file; to change them, write \\r explicitly in oldString or newString.",
			Matches:      matches,
			Replacements: replacements,
			EndingsOnly:  endingsOnly,
		}
	}
	return EditResult{
		OK:           true,
		Matches:      matches,
		Applied:      applied,
		Replacements: replacements,
		Updated:      updated,
		EndingsOnly:  endingsOnly,
	}
}
