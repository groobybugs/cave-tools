package compression

import (
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// RedactPattern is one labelled secret regex.
// port of src/compression/redact.ts:RedactPattern
type RedactPattern struct {
	Label string
	Regex *regexp.Regexp
}

// redactRule couples the public pattern with its replacement template.
// TS derives the template from the capture-group count in the replace
// callback: one group replaces the whole match, two groups keep group 1 and
// drop group 2, three groups keep groups 1 and 3.
type redactRule struct {
	pattern     RedactPattern
	replacement string
	// required lists substrings every regex match must begin with. A missing
	// needle means the rule cannot match; a present one can still be a false
	// positive, so the regex still decides the actual match.
	// fold needles are lower-case ASCII and are matched case-insensitively;
	// non-ASCII input always falls back to the regex because Go's (?i) folds
	// runes an ASCII-only scan would miss (for example Kelvin sign and long s).
	required []string
	fold     bool
	// separator additionally requires `\s*[=:]` after the needle, which
	// keeps replacement labels such as "Bearer token" from defeating the
	// rejection filter for the API-key and generic-secret rules.
	separator bool
}

// matchStart returns the earliest offset where the rule's regex could begin.
// ok is false when no required prefix occurs, so the rule cannot match.
func (rule redactRule) matchStart(input string) (start int, ok bool) {
	if len(rule.required) == 0 {
		return 0, true
	}
	earliest := -1
	for _, needle := range rule.required {
		index := -1
		if rule.fold {
			var found int
			var fallback bool
			if rule.separator {
				found, fallback = indexFoldASCIISeparator(input, needle)
			} else {
				found, fallback = indexFoldASCII(input, needle)
			}
			if fallback {
				return 0, true
			}
			index = found
		} else {
			index = strings.Index(input, needle)
		}
		if index >= 0 && (earliest < 0 || index < earliest) {
			earliest = index
		}
	}
	if earliest < 0 {
		return 0, false
	}
	return earliest, true
}

// indexFoldASCII returns the byte offset of needle under ASCII case folding.
// needle must be lower-case ASCII. fallback is true when a non-ASCII byte is
// reached before a match, so the caller must use the full regex instead of
// risking a false negative from Go's Unicode case folding.
func indexFoldASCII(input, needle string) (index int, fallback bool) {
	n := len(needle)
	if n == 0 {
		return 0, false
	}
	for i := 0; i < len(input); i++ {
		c := input[i]
		if c >= utf8.RuneSelf {
			return 0, true
		}
		if i+n > len(input) || lowerASCIIByte(c) != needle[0] {
			continue
		}
		j := 1
		for ; j < n; j++ {
			c = input[i+j]
			if c >= utf8.RuneSelf {
				return 0, true
			}
			if lowerASCIIByte(c) != needle[j] {
				break
			}
		}
		if j == n {
			return i, false
		}
	}
	return -1, false
}

// indexFoldASCIISeparator is indexFoldASCII plus the rule's mandatory
// `\s*[=:]` suffix. It returns the first needle occurrence that is followed by
// JS ASCII whitespace and '=' or ':'; non-ASCII input defers to the regex.
func indexFoldASCIISeparator(input, needle string) (index int, fallback bool) {
	n := len(needle)
	if n == 0 {
		return 0, false
	}
	for i := 0; i < len(input); i++ {
		c := input[i]
		if c >= utf8.RuneSelf {
			return 0, true
		}
		if i+n > len(input) || lowerASCIIByte(c) != needle[0] {
			continue
		}
		j := 1
		for ; j < n; j++ {
			c = input[i+j]
			if c >= utf8.RuneSelf {
				return 0, true
			}
			if lowerASCIIByte(c) != needle[j] {
				break
			}
		}
		if j != n {
			continue
		}
		k := i + n
		for k < len(input) {
			c = input[k]
			if c >= utf8.RuneSelf {
				return 0, true
			}
			if !isJSASCIISpace(c) {
				break
			}
			k++
		}
		if k < len(input) && (input[k] == '=' || input[k] == ':') {
			return i, false
		}
	}
	return -1, false
}

// isJSASCIISpace reports the ASCII subset of JavaScript's \s.
func isJSASCIISpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

// lowerASCIIByte lower-cases an ASCII byte; other bytes are unchanged.
func lowerASCIIByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// patterns mirrors PATTERNS in source order.
// port of src/compression/redact.ts:PATTERNS
var patterns = []redactRule{
	{
		pattern: RedactPattern{
			Label: "Bearer token",
			Regex: jsre.MustCompile(`(bearer\s+)([a-zA-Z0-9\-_.]{8,})`, "gi"),
		},
		replacement: "${1}[REDACTED:Bearer token]",
		required:    []string{"bearer"},
		fold:        true,
	},
	{
		pattern: RedactPattern{
			Label: "Authorization header",
			Regex: jsre.MustCompile(`(authorization:\s*(?:basic|bearer|token)\s+)([^\s\r\n\[]+)`, "gi"),
		},
		replacement: "${1}[REDACTED:Authorization header]",
		required:    []string{"authorization:"},
		fold:        true,
	},
	{
		pattern: RedactPattern{
			Label: "AWS key",
			Regex: jsre.MustCompile(`(AKIA[0-9A-Z]{16})`, "g"),
		},
		replacement: "[REDACTED:AWS key]",
		required:    []string{"AKIA"},
	},
	{
		pattern: RedactPattern{
			Label: "API key param",
			Regex: jsre.MustCompile(`((?:api[_-]?key|apikey|access[_-]?key|secret[_-]?key|token|password|passwd|pwd|secret)\s*[=:]\s*)([^\s\r\n,;&"'\[]+)`, "gi"),
		},
		replacement: "${1}[REDACTED:API key param]",
		required:    []string{"apikey", "api_key", "api-key", "accesskey", "access_key", "access-key", "secretkey", "secret_key", "secret-key", "token", "password", "passwd", "pwd", "secret"},
		fold:        true,
		separator:   true,
	},
	{
		pattern: RedactPattern{
			Label: "Private key block",
			Regex: jsre.MustCompile(`(-----BEGIN\s+(?:RSA\s+)?PRIVATE\s+KEY-----)([\s\S]*?)(-----END\s+(?:RSA\s+)?PRIVATE\s+KEY-----)`, "g"),
		},
		replacement: "${1}[REDACTED:Private key block]${3}",
		required:    []string{"-----BEGIN"},
	},
	{
		pattern: RedactPattern{
			Label: "GitHub token",
			Regex: jsre.MustCompile(`(gh[pousr]_[a-zA-Z0-9]{20,})`, "g"),
		},
		replacement: "[REDACTED:GitHub token]",
		required:    []string{"gh"},
	},
	{
		pattern: RedactPattern{
			Label: "GitLab PAT",
			Regex: jsre.MustCompile(`(glpat-[A-Za-z0-9_\-]{20,})`, "g"),
		},
		replacement: "[REDACTED:GitLab PAT]",
		required:    []string{"glpat-"},
	},
	{
		pattern: RedactPattern{
			Label: "OpenAI key",
			Regex: jsre.MustCompile(`(sk-[A-Za-z0-9]{20,})`, "g"),
		},
		replacement: "[REDACTED:OpenAI key]",
		required:    []string{"sk-"},
	},
	{
		pattern: RedactPattern{
			Label: "Anthropic key",
			Regex: jsre.MustCompile(`(sk-ant-[A-Za-z0-9_\-]{20,})`, "g"),
		},
		replacement: "[REDACTED:Anthropic key]",
		required:    []string{"sk-ant-"},
	},
	{
		pattern: RedactPattern{
			Label: "npm token",
			Regex: jsre.MustCompile(`(npm_[A-Za-z0-9]{10,})`, "g"),
		},
		replacement: "[REDACTED:npm token]",
		required:    []string{"npm_"},
	},
	{
		pattern: RedactPattern{
			Label: "JWT",
			Regex: jsre.MustCompile(`(eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]*)`, "g"),
		},
		replacement: "[REDACTED:JWT]",
		required:    []string{"eyJ"},
	},
	{
		pattern: RedactPattern{
			Label: "Database URL",
			Regex: jsre.MustCompile(`((?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|rediss?):\/\/[^\s]*:)([^\s]+)(@)`, "g"),
		},
		replacement: "${1}[REDACTED:Database URL]${3}",
		required:    []string{"postgres", "mysql", "mongodb", "redis"},
	},
	{
		pattern: RedactPattern{
			Label: "Generic long secret",
			Regex: jsre.MustCompile(`((?:key|token|secret|password|credential|auth)\s*[=:]\s*['"]?)([a-zA-Z0-9+/=\-_]{32,})(['"]?)`, "gi"),
		},
		replacement: "${1}[REDACTED:Generic long secret]${3}",
		required:    []string{"key", "token", "secret", "password", "credential", "auth"},
		fold:        true,
		separator:   true,
	},
}

// redactionCount mirrors the TS module-level counter (incremented, never read).
var redactionCount atomic.Int64

// redactSecrets replaces every known secret shape with a labelled marker.
// port of src/compression/redact.ts:redactSecrets
func RedactSecrets(input string) string {
	result := input

	for _, rule := range patterns {
		start, possible := rule.matchStart(result)
		if !possible {
			continue
		}
		regex := rule.pattern.Regex
		matches := regex.FindAllStringSubmatchIndex(result[start:], -1)
		if len(matches) == 0 {
			continue
		}
		var replaced strings.Builder
		last := 0
		for _, match := range matches {
			for i, index := range match {
				if index >= 0 {
					match[i] = index + start
				}
			}
			redactionCount.Add(1)
			replaced.WriteString(result[last:match[0]])
			replaced.Write(regex.ExpandString(nil, rule.replacement, result, match))
			last = match[1]
		}
		replaced.WriteString(result[last:])
		result = replaced.String()
	}

	return result
}
