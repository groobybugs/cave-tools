package compression

import (
	"regexp"
	"strings"
	"sync/atomic"
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
}

// patterns mirrors PATTERNS in source order.
// port of src/compression/redact.ts:PATTERNS
var patterns = []redactRule{
	{
		pattern: RedactPattern{
			Label: "Bearer token",
			Regex: regexp.MustCompile(`(?i)(bearer\s+)([a-zA-Z0-9\-_.]{8,})`),
		},
		replacement: "${1}[REDACTED:Bearer token]",
	},
	{
		pattern: RedactPattern{
			Label: "Authorization header",
			Regex: regexp.MustCompile(`(?i)(authorization:\s*(?:basic|bearer|token)\s+)([^\s\r\n\[]+)`),
		},
		replacement: "${1}[REDACTED:Authorization header]",
	},
	{
		pattern: RedactPattern{
			Label: "AWS key",
			Regex: regexp.MustCompile(`(AKIA[0-9A-Z]{16})`),
		},
		replacement: "[REDACTED:AWS key]",
	},
	{
		pattern: RedactPattern{
			Label: "API key param",
			Regex: regexp.MustCompile(`(?i)((?:api[_-]?key|apikey|access[_-]?key|secret[_-]?key|token|password|passwd|pwd|secret)\s*[=:]\s*)([^\s\r\n,;&"'\[]+)`),
		},
		replacement: "${1}[REDACTED:API key param]",
	},
	{
		pattern: RedactPattern{
			Label: "Private key block",
			Regex: regexp.MustCompile(`(-----BEGIN\s+(?:RSA\s+)?PRIVATE\s+KEY-----)[\s\S]*?(-----END\s+(?:RSA\s+)?PRIVATE\s+KEY-----)`),
		},
		replacement: "${1}[REDACTED:Private key block]",
	},
	{
		pattern: RedactPattern{
			Label: "GitHub token",
			Regex: regexp.MustCompile(`(gh[pousr]_[a-zA-Z0-9]{20,})`),
		},
		replacement: "[REDACTED:GitHub token]",
	},
	{
		pattern: RedactPattern{
			Label: "GitLab PAT",
			Regex: regexp.MustCompile(`(glpat-[A-Za-z0-9_\-]{20,})`),
		},
		replacement: "[REDACTED:GitLab PAT]",
	},
	{
		pattern: RedactPattern{
			Label: "OpenAI key",
			Regex: regexp.MustCompile(`(sk-[A-Za-z0-9]{20,})`),
		},
		replacement: "[REDACTED:OpenAI key]",
	},
	{
		pattern: RedactPattern{
			Label: "Anthropic key",
			Regex: regexp.MustCompile(`(sk-ant-[A-Za-z0-9_\-]{20,})`),
		},
		replacement: "[REDACTED:Anthropic key]",
	},
	{
		pattern: RedactPattern{
			Label: "npm token",
			Regex: regexp.MustCompile(`(npm_[A-Za-z0-9]{10,})`),
		},
		replacement: "[REDACTED:npm token]",
	},
	{
		pattern: RedactPattern{
			Label: "JWT",
			Regex: regexp.MustCompile(`(eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]*)`),
		},
		replacement: "[REDACTED:JWT]",
	},
	{
		pattern: RedactPattern{
			Label: "Database URL",
			Regex: regexp.MustCompile(`((?:postgres|mysql|mongodb|redis)://[^\s]+:)([^\s]+@)`),
		},
		replacement: "${1}[REDACTED:Database URL]",
	},
	{
		pattern: RedactPattern{
			Label: "Generic long secret",
			Regex: regexp.MustCompile(`(?i)((?:key|token|secret|password|credential|auth)\s*[=:]\s*['"]?)([a-zA-Z0-9+/=\-_]{32,})(['"]?)`),
		},
		replacement: "${1}[REDACTED:Generic long secret]${3}",
	},
}

// redactionCount mirrors the TS module-level counter (incremented, never read).
var redactionCount atomic.Int64

// redactSecrets replaces every known secret shape with a labelled marker.
// port of src/compression/redact.ts:redactSecrets
func RedactSecrets(input string) string {
	result := input

	for _, rule := range patterns {
		regex := rule.pattern.Regex
		matches := regex.FindAllStringSubmatchIndex(result, -1)
		if len(matches) == 0 {
			continue
		}
		var replaced strings.Builder
		last := 0
		for _, match := range matches {
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
