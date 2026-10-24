package bashstart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain serves as the durable job worker when this test binary is
// re-executed with JobWorkerCommand, and isolates storage in a temp dir.
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == rt.JobWorkerCommand {
		os.Exit(rt.RunJobWorker(os.Args[2:]))
	}
	dir, err := os.MkdirTemp("", "cave-tools-go-bashstart-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	_ = os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func run(t *testing.T, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := Tool.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

func text(t *testing.T, result types.ToolResult) string {
	t.Helper()
	content, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", result.Content[0])
	}
	return content.Text
}

// TestToolMetadata pins the declaration to src/tools/bash-start.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__bash_start" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if Tool.Annotations != nil {
		t.Fatalf("annotations = %s, want none", Tool.Annotations)
	}
	if !strings.Contains(string(Tool.InputSchema), `"required":["command","description"]`) {
		t.Fatalf("input schema = %s", Tool.InputSchema)
	}
}

var jobIDRE = regexp.MustCompile(`jobId: (\S+)`)
var logPathRE = regexp.MustCompile(`logPath: (\S+)`)

// TestStartJob ports test.mjs:background jobs case 1.
func TestStartJob(t *testing.T) {
	result := run(t, map[string]any{
		"command":     "echo hello-bg",
		"description": "fast echo job",
		"__sessionId": "test-jobs",
	})
	got := text(t, result)
	if result.IsError {
		t.Fatalf("isError = true: %s", got)
	}
	if !strings.HasPrefix(got, "Background job started: fast echo job\n") {
		t.Fatalf("text = %q", got)
	}
	jobID := jobIDRE.FindStringSubmatch(got)
	if jobID == nil {
		t.Fatalf("no jobId: %q", got)
	}
	logPath := logPathRE.FindStringSubmatch(got)
	if logPath == nil {
		t.Fatalf("no logPath: %q", got)
	}
	if !strings.Contains(got, `Poll with cave__bash_status { "jobId": "`+jobID[1]+`", "wait": 60 } — wait returns on exit.`) {
		t.Fatalf("poll instructions = %q", got)
	}

	record, err := rt.WaitForJob(jobID[1], 30_000)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.State != "exited" || record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("record = %#v", record)
	}
	log, err := os.ReadFile(logPath[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "hello-bg") {
		t.Fatalf("log = %q", log)
	}
}

// TestDescriptionFallsBackToRedactedCommand pins `description || commandText`.
func TestDescriptionFallsBackToRedactedCommand(t *testing.T) {
	result := run(t, map[string]any{
		"command":     "echo 'Authorization: Bearer sk-live-abcdefghijklmnop'",
		"description": "",
	})
	got := text(t, result)
	if !strings.HasPrefix(got, "Background job started: echo 'Authorization: Bearer [REDACTED:Bearer token]'\n") {
		t.Fatalf("text = %q", got)
	}
	if strings.Contains(got, "sk-live-abcdefghijklmnop") {
		t.Fatalf("secret leaked: %q", got)
	}
}

// TestMissingWorkdir pins the path error result.
func TestMissingWorkdir(t *testing.T) {
	result := run(t, map[string]any{
		"command":     "echo hi",
		"description": "d",
		"workdir":     "/definitely/not/here",
	})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	if got := text(t, result); got != "Path not found or not a directory: /definitely/not/here" {
		t.Fatalf("text = %q", got)
	}
}

// TestUnknownSessionListsSeparately covers the session filter used by status.
func TestUnknownSessionListsSeparately(t *testing.T) {
	jobs, err := rt.ListSessionJobs("no-such-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, want empty", jobs)
	}
}
