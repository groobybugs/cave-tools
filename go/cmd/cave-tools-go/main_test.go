package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"cave-tools-go", "--version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	want := "cave-tools-go " + version + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"cave-tools-go", "bogus", "--flag"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "Usage: cave-tools [") {
		t.Fatalf("stderr = %q, want usage line", stderr.String())
	}
}

func TestRunBench(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"cave-tools-go", "bench"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "Benchmark mode - not yet implemented\n" {
		t.Fatalf("bench = %d stdout=%q", code, stdout.String())
	}
}

func TestJobWorkerDispatchedBeforeFlagParsing(t *testing.T) {
	// An invalid worker argv exits 1 with the worker's usage error; the normal
	// CLI would treat __job-worker as an unknown command and exit 2.
	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	var stdout bytes.Buffer
	code := run([]string{"cave-tools-go", "__job-worker"}, strings.NewReader(""), &stdout, io.Discard)
	os.Stderr = old
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	message, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if string(message) != "cave job worker: invalid arguments\n" {
		t.Fatalf("stderr = %q", message)
	}
}

func TestVersionDefaultIsNonEmpty(t *testing.T) {
	// The default matches package.json; the parity harness re-stamps it via
	// -ldflags and fails if the two drift.
	if version == "" {
		t.Fatal("version constant must not be empty")
	}
}
