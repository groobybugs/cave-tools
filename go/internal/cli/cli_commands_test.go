package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
	"github.com/groobybugs/cave-tools/go/internal/tools/bash"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

func insertJobRow(t *testing.T, job storage.JobRecord) {
	t.Helper()
	if err := storage.InsertJob(job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}
}

func TestJobsEmpty(t *testing.T) {
	minimalEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"jobs"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "No background jobs recorded.\n" || stderr.Len() != 0 {
		t.Fatalf("jobs = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestJobsListFormatting(t *testing.T) {
	minimalEnv(t)
	started := time.Now().UnixMilli() - 2000
	ended := started + 2000
	exit := int64(3)
	logPath := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(logPath, []byte("out\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	insertJobRow(t, storage.JobRecord{
		JobID:     "jobfmt1",
		Pid:       4242,
		SessionID: "s",
		Command:   "echo   token=ghp_abcdefghijklmnopqrstuvwxyz0123456789\n  next",
		StartedAt: started,
		EndedAt:   &ended,
		ExitCode:  &exit,
		State:     storage.JobExited,
		LogPath:   logPath,
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"jobs"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("jobs exit = %d stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"jobfmt1", "exited(3)", "pid=4242", "  log: " + logPath} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("command not redacted: %q", output)
	}
	if !strings.Contains(output, "echo token=") || strings.Contains(output, "\n  next") {
		t.Fatalf("whitespace not collapsed: %q", output)
	}
}

func TestJobsKillUsageAndUnknown(t *testing.T) {
	minimalEnv(t)
	cases := []struct {
		name   string
		argv   []string
		code   int
		stderr string
	}{
		{name: "kill usage", argv: []string{"jobs", "kill"}, code: 1, stderr: "Usage: cave-tools jobs kill <jobId> [signal]\n"},
		{name: "log usage", argv: []string{"jobs", "log"}, code: 1, stderr: "Usage: cave-tools jobs log <jobId> [lines]\n"},
		{name: "unknown kill", argv: []string{"jobs", "kill", "nope"}, code: 1, stderr: "Unknown job: nope\n"},
		{name: "unknown log", argv: []string{"jobs", "log", "nope"}, code: 1, stderr: "Unknown job: nope\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), test.argv, strings.NewReader(""), &stdout, &stderr, "test")
			if code != test.code || stderr.String() != test.stderr || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestJobsLogTail(t *testing.T) {
	minimalEnv(t)
	logPath := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(logPath, []byte("a\nb\nc"), 0o600); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UnixMilli()
	insertJobRow(t, storage.JobRecord{
		JobID:     "joblog1",
		Pid:       1,
		SessionID: "s",
		Command:   "x",
		StartedAt: ended - 1000,
		EndedAt:   &ended,
		State:     storage.JobExited,
		LogPath:   logPath,
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"jobs", "log", "joblog1", "2"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "b\nc" || stderr.Len() != 0 {
		t.Fatalf("log = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	code = Run(context.Background(), []string{"jobs", "log", "joblog1"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "a\nb\nc" {
		t.Fatalf("default log = %d stdout=%q", code, stdout.String())
	}
}

func TestInitWritesInstructions(t *testing.T) {
	minimalEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("init exit = %d stderr=%q", code, stderr.String())
	}
	wantOutput := "Cave Tools instructions:\n  CLAUDE.md  created\n  AGENTS.md  created\n"
	if stdout.String() != wantOutput {
		t.Fatalf("output = %q, want %q", stdout.String(), wantOutput)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(content) != caveInstructions {
			t.Fatalf("%s content mismatch", name)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	code = Run(context.Background(), []string{"install-agent"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("install-agent exit = %d", code)
	}
	wantOutput = "Cave Tools instructions:\n  CLAUDE.md  already exists (left untouched)\n  AGENTS.md  already exists (left untouched)\n"
	if stdout.String() != wantOutput {
		t.Fatalf("output = %q, want %q", stdout.String(), wantOutput)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if string(content) != "mine" {
		t.Fatalf("existing file overwritten: %q", content)
	}
}

// TestInitInstructionsMatchTypeScript pins init output to the TS reference.
// Skips when node or the built dist/ tree is missing.
func TestInitInstructionsMatchTypeScript(t *testing.T) {
	// The reference must stay in TypeScript even when the host uses Go.
	t.Setenv("CAVE_TOOLS_IMPL", "")
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tsCLI := filepath.Join(root, "dist", "cli.js")
	if _, err := os.Stat(tsCLI); err != nil {
		t.Skip("dist/cli.js not built")
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules")); err != nil {
		t.Skip("node_modules not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command(nodePath, tsCLI, "init")
	cmd.Dir = dir
	cmd.Env = append(testenv.Environ(),
		"CAVE_TOOLS_DATA_DIR="+t.TempDir(),
		"CLAUDE_CONFIG_DIR="+t.TempDir(),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node dist/cli.js init: %v\n%s", err, output)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		tsContent, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read TS %s: %v", name, err)
		}
		if string(tsContent) != caveInstructions {
			t.Fatalf("%s differs from src/cli.ts:CAVE_INSTRUCTIONS\nTS:\n%s\nGo:\n%s", name, tsContent, caveInstructions)
		}
	}
}

func TestBinsCommand(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fixture uses POSIX executables")
	}
	minimalEnv(t)
	fakeBin := t.TempDir()
	for _, name := range []string{"rg", "fd"} {
		script := "#!/bin/sh\nexit 0\n"
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	rt.ResetBinsCache()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bins"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("bins exit = %d stderr=%q", code, stderr.String())
	}
	want := "binDir: " + filepath.Join(home, ".local", "share", "cave-tools", "bin") + "\n" +
		"rg: " + filepath.Join(fakeBin, "rg") + "\n" +
		"fd: " + filepath.Join(fakeBin, "fd") + "\n"
	if stdout.String() != want {
		t.Fatalf("bins output = %q, want %q", stdout.String(), want)
	}
}

func TestDaemonCommand(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("unix socket status check")
	}
	minimalEnv(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"daemon", "status"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 1 {
		t.Fatalf("daemon status exit = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "not running (socket ") {
		t.Fatalf("daemon status stdout = %q", stdout.String())
	}

	stdout.Reset()
	code = Run(context.Background(), []string{"daemon", "stop"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 1 || stdout.String() != "not running\n" {
		t.Fatalf("daemon stop = %d stdout=%q", code, stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"daemon", "bogus"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 2 || stderr.String() != "Usage: cave-tools daemon [start|stop|status]\n" {
		t.Fatalf("daemon bogus = %d stderr=%q", code, stderr.String())
	}
}

func TestRunDispatchBasics(t *testing.T) {
	minimalEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--version"}, strings.NewReader(""), &stdout, &stderr, "1.2.3")
	if code != 0 || stdout.String() != "cave-tools-go 1.2.3\n" || stderr.Len() != 0 {
		t.Fatalf("version = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"bogus"}, strings.NewReader(""), &stdout, &stderr, "1.2.3")
	if code != 2 || stderr.String() != usageLineCLI+"\n" {
		t.Fatalf("unknown = %d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"bench"}, strings.NewReader(""), &stdout, &stderr, "1.2.3")
	if code != 0 || stdout.String() != "Benchmark mode - not yet implemented\n" {
		t.Fatalf("bench = %d stdout=%q", code, stdout.String())
	}
}

func demoTool() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "cave__demo",
		Description: "Demo tool.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"count":{"type":"number"},"flag":{"type":"boolean"}},"required":["name"]}`),
	}
}

func TestToolCommandLocal(t *testing.T) {
	minimalEnv(t)
	var captured map[string]any
	var capturedCall *rt.CallContext
	installToolFixture(t, []types.ToolDefinition{demoTool()}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		captured = args
		capturedCall = call
		return types.ToolResult{Content: []types.Content{types.Text("ok")}}, nil
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo", "x", "--count", "0x2", "--flag"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "ok\n" || stderr.Len() != 0 {
		t.Fatalf("demo = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if captured["name"] != "x" || captured["count"] != float64(2) || captured["flag"] != true {
		t.Fatalf("args = %#v", captured)
	}
	if capturedCall == nil || !strings.HasPrefix(capturedCall.SessionID, "cli-") {
		t.Fatalf("call context = %#v", capturedCall)
	}
	if capturedCall.Cwd != "" || capturedCall.Env != nil {
		t.Fatalf("partial context should let dispatch default cwd/env: %#v", capturedCall)
	}
}

func TestToolCommandExplicitSession(t *testing.T) {
	minimalEnv(t)
	var capturedCall *rt.CallContext
	installToolFixture(t, []types.ToolDefinition{demoTool()}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		capturedCall = call
		return types.ToolResult{Content: []types.Content{types.Text("ok")}}, nil
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo", "x", "--session", "s9"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || capturedCall == nil || capturedCall.SessionID != "s9" {
		t.Fatalf("explicit session = %d call=%#v", code, capturedCall)
	}
}

func TestToolCommandResultShapes(t *testing.T) {
	minimalEnv(t)
	tests := []struct {
		name   string
		result types.ToolResult
		code   int
		stdout string
		stderr string
	}{
		{
			name:   "tool error",
			result: types.ToolResult{Content: []types.Content{types.Text("bad")}, IsError: true},
			code:   1,
			stderr: "bad\n",
		},
		{
			name: "cli meta",
			result: types.ToolResult{
				Content: []types.Content{types.Text("out")},
				Meta: map[string]any{"caveCli": map[string]any{
					"exitCode": 7,
					"stderr":   "err",
				}},
			},
			code:   7,
			stdout: "out\n",
			stderr: "err\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installToolFixture(t, []types.ToolDefinition{demoTool()}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
				return test.result, nil
			})
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), []string{"demo", "x"}, strings.NewReader(""), &stdout, &stderr, "test")
			if code != test.code || stdout.String() != test.stdout || stderr.String() != test.stderr {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestToolCommandUsageAndHelp(t *testing.T) {
	minimalEnv(t)
	installToolFixture(t, []types.ToolDefinition{demoTool()}, nil)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("usage exit = %d", code)
	}
	want := "cave-tools demo: missing required: name\nusage: cave-tools demo <name> [--count <number>] [--flag]\n"
	if stderr.String() != want || stdout.Len() != 0 {
		t.Fatalf("usage stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"demo", "--help"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || !strings.HasPrefix(stdout.String(), "demo: Demo tool.\n") {
		t.Fatalf("help = %d stdout=%q", code, stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"tool"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 2 || !strings.Contains(stderr.String(), `unknown tool ""`) {
		t.Fatalf("tool without name = %d stderr=%q", code, stderr.String())
	}
}

func TestToolCommandUnknownFlag(t *testing.T) {
	minimalEnv(t)
	installToolFixture(t, []types.ToolDefinition{demoTool()}, nil)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo", "x", "--bogus"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 2 || !strings.Contains(stderr.String(), "unknown flag --bogus") {
		t.Fatalf("unknown flag = %d stderr=%q", code, stderr.String())
	}
}

func TestToolCommandLocalError(t *testing.T) {
	minimalEnv(t)
	installToolFixture(t, []types.ToolDefinition{demoTool()}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		return types.ToolResult{}, errors.New("boom")
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo", "x"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 1 || stdout.Len() != 0 || stderr.String() != "cave-tools demo: boom\n" {
		t.Fatalf("error = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestToolCommandBashFlags(t *testing.T) {
	minimalEnv(t)
	var captured map[string]any
	installToolFixture(t, []types.ToolDefinition{bash.Tool}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		captured = args
		return types.ToolResult{Content: []types.Content{types.Text("bash ok")}}, nil
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bash", "echo", "hi"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("bash exit = %d stderr=%q", code, stderr.String())
	}
	if captured["command"] != "echo hi" || captured["description"] != "cave-tools CLI" || captured["__cli"] != true {
		t.Fatalf("bash args = %#v", captured)
	}
}

func TestToolCommandBashRawPassthrough(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("raw shell passthrough is POSIX")
	}
	minimalEnv(t)
	t.Setenv("CAVE_RAW", "1")
	called := false
	installToolFixture(t, []types.ToolDefinition{bash.Tool}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		called = true
		return types.ToolResult{}, nil
	})
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bash", "echo", "raw-ok"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "raw-ok\n" || called {
		t.Fatalf("raw bash = %d stdout=%q called=%v", code, stdout.String(), called)
	}
}

func TestToolCommandDaemonOutcome(t *testing.T) {
	minimalEnv(t)
	localCalled := false
	installToolFixture(t, []types.ToolDefinition{demoTool()}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		localCalled = true
		return types.ToolResult{Content: []types.Content{types.Text("local")}}, nil
	})
	var daemonExplicit bool
	var daemonCall *rt.CallContext
	hooks.DaemonEnabled = func() bool { return true }
	hooks.DaemonCall = func(ctx context.Context, name string, args map[string]any, call *rt.CallContext, explicit bool) (daemonCallOutcome, error) {
		daemonExplicit = explicit
		daemonCall = call
		return daemonCallOutcome{Sent: true, Result: types.ToolResult{Content: []types.Content{types.Text("remote")}}}, nil
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo", "x", "--session", "s9"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "remote\n" || localCalled {
		t.Fatalf("daemon = %d stdout=%q local=%v", code, stdout.String(), localCalled)
	}
	if !daemonExplicit || daemonCall == nil || daemonCall.SessionID != "s9" || daemonCall.Cwd == "" || daemonCall.Env == nil {
		t.Fatalf("daemon context = %#v explicit=%v", daemonCall, daemonExplicit)
	}

	// Sent=false falls back to the in-process call.
	localCalled = false
	hooks.DaemonCall = func(ctx context.Context, name string, args map[string]any, call *rt.CallContext, explicit bool) (daemonCallOutcome, error) {
		return daemonCallOutcome{Sent: false}, nil
	}
	stdout.Reset()
	code = Run(context.Background(), []string{"demo", "x"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 || stdout.String() != "local\n" || !localCalled {
		t.Fatalf("fallback = %d stdout=%q local=%v", code, stdout.String(), localCalled)
	}
}

func TestLocalToolCallFlushesStats(t *testing.T) {
	minimalEnv(t)
	// Use the production bindings (server.CallTool) so the tool records its
	// savings in the process bucket before the CLI flushes it.
	old := hooks
	t.Cleanup(func() { hooks = old })
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bash", "echo flush-me"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("bash exit = %d stderr=%q", code, stderr.String())
	}
	rows, err := storage.ListSessionStats(false)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Pid == int64(os.Getpid()) {
			found = true
			if row.Savings.TotalCalls < 1 {
				t.Fatalf("stats not flushed: %#v", row.Savings)
			}
		}
	}
	if !found {
		t.Fatalf("no stats row for pid %d: %#v", os.Getpid(), rows)
	}
}

func TestDaemonCallDoesNotFlushLocalStats(t *testing.T) {
	minimalEnv(t)
	const sentinelCalls = int64(7)
	if err := storage.UpsertSessionStats(storage.PersistedStats{
		Pid:       int64(os.Getpid()),
		UpdatedAt: 1,
		Savings:   storage.SavingsStats{TotalCalls: sentinelCalls},
	}); err != nil {
		t.Fatalf("UpsertSessionStats: %v", err)
	}
	installToolFixture(t, []types.ToolDefinition{demoTool()}, func(ctx context.Context, name string, args map[string]any, call *rt.CallContext) (types.ToolResult, error) {
		t.Fatal("daemon-served call must not run locally")
		return types.ToolResult{}, nil
	})
	hooks.DaemonEnabled = func() bool { return true }
	hooks.DaemonCall = func(ctx context.Context, name string, args map[string]any, call *rt.CallContext, explicit bool) (daemonCallOutcome, error) {
		return daemonCallOutcome{Sent: true, Result: types.ToolResult{Content: []types.Content{types.Text("remote")}}}, nil
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"demo", "x"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("demo exit = %d stderr=%q", code, stderr.String())
	}
	rows, err := storage.ListSessionStats(false)
	if err != nil {
		t.Fatalf("ListSessionStats: %v", err)
	}
	for _, row := range rows {
		if row.Pid != int64(os.Getpid()) {
			continue
		}
		if row.Savings.TotalCalls != sentinelCalls {
			t.Fatalf("daemon call flushed local stats: calls=%d want=%d", row.Savings.TotalCalls, sentinelCalls)
		}
		return
	}
	t.Fatal("sentinel stats row disappeared")
}

func TestMCPUnlinkedIsReported(t *testing.T) {
	old := hooks
	hooks = integration{}
	t.Cleanup(func() { hooks = old })
	minimalEnv(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"mcp"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != 1 || stderr.String() != "Fatal error: server runtime not linked\n" {
		t.Fatalf("mcp = %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
