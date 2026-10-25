// ripgrep/fd wrappers and the Go fallback finder.
//
// Port of src/runtime/ripgrep.ts.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

const maxRecordBytes = 64 * 1024
const maxLineLength = 2000

// GrepMatch mirrors src/runtime/ripgrep.ts:GrepMatch.
type GrepMatch struct {
	Path string `json:"path"`
	Line int64  `json:"line"`
	Text string `json:"text"`
}

// LineWindow mirrors one entry of readLineWindow's result. TS numbers are
// float64: a fractional context makes `current` fractional, so the line number
// and the empty text must stay exact.
type LineWindow struct {
	Line  float64 `json:"line"`
	Text  string  `json:"text"`
	Match bool    `json:"match"`
}

// RgGrepResult mirrors rgGrep's { matches, truncated } result.
type RgGrepResult struct {
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

// RgGrepInput mirrors rgGrep's input object.
type RgGrepInput struct {
	Cwd        string  `json:"cwd"`
	Pattern    string  `json:"pattern"`
	File       string  `json:"file,omitempty"`
	Include    string  `json:"include,omitempty"`
	Limit      float64 `json:"limit"`
	IgnoreCase bool    `json:"ignoreCase,omitempty"`
	Literal    bool    `json:"literal,omitempty"`
}

// port of src/runtime/ripgrep.ts:isInvalidPattern
func isInvalidPattern(stderr string) bool {
	return strings.Contains(stderr, "regex parse error") || strings.Contains(stderr, "error parsing regex")
}

// port of src/runtime/ripgrep.ts:globToRegExp
func globToRegExp(pattern string) *regexp.Regexp {
	var source strings.Builder
	const escapable = ".+^${}()|[]\\"
	for i := 0; i < len(pattern); i++ {
		char := pattern[i]
		if char == '*' && i+1 < len(pattern) && pattern[i+1] == '*' {
			source.WriteString(".*")
			i++
		} else if char == '*' {
			source.WriteString("[^/]*")
		} else if char == '?' {
			source.WriteString("[^/]")
		} else if strings.IndexByte(escapable, char) >= 0 {
			source.WriteByte('\\')
			source.WriteByte(char)
		} else {
			source.WriteByte(char)
		}
	}
	return jsre.MustCompile("^"+source.String()+"$", "")
}

type spawnBinResult struct {
	Stdout string
	Stderr string
	Code   int // -1 when the process could not be started (TS null)
	Err    error
}

// port of src/runtime/ripgrep.ts:spawnBin
func spawnBin(ctx context.Context, command string, args []string, cwd string) spawnBinResult {
	cmd := exec.Command(command, args...)
	cmd.Dir = cwd
	cmd.Env = envSlice(GetEnv(ctx))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// The TS helper spawns with windowsHide: true and without detached.
	configureSpawn(cmd, false)
	err := cmd.Run()
	if cmd.ProcessState == nil {
		return spawnBinResult{Code: -1, Err: err}
	}
	return spawnBinResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Code:   cmd.ProcessState.ExitCode(),
	}
}

// port of src/runtime/ripgrep.ts:parsePathLines
func parsePathLines(stdout string, limit float64) []string {
	lines := strings.Split(stdout, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := jsre.Trim(line)
		for strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, ".\\") {
			trimmed = trimmed[2:]
		}
		trimmed = strings.TrimLeft(trimmed, "/\\")
		trimmed = strings.ReplaceAll(trimmed, "\\", "/")
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	// TS ends with .slice(0, limit): ToIntegerOrInfinity truncates, NaN is 0,
	// a negative end counts from the tail and Infinity keeps everything.
	return result[:jsSliceEnd(limit, len(result))]
}

// jsSliceEnd mirrors ToIntegerOrInfinity for Array.prototype.slice(0, limit)
// with a non-negative start.
func jsSliceEnd(limit float64, length int) int {
	if math.IsNaN(limit) {
		return 0
	}
	truncated := math.Trunc(limit)
	if truncated >= float64(length) {
		return length
	}
	if truncated < 0 {
		truncated += float64(length)
		if truncated < 0 {
			return 0
		}
	}
	return int(truncated)
}

// port of src/runtime/ripgrep.ts:findWithNode
func findWithNode(cwd, pattern string, limit float64, hidden bool) []string {
	matcherPattern := pattern
	if !strings.Contains(pattern, "/") {
		matcherPattern = "**/" + pattern
	}
	matcher := globToRegExp(matcherPattern)
	var basenameMatcher *regexp.Regexp
	if !strings.Contains(pattern, "/") {
		basenameMatcher = globToRegExp(pattern)
	}
	results := []string{}
	stack := []string{cwd}
	for len(stack) > 0 && float64(len(results)) < limit {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Name() == ".git" {
				continue
			}
			if !hidden && strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			full := filepath.Join(dir, entry.Name())
			relativePath, err := filepath.Rel(cwd, full)
			if err != nil {
				continue
			}
			relative := ToPosixPath(relativePath)
			if entry.IsDir() {
				stack = append(stack, full)
			}
			if !entry.Type().IsRegular() {
				continue
			}
			if matcher.MatchString(relative) ||
				(basenameMatcher != nil && basenameMatcher.MatchString(filepath.Base(relative))) {
				results = append(results, relative)
			}
			if float64(len(results)) >= limit {
				break
			}
		}
	}
	// TS sorts with a.localeCompare(b) (stable). Key-based locale sort keeps the
	// same order without paying for two collations per comparison.
	SortStringsLocale(results)
	return results
}

// port of src/runtime/ripgrep.ts:rgFiles
func RgFiles(ctx context.Context, cwd, pattern string, limit float64, hidden bool) ([]string, error) {
	fd := ResolveFd(ctx, nil)
	if fd != "" {
		args := []string{"--glob", pattern}
		if hidden {
			args = append(args, "--hidden")
		}
		// TS passes String(limit): fd receives "2.5"/"1e+21"/"Infinity" and
		// rejects them, which is what sends the call to the rg fallback.
		args = append(args, "--exclude", ".git", "--max-results", jsNumberString(limit), ".")
		fdResult := spawnBin(ctx, fd, args, cwd)
		if fdResult.Err == nil && (fdResult.Code == 0 || fdResult.Code == 1) {
			return parsePathLines(fdResult.Stdout, limit), nil
		}
	}

	rg := ResolveRg(ctx, nil)
	if rg == "" {
		return findWithNode(cwd, pattern, limit, hidden), nil
	}
	args := []string{"--no-config", "--files"}
	if hidden {
		args = append(args, "--hidden")
	}
	args = append(args, "--glob="+pattern, "--glob=!**/.git/**", ".")
	result := spawnBin(ctx, rg, args, cwd)
	if result.Err != nil {
		return findWithNode(cwd, pattern, limit, hidden), nil
	}
	if result.Code != 0 && result.Code != 1 {
		return nil, fmt.Errorf("%s", firstNonEmpty(jsre.Trim(result.Stderr),
			fmt.Sprintf("ripgrep exited with code %d", result.Code)))
	}
	return parsePathLines(result.Stdout, limit), nil
}

// rgEvent is the subset of ripgrep's --json record this port consumes.
type rgEvent struct {
	Type string `json:"type"`
	Data *struct {
		Path *struct {
			Text *string `json:"text"`
		} `json:"path"`
		Lines *struct {
			Text *string `json:"text"`
		} `json:"lines"`
		LineNumber json.RawMessage `json:"line_number"`
	} `json:"data"`
}

// port of src/runtime/ripgrep.ts:rgGrep
func RgGrep(ctx context.Context, input RgGrepInput) (RgGrepResult, error) {
	args := []string{"--no-config", "--json", "--hidden", "--no-messages"}
	if input.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if input.Literal {
		args = append(args, "--fixed-strings")
	}
	if input.Include != "" {
		args = append(args, "--glob="+input.Include)
	}
	args = append(args, "--glob=!**/.git/**", "--", input.Pattern)
	target := input.File
	if target == "" {
		target = "."
	}
	args = append(args, target)

	rg := ResolveRg(ctx, nil)
	if rg == "" {
		return RgGrepResult{}, fmt.Errorf("ripgrep not available (not on PATH and download failed)")
	}
	result := spawnBin(ctx, rg, args, input.Cwd)
	if result.Err != nil {
		return RgGrepResult{}, fmt.Errorf("Failed to run ripgrep: %s", result.Err.Error())
	}
	if result.Code == 2 && isInvalidPattern(result.Stderr) {
		return RgGrepResult{}, fmt.Errorf("%s", jsre.Trim(result.Stderr))
	}
	if result.Code != 0 && result.Code != 1 && result.Code != 2 {
		return RgGrepResult{}, fmt.Errorf("%s", firstNonEmpty(jsre.Trim(result.Stderr),
			fmt.Sprintf("ripgrep exited with code %d", result.Code)))
	}

	matches := []GrepMatch{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if jsre.Trim(line) == "" {
			continue
		}
		if len(line) > maxRecordBytes {
			return RgGrepResult{}, fmt.Errorf("Ripgrep JSON record exceeded %d bytes", maxRecordBytes)
		}
		var event rgEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return RgGrepResult{}, fmt.Errorf("Invalid ripgrep JSON output")
		}
		if event.Type != "match" {
			continue
		}
		relative := ""
		if event.Data != nil && event.Data.Path != nil && event.Data.Path.Text != nil {
			relative = *event.Data.Path.Text
		}
		for strings.HasPrefix(relative, "./") || strings.HasPrefix(relative, ".\\") {
			relative = relative[2:]
		}
		relative = strings.TrimLeft(relative, "/\\")
		relative = strings.ReplaceAll(relative, "\\", "/")

		text := ""
		if event.Data != nil && event.Data.Lines != nil && event.Data.Lines.Text != nil {
			text = *event.Data.Lines.Text
		}
		if strings.HasSuffix(text, "\r\n") {
			text = text[:len(text)-2]
		} else if strings.HasSuffix(text, "\n") {
			text = text[:len(text)-1]
		}

		// rg omits line_number for some inputs; 0 means "unknown line".
		lineNumber := int64(0)
		if event.Data != nil && len(event.Data.LineNumber) > 0 {
			// Only a JSON number counts (Number.isFinite); strings, null and
			// out-of-range literals become 0.
			var parsed float64
			if err := json.Unmarshal(event.Data.LineNumber, &parsed); err == nil {
				lineNumber = int64(parsed)
			}
		}
		if utf16Length(text) > maxLineLength {
			text = truncateUTF16(text, maxLineLength) + "..."
		}
		matches = append(matches, GrepMatch{Path: relative, Line: lineNumber, Text: text})
		// TS: matches.length > input.limit. NaN never breaks, so later records
		// are still parsed (and rejected when malformed) exactly like TS.
		if float64(len(matches)) > input.Limit {
			break
		}
	}

	return RgGrepResult{
		Matches:   matches[:jsSliceEnd(input.Limit, len(matches))],
		Truncated: float64(len(matches)) > input.Limit || result.Code == 2,
	}, nil
}

// port of src/runtime/ripgrep.ts:readLineWindow
//
// line and lineContext are JS numbers. A fractional context makes `current`
// fractional: TS then reads lines[current - 1] (an undefined array property),
// so the text is empty and the emitted line number keeps its fraction.
func ReadLineWindow(filePath string, line float64, lineContext float64) ([]LineWindow, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	text := decodeUTF8Replace(content)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	start := math.Max(1, line-lineContext)
	end := math.Min(float64(len(lines)), line+lineContext)
	result := []LineWindow{}
	for current := start; current <= end; current++ {
		raw := ""
		if current == math.Trunc(current) && current >= 1 && current <= float64(len(lines)) {
			raw = lines[int(current)-1]
		}
		if utf16Length(raw) > 500 {
			raw = truncateUTF16(raw, 500) + "... (truncated)"
		}
		result = append(result, LineWindow{Line: current, Text: raw, Match: current == line})
	}
	return result, nil
}

// jsNumberString formats a float like ECMAScript Number::toString(10) for the
// values tool arguments carry. strconv.Itoa would print 1e+21 differently.
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
