package server

import "testing"

type namedTool struct {
	Name string
}

func nameOf(tool namedTool) string { return tool.Name }

func testTools() []namedTool {
	return []namedTool{
		{Name: "cave__configure"},
		{Name: "cave__compress"},
		{Name: "cave__status"},
		{Name: "cave__read"},
		{Name: "cave__write"},
		{Name: "cave__edit"},
		{Name: "cave__apply_patch"},
		{Name: "cave__bash"},
		{Name: "cave__bash_start"},
		{Name: "cave__bash_status"},
		{Name: "cave__bash_stop"},
		{Name: "cave__grep"},
		{Name: "cave__find"},
		{Name: "cave__ls"},
		{Name: "cave__webfetch"},
		{Name: "cave__websearch"},
		{Name: "cave__invalidate"},
	}
}

func names(tools []namedTool) []string {
	out := make([]string, len(tools))
	for i, tool := range tools {
		out[i] = tool.Name
	}
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Ported from test.mjs "Testing tool profile filtering".
func TestFilterByProfile(t *testing.T) {
	hidden := []string{"cave__configure", "cave__compress", "cave__status"}
	tools := testTools()

	full := names(FilterByProfile(tools, nameOf, "full"))
	if len(full) != 17 {
		t.Fatalf("full lists %d tools, want 17", len(full))
	}
	for _, name := range hidden {
		if !contains(full, name) {
			t.Fatalf("full does not list %s", name)
		}
	}

	for _, profile := range []string{"", "standard", "bogus"} {
		listed := names(FilterByProfile(tools, nameOf, profile))
		if len(listed) != 14 {
			t.Fatalf("profile %q lists %d tools, want 14", profile, len(listed))
		}
		for _, name := range hidden {
			if contains(listed, name) {
				t.Fatalf("profile %q lists hidden %s", profile, name)
			}
		}
	}

	if got := FilterByProfile(tools, nameOf, " FULL "); len(got) != 17 {
		t.Fatalf("profile is not trimmed + case-insensitive: %d tools", len(got))
	}
}

func TestFilterByProfileDefaultsToEnv(t *testing.T) {
	tools := testTools()
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	if got := FilterByProfile(tools, nameOf); len(got) != 17 {
		t.Fatalf("env full lists %d tools, want 17", len(got))
	}
	t.Setenv("CAVE_TOOLS_PROFILE", " Standard ")
	if got := FilterByProfile(tools, nameOf); len(got) != 14 {
		t.Fatalf("env standard lists %d tools, want 14", len(got))
	}
	t.Setenv("CAVE_TOOLS_PROFILE", "")
	if got := FilterByProfile(tools, nameOf); len(got) != 14 {
		t.Fatalf("unset profile lists %d tools, want 14", len(got))
	}
}

func TestFilterByProfileExplicitBeatsEnv(t *testing.T) {
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	if got := FilterByProfile(testTools(), nameOf, "standard"); len(got) != 14 {
		t.Fatalf("explicit standard lists %d tools, want 14", len(got))
	}
}

func TestFilterByProfileReturnsNonNilEmpty(t *testing.T) {
	got := FilterByProfile([]namedTool{}, nameOf, "standard")
	if got == nil {
		t.Fatal("FilterByProfile returned nil for empty input")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}
