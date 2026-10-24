// Package tools ports the MCP tool implementations in src/tools.
//
// One TS file maps to one Go file with the same base name (kebab-case ->
// snake_case); the shared purity rules live in docs/go-port.md.
package tools

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
)

// EOL is a line terminator: "\r\n" or "\n".
type EOL string

const (
	// EOLCRLF is the "\r\n" line terminator.
	EOLCRLF EOL = "\r\n"
	// EOLLF is the "\n" line terminator.
	EOLLF EOL = "\n"
)

// lineEol is one line's own terminator; "" marks the file-final line.
type lineEol string

// lineSeg is one line of content plus its own terminator.
// port of src/tools/line-range.ts:LineSeg
type lineSeg struct {
	Text string
	EOL  lineEol
}

// DetectEol reports the file's EOL style: CRLF when the content contains at
// least one "\r\n", LF otherwise (not a majority vote; see dominantEol).
// port of src/tools/line-range.ts:detectEol
func DetectEol(content string) EOL {
	if strings.Contains(content, "\r\n") {
		return EOLCRLF
	}
	return EOLLF
}

// ToLfLines normalizes content to LF lines for indexing (same line-count model
// as cave__read).
// port of src/tools/line-range.ts:toLfLines
func ToLfLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

// FromLfLines joins LF lines with the requested EOL.
// port of src/tools/line-range.ts:fromLfLines
func FromLfLines(lines []string, eol EOL) string {
	body := strings.Join(lines, "\n")
	if eol == EOLCRLF {
		return strings.ReplaceAll(body, "\n", "\r\n")
	}
	return body
}

// parseLineSegs splits content into per-line segments keeping each line's
// terminator. Line count is identical to len(ToLfLines(content)): a lone \r
// counts as a line break (normalized to "\n"), matching ToLfLines.
//
// The TS source matches /[^\r\n]*(?:\r\n|\n|\r|$)/g and keeps the terminal
// empty match (it is the sentinel of a trailing-newline file). Go's
// FindAllStringIndex drops an empty match that abuts the previous one, so the
// loop is written by hand; TestParseLineSegs pins it against the TS model.
// port of src/tools/line-range.ts:parseLineSegs
func parseLineSegs(content string) []lineSeg {
	segs := []lineSeg{}
	for i := 0; ; {
		end := i
		for end < len(content) && content[end] != '\r' && content[end] != '\n' {
			end++
		}
		if end >= len(content) {
			if end > i {
				segs = append(segs, lineSeg{Text: content[i:end], EOL: ""})
			}
			// Empty tail match: real only as the terminal sentinel of a file
			// that ends with a line break (or for empty content itself).
			if len(segs) == 0 || segs[len(segs)-1].EOL != "" {
				segs = append(segs, lineSeg{Text: "", EOL: ""})
			}
			break
		}
		if content[end] == '\r' && end+1 < len(content) && content[end+1] == '\n' {
			segs = append(segs, lineSeg{Text: content[i:end], EOL: lineEol(EOLCRLF)})
			i = end + 2
			continue
		}
		segs = append(segs, lineSeg{Text: content[i:end], EOL: lineEol(EOLLF)})
		i = end + 1
	}
	return segs
}

// dominantEol is the majority of present terminators; ties/empty -> "\n".
// port of src/tools/line-range.ts:dominantEol
func dominantEol(segs []lineSeg) EOL {
	crlf := 0
	lf := 0
	for _, s := range segs {
		if s.EOL == lineEol(EOLCRLF) {
			crlf++
		} else if s.EOL == lineEol(EOLLF) {
			lf++
		}
	}
	if crlf > lf {
		return EOLCRLF
	}
	return EOLLF
}

// renderSegs concatenates every segment's text and terminator.
// port of src/tools/line-range.ts:renderSegs
func renderSegs(segs []lineSeg) string {
	var out strings.Builder
	for _, s := range segs {
		out.WriteString(s.Text)
		out.WriteString(string(s.EOL))
	}
	return out.String()
}

// splitReplacementLines splits replacement text into LF lines. A trailing "\n"
// yields a trailing "" entry, i.e. an extra empty line, exactly like TS.
// port of src/tools/line-range.ts:splitReplacementLines
func splitReplacementLines(replacement string) []string {
	adapted := strings.ReplaceAll(replacement, "\r\n", "\n")
	adapted = strings.ReplaceAll(adapted, "\r", "\n")
	if adapted == "" {
		return []string{}
	}
	return strings.Split(adapted, "\n")
}

// LineTag is a short stable tag for one line (trueline-style fingerprint).
// port of src/tools/line-range.ts:lineTag
func LineTag(line string) string {
	sum := sha1.Sum([]byte(line))
	return hex.EncodeToString(sum[:])[:2]
}

// RangeChecksum is the checksum for an inclusive 1-based line range (or an
// empty insert span). Format: 8 hex chars over "start:end\n" + joined lines.
// port of src/tools/line-range.ts:rangeChecksum
func RangeChecksum(lines []string, startLine, endLine int) string {
	isInsert := endLine == startLine-1
	var slice []string
	if !isInsert {
		slice = jsLineSlice(lines, startLine-1, endLine)
	}
	payload := strconv.Itoa(startLine) + ":" + strconv.Itoa(endLine) + "\n" + strings.Join(slice, "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:8]
}

// jsLineSlice mirrors Array.prototype.slice for non-negative bounds; TS clamps
// out-of-range values instead of panicking.
func jsLineSlice(lines []string, begin, end int) []string {
	n := len(lines)
	if begin < 0 {
		begin += n
		if begin < 0 {
			begin = 0
		}
	} else if begin > n {
		begin = n
	}
	if end < 0 {
		end += n
		if end < 0 {
			end = 0
		}
	} else if end > n {
		end = n
	}
	if end < begin {
		end = begin
	}
	return lines[begin:end]
}

// padStart mirrors String.prototype.padStart for a single-character pad.
func padStart(value string, length int, pad string) string {
	if len(value) >= length {
		return value
	}
	return strings.Repeat(pad, length-len(value)) + value
}

// FormatNumberedLine renders `00012:ab| content` when withTags, else
// `00012| content`.
// port of src/tools/line-range.ts:formatNumberedLine
func FormatNumberedLine(lineNo int, line string, withTags ...bool) string {
	tags := true
	if len(withTags) > 0 {
		tags = withTags[0]
	}
	num := padStart(strconv.Itoa(lineNo), 5, "0")
	if !tags {
		return num + "| " + line
	}
	return num + ":" + LineTag(line) + "| " + line
}

// FormatNumberedWindow renders an inclusive 1-based numbered line window.
// port of src/tools/line-range.ts:formatNumberedWindow
func FormatNumberedWindow(lines []string, startLine, endLine int, withTags ...bool) string {
	out := []string{}
	lo := max(1, startLine)
	hi := min(len(lines), endLine)
	for n := lo; n <= hi; n++ {
		line := ""
		if n-1 >= 0 && n-1 < len(lines) {
			line = lines[n-1]
		}
		out = append(out, FormatNumberedLine(n, line, withTags...))
	}
	return strings.Join(out, "\n")
}

// RangeEditResult is the ApplyRangeEdit outcome: the TS success/error union.
type RangeEditResult struct {
	OK            bool
	Content       string
	LineCount     int
	ReplacedChars int
	Error         string
}

// ApplyRangeEdit replaces inclusive 1-based lines [startLine, endLine] with
// replacement. Insert before N: startLine=N, endLine=N-1. Append after last:
// startLine=lineCount+1, endLine=lineCount. Only touched lines change; every
// other line keeps its exact bytes and terminator.
// port of src/tools/line-range.ts:applyRangeEdit
func ApplyRangeEdit(content string, startLine, endLine int, replacement string) RangeEditResult {
	if startLine < 1 {
		return RangeEditResult{Error: fmt.Sprintf("start_line must be >= 1 (got %d)", startLine)}
	}

	segs := parseLineSegs(content)
	lineCount := len(segs)
	isInsert := endLine == startLine-1

	if isInsert {
		if startLine > lineCount+1 {
			return RangeEditResult{Error: fmt.Sprintf(
				"insert start_line %d out of range (file has %d lines; max insert is %d)",
				startLine, lineCount, lineCount+1)}
		}
	} else {
		if endLine < startLine {
			return RangeEditResult{Error: fmt.Sprintf(
				"end_line (%d) must be >= start_line-1 (%d); use end_line=start_line-1 to insert",
				endLine, startLine-1)}
		}
		if startLine > lineCount {
			return RangeEditResult{Error: fmt.Sprintf(
				"start_line %d out of range (file has %d lines)", startLine, lineCount)}
		}
		if endLine > lineCount {
			return RangeEditResult{Error: fmt.Sprintf(
				"end_line %d out of range (file has %d lines)", endLine, lineCount)}
		}
	}

	// Append at lineCount+1 on a trailing-newline file targets the terminal
	// empty sentinel (not content): fold to insert-before-sentinel so the
	// result keeps its final newline instead of gaining a blank line.
	s := startLine
	e := endLine
	if isInsert &&
		s == lineCount+1 &&
		lineCount >= 1 &&
		segs[lineCount-1].Text == "" &&
		segs[lineCount-1].EOL == "" {
		s = lineCount
		e = lineCount - 1
	}
	ins := e == s-1

	newTexts := splitReplacementLines(replacement)

	var region []lineSeg
	if !ins {
		region = segs[s-1 : e]
	}
	regionTexts := make([]string, len(region))
	for i, r := range region {
		regionTexts[i] = r.Text
	}
	replacedChars := utf16Len(strings.Join(regionTexts, "\n"))

	fallback := dominantEol(segs)
	var following *lineSeg
	if ins && s-1 >= 0 && s-1 < len(segs) {
		following = &segs[s-1]
	}
	newSegs := make([]lineSeg, len(newTexts))
	for i, t := range newTexts {
		var le lineEol
		switch {
		case ins:
			if following != nil && following.EOL != "" {
				le = following.EOL
			} else {
				le = lineEol(fallback)
			}
		case i < len(region):
			if region[i].EOL != "" {
				le = region[i].EOL
			} else {
				le = lineEol(fallback)
			}
		default:
			last := region[len(region)-1]
			if last.EOL != "" {
				le = last.EOL
			} else {
				le = lineEol(fallback)
			}
		}
		newSegs[i] = lineSeg{Text: t, EOL: le}
	}

	before := segs[:s-1]
	var after []lineSeg
	if ins {
		after = segs[s-1:]
	} else {
		after = segs[e:]
	}
	next := make([]lineSeg, 0, len(before)+len(newSegs)+len(after))
	next = append(next, before...)
	next = append(next, newSegs...)
	next = append(next, after...)
	// Preserve the file-final newline state when the op consumed the original
	// final segment (append/replace/delete through end of file).
	if len(after) == 0 && len(next) > 0 {
		next[len(next)-1].EOL = segs[len(segs)-1].EOL
	}
	// A former file-final line that is now followed by more lines takes the
	// file fallback EOL (untouched lines already carry their own terminators).
	for i := 0; i+1 < len(next); i++ {
		if next[i].EOL == "" {
			next[i].EOL = lineEol(fallback)
		}
	}
	return RangeEditResult{
		OK:            true,
		Content:       renderSegs(next),
		LineCount:     lineCount,
		ReplacedChars: replacedChars,
	}
}

// MoveLinesResult is the ApplyMoveLines outcome: the TS success/error union.
type MoveLinesResult struct {
	OK         bool
	Content    string
	MovedChars int
	Error      string
}

// ApplyMoveLines moves inclusive lines [startLine, endLine] to insert before
// insertBefore (1-based); insertBefore may equal lineCount+1 to move to end.
// Computed on original line numbers (cut first, then insert). The terminal
// empty sentinel of a trailing-newline file is not movable content.
// port of src/tools/line-range.ts:applyMoveLines
func ApplyMoveLines(content string, startLine, endLine, insertBefore int) MoveLinesResult {
	if startLine < 1 || endLine < startLine {
		return MoveLinesResult{Error: "move requires end_line >= start_line >= 1"}
	}

	segs := parseLineSegs(content)
	lineCount := len(segs)
	blockLen := endLine - startLine + 1

	if endLine > lineCount {
		return MoveLinesResult{Error: fmt.Sprintf(
			"end_line %d out of range (file has %d lines)", endLine, lineCount)}
	}
	if insertBefore < 1 || insertBefore > lineCount+1 {
		return MoveLinesResult{Error: fmt.Sprintf(
			"insert_before %d out of range (valid 1..%d)", insertBefore, lineCount+1)}
	}
	// Inside the block (not start or end+1 no-ops).
	if insertBefore > startLine && insertBefore <= endLine {
		return MoveLinesResult{Error: fmt.Sprintf(
			"insert_before %d lies inside moved block %d-%d", insertBefore, startLine, endLine)}
	}

	// The terminal empty sentinel is the trailing newline, not content:
	// folding it into the block (or landing on it) silently corrupts the
	// final newline, so reject it with a clear error instead.
	hasSentinel := lineCount >= 1 &&
		segs[lineCount-1].Text == "" &&
		segs[lineCount-1].EOL == ""
	if hasSentinel && endLine == lineCount {
		return MoveLinesResult{Error: fmt.Sprintf(
			"cannot move line %d: it is the trailing newline, not content (move lines 1..%d)",
			lineCount, lineCount-1)}
	}

	// Work on real content lines; the sentinel (if any) is re-attached after.
	w := segs
	if hasSentinel {
		w = segs[:lineCount-1]
	}
	block := make([]lineSeg, blockLen)
	copy(block, w[startLine-1:endLine])
	blockTexts := make([]string, len(block))
	for i, b := range block {
		blockTexts[i] = b.Text
	}
	movedChars := utf16Len(strings.Join(blockTexts, "\n"))
	without := make([]lineSeg, 0, len(w)-blockLen)
	without = append(without, w[:startLine-1]...)
	without = append(without, w[endLine:]...)

	// Destination index into `without` (1-based insert-before). insert_before
	// lineCount+1 on a trailing-newline file means end-of-content: the
	// sentinel is not addressable, so clamp to it.
	dest0 := insertBefore
	if hasSentinel {
		dest0 = min(insertBefore, len(w)+1)
	}
	dest := dest0
	if insertBefore > endLine {
		dest = dest0 - blockLen
	}
	// insertBefore <= startLine: dest stays insertBefore.

	fallback := dominantEol(segs)
	nextW := make([]lineSeg, 0, len(without)+blockLen)
	nextW = append(nextW, without[:dest-1]...)
	nextW = append(nextW, block...)
	nextW = append(nextW, without[dest-1:]...)
	next := nextW
	if hasSentinel {
		next = append(nextW, lineSeg{Text: "", EOL: ""})
	}
	for i := 0; i+1 < len(next); i++ {
		if next[i].EOL == "" {
			next[i].EOL = lineEol(fallback)
		}
	}
	if !hasSentinel && len(next) > 0 {
		next[len(next)-1].EOL = ""
	}
	return MoveLinesResult{OK: true, Content: renderSegs(next), MovedChars: movedChars}
}

// VerifyRangeChecksumResult is the VerifyRangeChecksum outcome.
type VerifyRangeChecksumResult struct {
	OK     bool
	Actual string
	Window string
	Error  string
}

// VerifyRangeChecksum checks an expected range checksum, accepting a full
// match or expected as a prefix of actual (the footer may shorten later).
// port of src/tools/line-range.ts:verifyRangeChecksum
func VerifyRangeChecksum(content string, startLine, endLine int, expected string) VerifyRangeChecksumResult {
	want := strings.ToLower(jsre.Trim(expected))
	if utf16Len(want) < 4 {
		return VerifyRangeChecksumResult{
			Error: "expected_range_checksum must be at least 4 hex characters",
		}
	}
	lines := ToLfLines(content)
	actual := RangeChecksum(lines, startLine, endLine)
	if actual == want || strings.HasPrefix(actual, want) {
		return VerifyRangeChecksumResult{OK: true}
	}
	isInsert := endLine == startLine-1
	winStart := startLine
	if isInsert {
		winStart = max(1, startLine-2)
	}
	winEnd := endLine
	if isInsert {
		winEnd = min(len(lines), startLine+1)
	}
	window := FormatNumberedWindow(lines, winStart, winEnd, true)
	limit := max(1, endLine-startLine+1)
	if isInsert {
		limit = 5
	}
	return VerifyRangeChecksumResult{
		Actual: actual,
		Window: window,
		Error: fmt.Sprintf(
			"expected_range_checksum mismatch for lines %d-%d. Re-read with cave__read offset=%d limit=%d line_numbers=true.",
			startLine, endLine, startLine, limit),
	}
}

// ActiveEditMode is the active cave-tools intensity from env
// (off|hint|enforce|strict). Default enforce.
// port of src/tools/line-range.ts:activeEditMode
func ActiveEditMode(ctx context.Context) string {
	raw := runtime.GetEnv(ctx)["CAVE_TOOLS_MODE"]
	if raw == "" {
		raw = "enforce"
	}
	raw = jsre.Trim(strings.ToLower(raw))
	switch raw {
	case "off", "hint", "enforce", "strict":
		return raw
	}
	return "enforce"
}
