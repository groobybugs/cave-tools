//go:build parity

// End-to-end CLI tests: the real binary, a durable __job-worker child and a
// shared daemon. These complement the argv/stdout parity table by exercising
// the parts that need more than one process.
package parity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCLIDurableJobRoundTrip drives bash_start -> __job-worker -> jobs/log.
func TestCLIDurableJobRoundTrip(t *testing.T) {
	root := repoRoot(t)
	binary := buildGoBinary(t, root, packageVersion(t, root))
	dataDir := t.TempDir()
	run := func(args ...string) outcome {
		return runProcess(t, binary, nil, parityCase{args: args}, dataDir)
	}

	started := run("bash_start", "echo job-ok; echo err-ok >&2; exit 5")
	if started.code != 0 {
		t.Fatalf("bash_start failed: code=%d stderr=%q", started.code, started.stderr)
	}
	jobID := ""
	for _, line := range strings.Split(started.stdout, "\n") {
		if value, ok := strings.CutPrefix(line, "jobId: "); ok {
			jobID = value
			break
		}
	}
	if jobID == "" {
		t.Fatalf("bash_start output has no jobId: %q", started.stdout)
	}

	waited := run("bash_status", jobID, "--wait", "60", "--tail_lines", "0")
	// bash_status reports a non-zero job as a tool error (exit 1); the wait
	// itself succeeded when the status output names the job.
	if !strings.Contains(waited.stdout+waited.stderr, jobID) {
		t.Fatalf("bash_status --wait produced no job status: code=%d stdout=%q stderr=%q", waited.code, waited.stdout, waited.stderr)
	}

	list := run("jobs")
	if list.code != 0 || !strings.Contains(list.stdout, jobID) || !strings.Contains(list.stdout, "exited(5)") {
		t.Fatalf("jobs = code %d stdout %q stderr %q", list.code, list.stdout, list.stderr)
	}

	log := run("jobs", "log", jobID, "50")
	if log.code != 0 || !strings.Contains(log.stdout, "job-ok") || !strings.Contains(log.stdout, "[cave-job exit=5]") {
		t.Fatalf("jobs log = code %d stdout %q", log.code, log.stdout)
	}

	resultPath := filepath.Join(dataDir, "jobs", jobID+".result.json")
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("durable result sidecar: %v", err)
	}
	for _, want := range []string{`"jobId":"` + jobID + `"`, `"exitCode":5`} {
		if !strings.Contains(string(result), want) {
			t.Fatalf("result sidecar missing %q: %s", want, result)
		}
	}

	killed := run("jobs", "kill", jobID)
	if killed.code != 0 || !strings.Contains(killed.stdout, "already finished (state: exited).") {
		t.Fatalf("jobs kill = code %d stdout %q", killed.code, killed.stdout)
	}
}

// TestCLIDaemonRoundTrip starts the real daemon, runs a tool through it, and
// checks explicit-session state sharing before stopping it.
func TestCLIDaemonRoundTrip(t *testing.T) {
	root := repoRoot(t)
	binary := buildGoBinary(t, root, packageVersion(t, root))
	dataDir := t.TempDir()
	runDir := filepath.Join(dataDir, "run")
	extras := map[string]string{
		"XDG_RUNTIME_DIR":           runDir,
		"CAVE_TOOLS_DAEMON_IDLE_MS": "1500",
	}
	daemonEnv := isolatedEnv(dataDir, extras)
	command := exec.Command(binary, "daemon", "start")
	command.Dir = root
	command.Env = daemonEnv
	var daemonStdout, daemonStderr strings.Builder
	command.Stdout = &daemonStdout
	command.Stderr = &daemonStderr
	if err := command.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- command.Wait() }()
	daemonExited := false
	defer func() {
		if daemonExited {
			return
		}
		_ = command.Process.Kill()
		select {
		case <-daemonDone:
		case <-time.After(2 * time.Second):
		}
	}()

	run := func(env map[string]string, args ...string) outcome {
		merged := map[string]string{}
		for key, value := range extras {
			merged[key] = value
		}
		for key, value := range env {
			merged[key] = value
		}
		return runProcess(t, binary, nil, parityCase{args: args, env: merged}, dataDir)
	}

	ready := false
	for attempt := 0; attempt < 100; attempt++ {
		status := run(nil, "daemon", "status")
		if status.code == 0 && strings.Contains(status.stdout, "running pid ") {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("daemon never became ready: stdout=%q stderr=%q", daemonStdout.String(), daemonStderr.String())
	}

	bash := run(map[string]string{"CAVE_TOOLS_DAEMON": "1"}, "bash", "echo daemon-ok")
	if bash.code != 0 || !strings.Contains(bash.stdout, "daemon-ok") {
		t.Fatalf("daemon bash = code %d stdout %q stderr %q", bash.code, bash.stdout, bash.stderr)
	}

	file := filepath.Join(dataDir, "session.txt")
	if err := os.WriteFile(file, []byte("session content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := run(map[string]string{"CAVE_TOOLS_DAEMON": "1"}, "read", file, "--session", "s1")
	if !strings.HasPrefix(first.stdout, "session content") {
		t.Fatalf("first session read = %q", first.stdout)
	}
	second := run(map[string]string{"CAVE_TOOLS_DAEMON": "1"}, "read", file, "--session", "s1")
	if !strings.HasPrefix(second.stdout, "<file unchanged") {
		t.Fatalf("second session read = %q", second.stdout)
	}

	// Exercise media and error/exit metadata through the real CLI formatter,
	// not only through MCP or a fixture daemon callback.
	png := []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	imagePath := filepath.Join(dataDir, "image.png")
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	image := run(map[string]string{"CAVE_TOOLS_DAEMON": "1"}, "read", imagePath)
	directImage := run(map[string]string{"CAVE_TOOLS_DAEMON": "0"}, "read", imagePath)
	if image != directImage || image.code != 0 || !strings.Contains(image.stdout, "[image image/png 16 B]") {
		t.Fatalf("daemon image = %#v, direct = %#v", image, directImage)
	}
	failure := run(map[string]string{"CAVE_TOOLS_DAEMON": "1"}, "bash", "printf cli-out; printf cli-err >&2; exit 3")
	directFailure := run(map[string]string{"CAVE_TOOLS_DAEMON": "0"}, "bash", "printf cli-out; printf cli-err >&2; exit 3")
	if failure != directFailure || failure.code != 3 {
		t.Fatalf("daemon bash exit metadata = %#v, direct = %#v", failure, directFailure)
	}
	missingPath := filepath.Join(dataDir, "missing.txt")
	missing := run(map[string]string{"CAVE_TOOLS_DAEMON": "1"}, "read", missingPath)
	directMissing := run(map[string]string{"CAVE_TOOLS_DAEMON": "0"}, "read", missingPath)
	if missing != directMissing || missing.code != 1 || !strings.Contains(missing.stderr, "missing.txt") {
		t.Fatalf("daemon soft error = %#v, direct = %#v", missing, directMissing)
	}

	stopped := run(nil, "daemon", "stop")
	if stopped.code != 0 || stopped.stdout != "stopped\n" {
		t.Fatalf("daemon stop = code %d stdout %q", stopped.code, stopped.stdout)
	}
	select {
	case err := <-daemonDone:
		daemonExited = true
		if err != nil {
			t.Fatalf("daemon exited with %v (stderr %q)", err, daemonStderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after stop")
	}
}
