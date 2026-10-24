// Package compression is the Go port of src/compression/.
//
// Every file here mirrors one TypeScript file with the same base name; see
// docs/go-port.md for the map. Output must stay byte-compatible with TS.
package compression

import (
	"strings"
	"unicode"
)

// CommentConfig describes how one language marks comments.
// port of src/compression/aggressive.ts:CommentConfig
type CommentConfig struct {
	Line        []string
	BlockStart  string
	BlockEnd    string
	PreserveDoc bool
}

// langConfig mirrors LANG_CONFIG. Keys are lowercase extensions with the dot.
// port of src/compression/aggressive.ts:LANG_CONFIG
var langConfig = map[string]CommentConfig{
	".ts":   {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".tsx":  {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".js":   {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".jsx":  {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".rs":   {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".go":   {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".c":    {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".cpp":  {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".cc":   {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".h":    {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".hpp":  {Line: []string{"//"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: true},
	".py":   {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".sh":   {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".bash": {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".zsh":  {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".yml":  {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".yaml": {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".toml": {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".rb":   {Line: []string{"#"}, BlockStart: "", BlockEnd: "", PreserveDoc: false},
	".html": {Line: nil, BlockStart: "<!--", BlockEnd: "-->", PreserveDoc: false},
	".htm":  {Line: nil, BlockStart: "<!--", BlockEnd: "-->", PreserveDoc: false},
	".xml":  {Line: nil, BlockStart: "<!--", BlockEnd: "-->", PreserveDoc: false},
	".svg":  {Line: nil, BlockStart: "<!--", BlockEnd: "-->", PreserveDoc: false},
	".vue":  {Line: nil, BlockStart: "<!--", BlockEnd: "-->", PreserveDoc: false},
	".sql":  {Line: []string{"--"}, BlockStart: "/*", BlockEnd: "*/", PreserveDoc: false},
}

// isInsideString reports whether position sits inside a single-, double- or
// backtick-quoted string on the line.
// port of src/compression/aggressive.ts:isInsideString
func isInsideString(line string, position int) bool {
	var inString byte
	escaped := false

	for i := 0; i < position; i++ {
		c := line[i]

		if inString != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == inString {
				inString = 0
			}
		} else if c == '"' || c == '\'' || c == '`' {
			inString = c
		}
	}

	return inString != 0
}

// isDocComment reports whether line is a doc comment the language preserves.
// port of src/compression/aggressive.ts:isDocComment
func isDocComment(line string, config CommentConfig) bool {
	if !config.PreserveDoc {
		return false
	}
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "///") ||
		strings.HasPrefix(trimmed, "/**") ||
		strings.HasPrefix(trimmed, "//!")
}

// aggressiveCompress strips comments and blank lines for ext. It returns the
// original content when compression does not make it shorter.
// port of src/compression/aggressive.ts:aggressiveCompress
func AggressiveCompress(content string, ext string) string {
	config, ok := langConfig[strings.ToLower(ext)]
	if !ok {
		return content
	}

	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	inBlockComment := false
	blockNesting := 0

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			if len(result) > 0 && result[len(result)-1] != "" {
				result = append(result, "")
			}
			continue
		}

		// Preserve shebang
		if i == 0 && strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "#!") {
			result = append(result, line)
			continue
		}

		// Block comments
		if inBlockComment {
			if config.BlockEnd != "" && strings.Contains(line, config.BlockEnd) {
				blockNesting--
				if blockNesting <= 0 {
					inBlockComment = false
					blockNesting = 0
				}
			} else if config.BlockStart != "" && strings.Contains(line, config.BlockStart) {
				blockNesting++
			}
			continue
		}

		if config.BlockStart != "" && strings.Contains(line, config.BlockStart) {
			trimmed := strings.TrimSpace(line)
			if !isDocComment(trimmed, config) {
				startIdx := strings.Index(line, config.BlockStart)
				endIdx := strings.Index(line[startIdx+len(config.BlockStart):], config.BlockEnd)
				if endIdx == -1 {
					inBlockComment = true
					blockNesting = 1
				}
				// Keep code before block comment
				before := strings.TrimRightFunc(line[:startIdx], unicode.IsSpace)
				if before != "" {
					result = append(result, before)
				}
				continue
			}
		}

		// Line comments
		codeLine := line
		for _, marker := range config.Line {
			idx := strings.Index(codeLine, marker)
			for idx != -1 {
				if !isInsideString(codeLine, idx) && !isDocComment(codeLine[idx:], config) {
					codeLine = strings.TrimRightFunc(codeLine[:idx], unicode.IsSpace)
					break
				}
				next := strings.Index(codeLine[idx+1:], marker)
				if next == -1 {
					idx = -1
				} else {
					idx += 1 + next
				}
			}
		}

		trimmed := strings.TrimSpace(codeLine)
		if trimmed == "" {
			if len(result) > 0 && result[len(result)-1] != "" {
				result = append(result, "")
			}
			continue
		}

		result = append(result, codeLine)
	}

	// Collapse multiple blank lines to single
	collapsed := make([]string, 0, len(result))
	for _, line := range result {
		if line == "" {
			if len(collapsed) > 0 && collapsed[len(collapsed)-1] != "" {
				collapsed = append(collapsed, line)
			}
		} else {
			collapsed = append(collapsed, line)
		}
	}

	// Trim trailing blank
	for len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
		collapsed = collapsed[:len(collapsed)-1]
	}

	output := strings.Join(collapsed, "\n")
	if len(output) < len(content) {
		return output
	}
	return content
}
