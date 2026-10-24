package bashstop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain serves as the durable job worker when this test binary is
// re-executed with JobWorkerCommand, and isolates storage in a temp dir.
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == rt.JobWorkerCommand {
		os.Exit(rt.RunJobWorker(os.Args[2:]))
	}
	dir, err := os.MkdirTemp("", "cave-tools-go-bashstop-")
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

// TestToolMetadata pins the declaration to src/tools/bash-stop.ts.
func TestToolMetadata(t *testing.T) {
	if Tool.Name != "cave__bash_stop" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if Tool.Annotations != nil {
		t.Fatalf("annotations = %s, want none", Tool.Annotations)
	}
	if !strings.Contains(string(Tool.InputSchema), `"required":["jobId"]`) {
		t.Fatalf("input schema = %s", Tool.InputSchema)
	}
}

// TestUnsupportedSignal pins the message and signal order.
func TestUnsupportedSignal(t *testing.T) {
	result := run(t, map[string]any{"jobId": "j-x", "signal": "SIGBOGUS"})
	if !result.IsError {
		t.Fatal("isError = false")
	}
	want := "Unsupported signal: SIGBOGUS. Use one of: SIGTERM, SIGINT, SIGKILL, SIGHUP"
	if got := text(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// TestUnknownJob pins the unknown-job error.
func TestUnknownJob(t *testing.T) {
	result := run(t, map[string]any{"jobId": "j-surely-missing"})
	if !result.IsError || text(t, result) != "Unknown job: j-surely-missing" {
		t.Fatalf("result = %+v", result)
	}
}

// TestAlreadyFinished ports the non-running branch.
func TestAlreadyFinished(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "j-done.log")
	if err := os.WriteFile(logPath, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1000)
	endedAt := int64(2000)
	exitCode := int64(7)
	if err := storage.InsertJob(storage.JobRecord{
		JobID: "j-stop-done", Pid: 4242, SessionID: "s1", Command: "exit 7",
		StartedAt: startedAt, EndedAt: &endedAt, ExitCode: &exitCode,
		State: storage.JobExited, LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}
	result := run(t, map[string]any{"jobId": "j-stop-done"})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	if got := text(t, result); got != "j-stop-done already finished (state: exited, exit 7)." {
		t.Fatalf("text = %q", got)
	}
}

// TestAlreadyFinishedWithoutExitCode pins the null-exit-code suffix.
func TestAlreadyFinishedWithoutExitCode(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "j-done-signal.log")
	if err := os.WriteFile(logPath, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1000)
	endedAt := int64(2000)
	signal := "SIGTERM"
	if err := storage.InsertJob(storage.JobRecord{
		JobID: "j-stop-signal", Pid: 4242, SessionID: "s1", Command: "sleep 1",
		StartedAt: startedAt, EndedAt: &endedAt, Signal: &signal,
		State: storage.JobExited, LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}
	got := text(t, run(t, map[string]any{"jobId": "j-stop-signal"}))
	if got != "j-stop-signal already finished (state: exited)." {
		t.Fatalf("text = %q", got)
	}
}

// TestStopKillsJob ports test.mjs case 3 (stop -> killed).
func TestStopKillsJob(t *testing.T) {
	record, err := rt.StartJob(context.Background(), rt.StartJobInput{Command: "sleep 30", SessionID: "stop-test"})
	if err != nil {
		t.Fatal(err)
	}
	result := run(t, map[string]any{"jobId": record.JobID})
	if result.IsError {
		t.Fatalf("isError = true: %s", text(t, result))
	}
	got := text(t, result)
	want := record.JobID + " killed (sent SIGTERM to process group "
	if !strings.HasPrefix(got, want) {
		t.Fatalf("text = %q, want prefix %q", got, want)
	}
	if !strings.HasSuffix(got, "SIGKILL follows in 3s if needed). Log: "+record.LogPath) {
		t.Fatalf("text = %q", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := rt.GetJobInfo(record.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if info != nil && info.State == storage.JobKilled {
			if rt.IsProcessAlive(int(record.Pid)) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s still alive after stop", record.JobID)
}
