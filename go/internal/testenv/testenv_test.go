package testenv

import (
	"os"
	"slices"
	"testing"
)

func TestSubagentLike(t *testing.T) {
	liked := []string{
		"CAVE_TOOLS_FORCE_FRESH",
		"CLINE_TASK_ID",
		"CLAUDE_CODE_SUBAGENT",
		"ANTHROPIC_SUBAGENT",
		"CURSOR_TASK_ID",
		"TASK_ID",
		"FOO_TASK_ID",
		"CLAUDE_CODE_SESSION_ID",
		"CLAUDE_CODE_ENTRYPOINT",
		"MY_SUBAGENT_HINT",
	}
	for _, key := range liked {
		if !SubagentLike(key) {
			t.Errorf("SubagentLike(%q) = false, want true", key)
		}
	}
	plain := []string{
		"PATH",
		"CAVE_TOOLS_DATA_DIR",
		"CLAUDE_CONFIG_DIR",
		"SUBTASK_ID",
		"TASK_IDENTITY",
		"XDG_RUNTIME_DIR",
	}
	for _, key := range plain {
		if SubagentLike(key) {
			t.Errorf("SubagentLike(%q) = true, want false", key)
		}
	}
}

func TestScrub(t *testing.T) {
	got := Scrub([]string{
		"PATH=/usr/bin",
		"CLAUDE_CODE_SESSION_ID=abc",
		"FOO_TASK_ID=1",
		"CAVE_TOOLS_FORCE_FRESH=1",
		"CAVE_TOOLS_DATA_DIR=/tmp/data",
		"NO_EQUALS_SIGN",
	})
	want := []string{
		"PATH=/usr/bin",
		"CAVE_TOOLS_DATA_DIR=/tmp/data",
		"NO_EQUALS_SIGN",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Scrub = %v, want %v", got, want)
	}
}

func TestScrubProcess(t *testing.T) {
	t.Setenv("CLAUDE_CODE_TEST_KEY", "1")
	t.Setenv("PATH", os.Getenv("PATH"))
	ScrubProcess()
	if _, ok := os.LookupEnv("CLAUDE_CODE_TEST_KEY"); ok {
		t.Error("ScrubProcess kept CLAUDE_CODE_TEST_KEY")
	}
	if _, ok := os.LookupEnv("PATH"); !ok {
		t.Error("ScrubProcess dropped PATH")
	}
}

func TestScrubMap(t *testing.T) {
	env := map[string]string{
		"PATH":                   "/usr/bin",
		"CLAUDE_CODE_ENTRYPOINT": "cli",
		"ANTHROPIC_SUBAGENT":     "1",
	}
	got := ScrubMap(env)
	if _, ok := got["PATH"]; !ok {
		t.Error("ScrubMap dropped PATH")
	}
	if len(got) != 1 {
		t.Fatalf("ScrubMap left %v, want only PATH", got)
	}
	var nilEnv map[string]string
	if ScrubMap(nilEnv) != nil {
		t.Error("ScrubMap(nil) = non-nil")
	}
}
