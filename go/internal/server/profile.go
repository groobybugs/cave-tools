// Package server is the MCP entry layer: tool list, dispatch, profile and
// (later wave) the stdio server.
package server

import (
	"os"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// standardHidden holds the rarely used tools the standard profile hides.
// port of src/profile.ts:STANDARD_HIDDEN
var standardHidden = map[string]struct{}{
	"cave__configure": {},
	"cave__compress":  {},
	"cave__status":    {},
}

// FilterByProfile hides the standard-profile tools unless profile is "full".
// nameOf stands in for TS's `T extends { name: string }` constraint, which Go
// cannot express without a method on every tool type.
//
// port of src/profile.ts:filterByProfile
func FilterByProfile[T any](tools []T, nameOf func(T) string, profile ...string) []T {
	resolved := os.Getenv("CAVE_TOOLS_PROFILE")
	if len(profile) > 0 {
		resolved = profile[0]
	}
	if strings.ToLower(jsre.Trim(resolved)) == "full" {
		return tools
	}
	filtered := make([]T, 0, len(tools))
	for _, tool := range tools {
		if _, hidden := standardHidden[nameOf(tool)]; !hidden {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
