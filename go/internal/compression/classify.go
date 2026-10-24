package compression

import (
	"regexp"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
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
	jsre.MustCompile(`^auth\s+login\b`, "i"),
	jsre.MustCompile(`^auth\s+logout\b`, "i"),
	jsre.MustCompile(`^auth\s+status\b`, "i"),
	jsre.MustCompile(`^gh\s+auth\b`, "i"),
	jsre.MustCompile(`^npm\s+login\b`, "i"),
	jsre.MustCompile(`^pnpm\s+login\b`, "i"),
	jsre.MustCompile(`^yarn\s+login\b`, "i"),
	jsre.MustCompile(`^ssh\s`, "i"),
	jsre.MustCompile(`^scp\s`, "i"),
	jsre.MustCompile(`^rsync\s`, "i"),
	jsre.MustCompile(`^docker\s+exec\b`, "i"),
	jsre.MustCompile(`^kubectl\s+exec\b`, "i"),
	jsre.MustCompile(`\bvim\s`, "i"),
	jsre.MustCompile(`\bnano\s`, "i"),
	jsre.MustCompile(`\bless\s`, "i"),
	jsre.MustCompile(`\bmore\s`, "i"),
	jsre.MustCompile(`\bman\s`, "i"),
	jsre.MustCompile(`^git\s+commit\b`, "i"),
	jsre.MustCompile(`^npm\s+run\s+dev\b`, "i"),
	jsre.MustCompile(`^pnpm\s+dev\b`, "i"),
	jsre.MustCompile(`^pnpm\s+run\s+dev\b`, "i"),
	jsre.MustCompile(`^yarn\s+dev\b`, "i"),
	jsre.MustCompile(`^cargo\s+watch\b`, "i"),
	jsre.MustCompile(`^next\s+dev\b`, "i"),
	jsre.MustCompile(`^vite\b`, "i"),
	jsre.MustCompile(`^vitepress\b`, "i"),
	jsre.MustCompile(`^astro\s+dev\b`, "i"),
	jsre.MustCompile(`^nuxt\s+dev\b`, "i"),
	jsre.MustCompile(`^webpack\s+serve\b`, "i"),
}

// verbatimPatterns is VERBATIM_PATTERNS in source order.
// port of src/compression/classify.ts:VERBATIM_PATTERNS
var verbatimPatterns = []*regexp.Regexp{
	jsre.MustCompile(`^curl\s`, "i"),
	jsre.MustCompile(`^wget\s`, "i"),
	jsre.MustCompile(`^gh\s+api\b`, "i"),
	jsre.MustCompile(`^glab\s+api\b`, "i"),
	jsre.MustCompile(`^jq\s`, "i"),
	jsre.MustCompile(`^yq\s`, "i"),
	jsre.MustCompile(`\bcat\s`, "i"),
	jsre.MustCompile(`\bhead\s`, "i"),
	jsre.MustCompile(`\btail\s`, "i"),
	jsre.MustCompile(`^docker\s+inspect\b`, "i"),
	jsre.MustCompile(`^kubectl\s+get\b`, "i"),
	jsre.MustCompile(`^terraform\s+show\b`, "i"),
	jsre.MustCompile(`^aws\s+.*\b--output\s+json\b`, "i"),
}

// classifyCommand picks the output policy for a shell command.
// port of src/compression/classify.ts:classifyCommand
func ClassifyCommand(command string) OutputPolicy {
	trimmed := jsre.Trim(command)

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
