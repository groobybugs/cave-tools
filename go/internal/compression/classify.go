package compression

import (
	"regexp"
	"strings"
)

// OutputPolicy says how a command's output may be processed.
// port of src/compression/classify.ts:OutputPolicy
type OutputPolicy string

const (
	// PolicyPassthrough leaves interactive or streaming output untouched.
	PolicyPassthrough OutputPolicy = "passthrough"
	// PolicyVerbatim keeps output text but only trims it by budget.
	PolicyVerbatim OutputPolicy = "verbatim"
	// PolicyCompressible may be compressed aggressively.
	PolicyCompressible OutputPolicy = "compressible"
)

// passthroughPatterns is PASSTHROUGH_PATTERNS in source order.
// port of src/compression/classify.ts:PASSTHROUGH_PATTERNS
var passthroughPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^auth\s+login\b`),
	regexp.MustCompile(`(?i)^auth\s+logout\b`),
	regexp.MustCompile(`(?i)^auth\s+status\b`),
	regexp.MustCompile(`(?i)^gh\s+auth\b`),
	regexp.MustCompile(`(?i)^npm\s+login\b`),
	regexp.MustCompile(`(?i)^pnpm\s+login\b`),
	regexp.MustCompile(`(?i)^yarn\s+login\b`),
	regexp.MustCompile(`(?i)^ssh\s`),
	regexp.MustCompile(`(?i)^scp\s`),
	regexp.MustCompile(`(?i)^rsync\s`),
	regexp.MustCompile(`(?i)^docker\s+exec\b`),
	regexp.MustCompile(`(?i)^kubectl\s+exec\b`),
	regexp.MustCompile(`(?i)\bvim\s`),
	regexp.MustCompile(`(?i)\bnano\s`),
	regexp.MustCompile(`(?i)\bless\s`),
	regexp.MustCompile(`(?i)\bmore\s`),
	regexp.MustCompile(`(?i)\bman\s`),
	regexp.MustCompile(`(?i)^git\s+commit\b`),
	regexp.MustCompile(`(?i)^npm\s+run\s+dev\b`),
	regexp.MustCompile(`(?i)^pnpm\s+dev\b`),
	regexp.MustCompile(`(?i)^pnpm\s+run\s+dev\b`),
	regexp.MustCompile(`(?i)^yarn\s+dev\b`),
	regexp.MustCompile(`(?i)^cargo\s+watch\b`),
	regexp.MustCompile(`(?i)^next\s+dev\b`),
	regexp.MustCompile(`(?i)^vite\b`),
	regexp.MustCompile(`(?i)^vitepress\b`),
	regexp.MustCompile(`(?i)^astro\s+dev\b`),
	regexp.MustCompile(`(?i)^nuxt\s+dev\b`),
	regexp.MustCompile(`(?i)^webpack\s+serve\b`),
}

// verbatimPatterns is VERBATIM_PATTERNS in source order.
// port of src/compression/classify.ts:VERBATIM_PATTERNS
var verbatimPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^curl\s`),
	regexp.MustCompile(`(?i)^wget\s`),
	regexp.MustCompile(`(?i)^gh\s+api\b`),
	regexp.MustCompile(`(?i)^glab\s+api\b`),
	regexp.MustCompile(`(?i)^jq\s`),
	regexp.MustCompile(`(?i)^yq\s`),
	regexp.MustCompile(`(?i)\bcat\s`),
	regexp.MustCompile(`(?i)\bhead\s`),
	regexp.MustCompile(`(?i)\btail\s`),
	regexp.MustCompile(`(?i)^docker\s+inspect\b`),
	regexp.MustCompile(`(?i)^kubectl\s+get\b`),
	regexp.MustCompile(`(?i)^terraform\s+show\b`),
	regexp.MustCompile(`(?i)^aws\s+.*\b--output\s+json\b`),
}

// classifyCommand picks the output policy for a shell command.
// port of src/compression/classify.ts:classifyCommand
func ClassifyCommand(command string) OutputPolicy {
	trimmed := strings.TrimSpace(command)

	for _, pattern := range passthroughPatterns {
		if pattern.MatchString(trimmed) {
			return PolicyPassthrough
		}
	}

	for _, pattern := range verbatimPatterns {
		if pattern.MatchString(trimmed) {
			return PolicyVerbatim
		}
	}

	return PolicyCompressible
}
