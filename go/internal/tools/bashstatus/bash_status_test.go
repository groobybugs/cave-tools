package bashstatus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain pins one data dir for the package: storage keeps a process
// singleton connection, so per-test dirs would dangle.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-bashstatus-")
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

// TestToolMetadata pins the declaration to src/tools/bash-status.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__bash_status" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if string(Tool.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("annotations = %s", Tool.Annotations)
	}
}

// insertJob writes a job row and its log file, returning the log path.
func insertJob(t *testing.T, jobID, session string, state storage.JobState, logText string) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), jobID+".log")
	if err := os.WriteFile(logPath, []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1_000_000)
	endedAt := int64(1_004_500) // 4.5s -> Math.round gives 5s
	exitCode := int64(0)
	record := storage.JobRecord{
		JobID:     jobID,
		Pid:       4242,
		SessionID: session,
		Command:   "echo hello-bg",
		StartedAt: startedAt,
		EndedAt:   &endedAt,
		ExitCode:  &exitCode,
		State:     state,
		LogPath:   logPath,
	}
	if err := storage.InsertJob(record); err != nil {
		t.Fatal(err)
	}
	return logPath
}

// TestExitedStatus ports test.mjs background job case 1.
func TestExitedStatus(t *testing.T) {
	logPath := insertJob(t, "j-exited", "s1", storage.JobExited, "# cave-job j-exited\n\nhello-bg\n")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}

	result := run(t, map[string]any{"jobId": "j-exited", "__sessionId": "s1"})
	got := text(t, result)
	if !strings.Contains(got, "j-exited  exited (code 0) after 5s") {
		t.Fatalf("header = %q", got)
	}
	if !strings.Contains(got, "command: echo hello-bg") {
		t.Fatalf("missing command: %q", got)
	}
	if !strings.Contains(got, "log: "+logPath) {
		t.Fatalf("missing log path: %q", got)
	}
	if !strings.Contains(got, "next_cursor: "+strconv.FormatInt(info.Size(), 10)) {
		t.Fatalf("next_cursor = %q", got)
	}
	if !strings.Contains(got, "hello-bg") {
		t.Fatalf("missing log tail: %q", got)
	}
	if result.IsError {
		t.Fatalf("isError = true for exit 0: %q", got)
	}
}

// TestStatusOnly ports test.mjs `tail_lines: 0` (no log bytes read).
func TestStatusOnly(t *testing.T) {
	logPath := insertJob(t, "j-status-only", "s1", storage.JobExited, "hello-bg\n")
	info, _ := os.Stat(logPath)

	got := text(t, run(t, map[string]any{"jobId": "j-status-only", "tail_lines": float64(0)}))
	if !strings.Contains(got, "log_size: "+strconv.FormatInt(info.Size(), 10)+" bytes") {
		t.Fatalf("log_size missing: %q", got)
	}
	if !strings.Contains(got, "next_cursor: "+strconv.FormatInt(info.Size(), 10)) {
		t.Fatalf("next_cursor missing: %q", got)
	}
	if !strings.Contains(got, "tail_lines=0 read no log bytes") {
		t.Fatalf("notice missing: %q", got)
	}
	if strings.Contains(got[strings.Index(got, "\n\n")+2:], "hello-bg") {
		t.Fatalf("log body leaked: %q", got)
	}
}

// TestCursorIncremental ports test.mjs cursor first/second reads.
func TestCursorIncremental(t *testing.T) {
	insertJob(t, "j-cursor", "s1", storage.JobExited, "hello-bg\n")

	first := text(t, run(t, map[string]any{"jobId": "j-cursor", "cursor": float64(0), "tail_lines": float64(100)}))
	match := regexp.MustCompile(`next_cursor: (\d+)`).FindStringSubmatch(first)
	if match == nil {
		t.Fatalf("no next_cursor: %q", first)
	}
	nextCursor, _ := strconv.Atoi(match[1])
	if !strings.Contains(first[strings.Index(first, "\n\n")+2:], "hello-bg") {
		t.Fatalf("first body = %q", first)
	}

	second := text(t, run(t, map[string]any{"jobId": "j-cursor", "cursor": float64(nextCursor), "tail_lines": float64(100)}))
	body := second[strings.Index(second, "\n\n")+2:]
	if !strings.Contains(body, "(no new log output)") || strings.Contains(body, "hello-bg") {
		t.Fatalf("second body = %q", body)
	}
}

// TestTailTruncated covers the "earlier log output omitted" notice.
func TestTailTruncated(t *testing.T) {
	var builder strings.Builder
	for builder.Len() < 70*1024 {
		builder.WriteString(strings.Repeat("a", 99) + "\n")
	}
	insertJob(t, "j-big", "s1", storage.JobExited, builder.String())

	got := text(t, run(t, map[string]any{"jobId": "j-big", "tail_lines": float64(5)}))
	if !strings.Contains(got, "tail_truncated: earlier log output omitted; use cursor=0 for incremental output") {
		t.Fatalf("tail notice missing: %q", got[len(got)-300:])
	}
}

// TestNonNegativeValidation ports the nonnegativeInteger error path.
func TestNonNegativeValidation(t *testing.T) {
	insertJob(t, "j-negative", "s1", storage.JobExited, "x\n")

	negativeTail := run(t, map[string]any{"jobId": "j-negative", "tail_lines": float64(-1)})
	if !negativeTail.IsError || text(t, negativeTail) != "tail_lines and cursor must be non-negative numbers" {
		t.Fatalf("negative tail = %+v", negativeTail)
	}
	negativeCursor := run(t, map[string]any{"jobId": "j-negative", "cursor": float64(-2)})
	if !negativeCursor.IsError {
		t.Fatalf("negative cursor = %+v", negativeCursor)
	}
	stringTail := run(t, map[string]any{"jobId": "j-negative", "tail_lines": "abc"})
	if !stringTail.IsError {
		t.Fatalf("string tail = %+v", stringTail)
	}
}

// TestUnknownJob pins the unknown-job error.
func TestUnknownJob(t *testing.T) {
	result := run(t, map[string]any{"jobId": "j-nope", "__sessionId": "s1"})
	if !result.IsError || text(t, result) != "Unknown job: j-nope" {
		t.Fatalf("result = %+v", result)
	}
}

// TestNonZeroExitIsError ports test.mjs case 7.
func TestNonZeroExitIsError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "j-fail.log")
	if err := os.WriteFile(logPath, []byte("boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1000)
	endedAt := int64(2000)
	exitCode := int64(7)
	if err := storage.InsertJob(storage.JobRecord{
		JobID: "j-fail", Pid: 4242, SessionID: "s1", Command: "exit 7",
		StartedAt: startedAt, EndedAt: &endedAt, ExitCode: &exitCode,
		State: storage.JobExited, LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}
	result := run(t, map[string]any{"jobId": "j-fail", "__sessionId": "s1"})
	if !result.IsError || !strings.Contains(text(t, result), "exited (code 7)") {
		t.Fatalf("result = %+v", result)
	}
}

// TestExitedWithoutExitCodeIsError pins TS's `exitCode !== 0` on null.
func TestExitedWithoutExitCodeIsError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "j-signal.log")
	if err := os.WriteFile(logPath, []byte("sig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1000)
	endedAt := int64(2000)
	signal := "SIGTERM"
	if err := storage.InsertJob(storage.JobRecord{
		JobID: "j-signal", Pid: 4242, SessionID: "s1", Command: "sleep 1",
		StartedAt: startedAt, EndedAt: &endedAt, Signal: &signal,
		State: storage.JobExited, LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}
	if result := run(t, map[string]any{"jobId": "j-signal"}); !result.IsError {
		t.Fatal("isError = false for exited-with-null-exit-code, want true")
	}
}

// TestListSessionJobs ports test.mjs case 5.
func TestListSessionJobs(t *testing.T) {
	insertJob(t, "j-list-a", "test-jobs", storage.JobExited, "a\n")
	insertJob(t, "j-list-b", "test-jobs", storage.JobExited, "b\n")

	listing := text(t, run(t, map[string]any{"__sessionId": "test-jobs"}))
	if !strings.Contains(listing, "j-list-a") || !strings.Contains(listing, "j-list-b") {
		t.Fatalf("listing = %q", listing)
	}
	if !strings.Contains(listing, "  exited (code 0) after 5s  echo hello-bg") {
		t.Fatalf("listing format = %q", listing)
	}
	other := run(t, map[string]any{"__sessionId": "other-session"})
	if text(t, other) != "No background jobs for this session." {
		t.Fatalf("other = %q", text(t, other))
	}
}

// TestRedaction pins redaction of job metadata and log output.
func TestRedaction(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	logPath := filepath.Join(t.TempDir(), "j-secret.log")
	if err := os.WriteFile(logPath, []byte("token="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1000)
	endedAt := int64(2000)
	exitCode := int64(0)
	if err := storage.InsertJob(storage.JobRecord{
		JobID: "j-secret", Pid: 4242, SessionID: "s1", Command: "printf safe-output # token=" + secret,
		StartedAt: startedAt, EndedAt: &endedAt, ExitCode: &exitCode,
		State: storage.JobExited, LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}
	result := run(t, map[string]any{"jobId": "j-secret"})
	got := text(t, result)
	if strings.Contains(got, secret) {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("no redaction marker: %q", got)
	}
	raw := run(t, map[string]any{"jobId": "j-secret", "redact_secrets": false})
	if !strings.Contains(text(t, raw), secret) {
		t.Fatalf("redact_secrets=false still redacted: %q", text(t, raw))
	}
}

// TestRunningWaitWithNoCompletion covers the `wait` branch without blocking.
func TestRunningWaitWithNoCompletion(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "j-running.log")
	if err := os.WriteFile(logPath, []byte("still here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.InsertJob(storage.JobRecord{
		JobID: "j-running", Pid: int64(os.Getpid()), SessionID: "s1", Command: "sleep 30",
		StartedAt: 1, State: storage.JobRunning, LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}
	got := text(t, run(t, map[string]any{"jobId": "j-running", "wait": 0.01}))
	if !strings.Contains(got, "running (") {
		t.Fatalf("text = %q", got)
	}
}

// TestFormatState pins every state line.
func TestFormatState(t *testing.T) {
	endedAt := int64(6000)
	exitCode := int64(9)
	signal := "SIGKILL"
	tests := []struct {
		name string
		job  storage.JobRecord
		want string
	}{
		{name: "running", job: storage.JobRecord{State: storage.JobRunning, Pid: 1234, StartedAt: 5000, EndedAt: &endedAt}, want: "running (1s elapsed, pid 1234)"},
		{name: "exited", job: storage.JobRecord{State: storage.JobExited, StartedAt: 1000, EndedAt: &endedAt, ExitCode: &exitCode}, want: "exited (code 9) after 5s"},
		{name: "exited-signal", job: storage.JobRecord{State: storage.JobExited, StartedAt: 1000, EndedAt: &endedAt, ExitCode: &exitCode, Signal: &signal}, want: "exited (code 9, signal SIGKILL) after 5s"},
		{name: "exited-unknown-code", job: storage.JobRecord{State: storage.JobExited, StartedAt: 1000, EndedAt: &endedAt}, want: "exited (code ?) after 5s"},
		{name: "killed", job: storage.JobRecord{State: storage.JobKilled, StartedAt: 1000, EndedAt: &endedAt, Signal: &signal}, want: "killed (SIGKILL) after 5s"},
		{name: "killed-default", job: storage.JobRecord{State: storage.JobKilled, StartedAt: 1000, EndedAt: &endedAt}, want: "killed (SIGTERM) after 5s"},
		{name: "lost", job: storage.JobRecord{State: storage.JobLost, StartedAt: 1000, EndedAt: &endedAt}, want: "lost (server restarted; exit code unrecoverable — log tail below)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatState(test.job); got != test.want {
				t.Fatalf("formatState = %q, want %q", got, test.want)
			}
		})
	}
}

// TestTruncateCommand pins whitespace collapse and the 120-column cap.
func TestTruncateCommand(t *testing.T) {
	if got := truncateCommand("  echo \n\t hi  "); got != "echo hi" {
		t.Fatalf("truncateCommand = %q", got)
	}
	if got := truncateCommand(strings.Repeat("x", 120)); got != strings.Repeat("x", 120) {
		t.Fatalf("120-char must stay intact: %d", len(got))
	}
	if got := truncateCommand(strings.Repeat("x", 121)); got != strings.Repeat("x", 117)+"..." {
		t.Fatalf("121-char = %q", got)
	}
	// UTF-16 length: 60 emoji are 120 code units, 61 are 122.
	emoji := strings.Repeat("😀", 60)
	if got := truncateCommand(emoji); got != emoji {
		t.Fatalf("60 emoji should not truncate: %d", len([]rune(got)))
	}
}

// TestNonNegativeInteger pins the JS Number/floor semantics.
func TestNonNegativeInteger(t *testing.T) {
	if value, ok := nonnegativeInteger(nil, false, 50); !ok || value != 50 {
		t.Fatalf("undefined = %d/%v", value, ok)
	}
	if value, ok := nonnegativeInteger("2.9", true, 0); !ok || value != 2 {
		t.Fatalf("string 2.9 = %d/%v", value, ok)
	}
	if _, ok := nonnegativeInteger(float64(-1), true, 0); ok {
		t.Fatal("negative should be null")
	}
	if _, ok := nonnegativeInteger("abc", true, 0); ok {
		t.Fatal("NaN should be null")
	}
}

// TestLimitIncrementalLines pins the line splitter and its byte accounting.
func TestLimitIncrementalLines(t *testing.T) {
	text, consumed, limited := limitIncrementalLines("a\nb\nc", 2)
	if text != "a\nb\n" || consumed != 4 || !limited {
		t.Fatalf("= %q/%d/%v", text, consumed, limited)
	}
	text, consumed, limited = limitIncrementalLines("a\nb", 5)
	if text != "a\nb" || consumed != 3 || limited {
		t.Fatalf("= %q/%d/%v", text, consumed, limited)
	}
}

// TestBudgetAndArchive pins the tail budget path.
func TestBudgetAndArchive(t *testing.T) {
	compression.ResetCache(context.Background())
	got := compression.ApplyBudget(context.Background(), strings.Repeat("line\n", 100), "bash")
	if !strings.Contains(got, "lines truncated") {
		t.Fatalf("budget = %q", got)
	}
}
