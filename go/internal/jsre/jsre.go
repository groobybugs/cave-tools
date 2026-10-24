// Package jsre translates the JavaScript regular expressions used by the
// TypeScript implementation into RE2 patterns with identical matching, and
// provides JavaScript's whitespace semantics (`\s`, `String.prototype.trim`,
// `String.prototype.split(/\s+/)`). ToLowerJS mirrors
// `String.prototype.toLowerCase`, including SpecialCasing and Final_Sigma.
//
// Go's RE2 differs from ECMAScript in three ways that matter for the port:
//
//   - `\s`/`\S` are ASCII-only in RE2, while JavaScript's `\s` is the Unicode
//     WhiteSpace + LineTerminator set (including U+00A0, U+FEFF, U+2000-U+200A,
//     U+2028/U+2029, U+202F, U+205F, U+3000, U+1680 and \v).
//   - `.` excludes only `\n` in RE2 but excludes `\n`, `\r`, U+2028 and U+2029
//     in JavaScript (unless the `s` flag is set).
//   - `String.prototype.trim` strips that same Unicode whitespace set, while
//     `strings.TrimSpace` follows unicode.IsSpace (it adds U+0085 and misses
//     U+FEFF).
//
// Every regex ported from TS must be compiled through Compile or MustCompile
// with the original TS source, so the two implementations can be diffed by eye.
// See the "Regex and whitespace rule" section of docs/go-port.md.
//
// Known limitation: the `i` flag maps to RE2's Unicode case folding, which is
// slightly wider than JavaScript's non-`u` Canonicalize (ſ folds to s, Kelvin
// sign to k). TS only applies `i` to ASCII command words, so this cannot be
// observed for the patterns in src/compression.
package jsre

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// jsWhitespaceClass is the RE2 character-class body matching exactly the
// ECMAScript WhiteSpace + LineTerminator set that `\s` matches.
const jsWhitespaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsNonWhitespaceClass is the explicit complement of jsWhitespaceClass, used
// for `\S` inside character classes where a nested negation is not expressible.
const jsNonWhitespaceClass = `\x{0}-\x{8}\x{e}-\x{1f}\x{21}-\x{9f}\x{a1}-\x{167f}\x{1681}-\x{1fff}\x{200b}-\x{2027}\x{202a}-\x{202e}\x{2030}-\x{205e}\x{2060}-\x{2fff}\x{3001}-\x{fefe}\x{ff00}-\x{10ffff}`

// jsLineTerminators is the set JavaScript's `.` refuses to match without the
// `s` flag.
const jsLineTerminators = `\n\r\x{2028}\x{2029}`

// jsAnyClass matches every character, used for `[^]`.
const jsAnyClass = `[\x{0}-\x{10ffff}]`

// jsNeverClass matches no character, used for `[]`.
const jsNeverClass = `[^\x{0}-\x{10ffff}]`

// IsSpace reports whether r is JavaScript WhiteSpace or LineTerminator, the
// set removed by String.prototype.trim and matched by `\s`.
func IsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// Trim mirrors String.prototype.trim.
func Trim(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

// TrimStart mirrors String.prototype.trimStart.
func TrimStart(s string) string {
	return strings.TrimLeftFunc(s, IsSpace)
}

// TrimEnd mirrors String.prototype.trimEnd.
func TrimEnd(s string) string {
	return strings.TrimRightFunc(s, IsSpace)
}

// SplitWS mirrors JavaScript's s.split(/\s+/): every run of whitespace is one
// separator, the result always has at least one element, and an empty string
// at either end is kept (" a".split(/\s+/) is ["", "a"]).
func SplitWS(s string) []string {
	parts := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !IsSpace(r) {
			i += size
			continue
		}
		parts = append(parts, s[start:i])
		i += size
		for i < len(s) {
			next, nextSize := utf8.DecodeRuneInString(s[i:])
			if !IsSpace(next) {
				break
			}
			i += nextSize
		}
		start = i
	}
	return append(parts, s[start:])
}

// Compile translates a JavaScript regex source and flags to a Go regexp.
// Supported flags are g (no-op), i, m, s and u (no-op); anything else is
// rejected rather than silently ignored.
func Compile(jsSource string, flags string) (*regexp.Regexp, error) {
	translated, err := Translate(jsSource, flags)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(translated)
	if err != nil {
		return nil, fmt.Errorf("jsre: %q: %w", jsSource, err)
	}
	return re, nil
}

// MustCompile is Compile for pattern tables that must not fail at startup.
func MustCompile(jsSource string, flags string) *regexp.Regexp {
	re, err := Compile(jsSource, flags)
	if err != nil {
		panic(err)
	}
	return re
}

// Translate rewrites a JavaScript regex source into an equivalent RE2 pattern.
func Translate(jsSource string, flags string) (string, error) {
	prefix, stickyUnicode, err := flagPrefix(jsSource, flags)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	out.WriteString(prefix)

	inClass := false
	dotAll := strings.ContainsRune(flags, 's')

	for i := 0; i < len(jsSource); i++ {
		c := jsSource[i]

		if c == '\\' {
			i++
			if i >= len(jsSource) {
				return "", fmt.Errorf("jsre: trailing backslash in %q", jsSource)
			}
			if err := translateEscape(&out, jsSource, &i, inClass, stickyUnicode); err != nil {
				return "", err
			}
			continue
		}

		if inClass {
			if c == ']' {
				inClass = false
				out.WriteByte(']')
				continue
			}
			out.WriteByte(c)
			continue
		}

		switch c {
		case '[':
			switch {
			case strings.HasPrefix(jsSource[i:], "[]"):
				// JavaScript's [] matches nothing.
				out.WriteString(jsNeverClass)
				i++
			case strings.HasPrefix(jsSource[i:], "[^]"):
				// JavaScript's [^] matches any character, newlines included.
				out.WriteString(jsAnyClass)
				i += 2
			default:
				inClass = true
				out.WriteByte('[')
			}
		case '.':
			if dotAll {
				out.WriteString("(?s:.)")
			} else {
				out.WriteString(`[^` + jsLineTerminators + `]`)
			}
		case '(':
			if err := checkGroup(jsSource[i:]); err != nil {
				return "", err
			}
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}

	if inClass {
		return "", fmt.Errorf("jsre: unterminated character class in %q", jsSource)
	}
	return out.String(), nil
}

// flagPrefix validates the JS flags and returns the equivalent RE2 inline
// flags, plus whether the u flag is set.
func flagPrefix(jsSource string, flags string) (string, bool, error) {
	var prefix strings.Builder
	unicodeMode := false
	for _, flag := range flags {
		switch flag {
		case 'g':
			// Global matching is expressed by Go's FindAll*/ReplaceAll
			// helpers, so the flag itself is a no-op.
		case 'i', 'm', 's':
			prefix.WriteString("(?")
			prefix.WriteRune(flag)
			prefix.WriteString(")")
		case 'u':
			unicodeMode = true
		default:
			return "", false, fmt.Errorf("jsre: unsupported flag %q in %q", string(flag), jsSource)
		}
	}
	return prefix.String(), unicodeMode, nil
}

// checkGroup rejects group constructs RE2 cannot express.
func checkGroup(rest string) error {
	switch {
	case strings.HasPrefix(rest, "(?<="), strings.HasPrefix(rest, "(?<!"):
		return fmt.Errorf("jsre: lookbehind is not supported: %q", rest)
	case strings.HasPrefix(rest, "(?="), strings.HasPrefix(rest, "(?!"):
		return fmt.Errorf("jsre: lookahead is not supported: %q", rest)
	case strings.HasPrefix(rest, "(?<"):
		return fmt.Errorf("jsre: named groups are not supported: %q", rest)
	}
	return nil
}

// translateEscape writes the RE2 form of one backslash escape and advances i
// past the escape's last character.
func translateEscape(out *strings.Builder, jsSource string, i *int, inClass bool, unicodeMode bool) error {
	c := jsSource[*i]
	switch c {
	case 's', 'S':
		set := jsWhitespaceClass
		if c == 'S' {
			set = jsNonWhitespaceClass
		}
		if inClass {
			out.WriteString(set)
		} else {
			out.WriteByte('[')
			out.WriteString(set)
			out.WriteByte(']')
		}
	case 'b':
		// Inside a class JavaScript's \b is a backspace, outside it is a word
		// boundary.
		if inClass {
			out.WriteString(`\x{8}`)
		} else {
			out.WriteString(`\b`)
		}
	case 'B':
		if inClass {
			return fmt.Errorf("jsre: \\B inside a character class: %q", jsSource)
		}
		out.WriteString(`\B`)
	case 'd', 'D', 'w', 'W', 'n', 'r', 't', 'f', 'v':
		out.WriteByte('\\')
		out.WriteByte(c)
	case '0':
		if *i+1 < len(jsSource) && isDigit(jsSource[*i+1]) {
			return fmt.Errorf("jsre: octal escapes are not supported: %q", jsSource)
		}
		out.WriteString(`\x{0}`)
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return fmt.Errorf("jsre: backreferences are not supported: %q", jsSource)
	case 'k':
		return fmt.Errorf("jsre: named backreferences are not supported: %q", jsSource)
	case 'u':
		if *i+2 < len(jsSource) && jsSource[*i+1] == '{' {
			if !unicodeMode {
				return fmt.Errorf("jsre: \\u{...} needs the u flag: %q", jsSource)
			}
			end := strings.IndexByte(jsSource[*i+2:], '}')
			if end < 0 {
				return fmt.Errorf("jsre: unterminated \\u{...}: %q", jsSource)
			}
			hex := jsSource[*i+2 : *i+2+end]
			if !isHex(hex) {
				return fmt.Errorf("jsre: bad \\u{...}: %q", jsSource)
			}
			out.WriteString(`\x{` + hex + `}`)
			*i += 2 + end
			return nil
		}
		hex, err := fixedHex(jsSource, *i+1, 4)
		if err != nil {
			return err
		}
		out.WriteString(`\x{` + hex + `}`)
		*i += 4
	case 'x':
		hex, err := fixedHex(jsSource, *i+1, 2)
		if err != nil {
			return err
		}
		out.WriteString(`\x{` + hex + `}`)
		*i += 2
	case 'c':
		if *i+1 >= len(jsSource) || !isASCIILetter(jsSource[*i+1]) {
			return fmt.Errorf("jsre: bad control escape: %q", jsSource)
		}
		out.WriteString(fmt.Sprintf(`\x{%x}`, jsSource[*i+1]%32))
		*i++
	case '/':
		out.WriteByte('/')
	default:
		if isDigit(c) || isASCIILetter(c) {
			return fmt.Errorf("jsre: unsupported escape \\%c: %q", c, jsSource)
		}
		out.WriteByte('\\')
		out.WriteByte(c)
	}
	return nil
}

// fixedHex reads exactly n hex digits starting at start.
func fixedHex(jsSource string, start int, n int) (string, error) {
	if start+n > len(jsSource) {
		return "", fmt.Errorf("jsre: truncated escape: %q", jsSource)
	}
	hex := jsSource[start : start+n]
	if !isHex(hex) {
		return "", fmt.Errorf("jsre: bad hex escape: %q", jsSource)
	}
	return hex, nil
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isDigit(c) && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
