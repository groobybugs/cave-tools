package main

import (
	"bytes"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
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

func TestRunStubCommands(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		command string
	}{
		{name: "no args names mcp", args: nil, command: "mcp"},
		{name: "named command", args: []string{"status"}, command: "status"},
		{name: "unknown command", args: []string{"bogus", "--flag"}, command: "bogus"},
		{name: "version with extra argv is not version", args: []string{"--version", "x"}, command: "--version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			want := "cave-tools-go: not implemented yet: " + test.command + "\n"
			if stderr.String() != want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), want)
			}
		})
	}
}

func TestVersionDefaultIsNonEmpty(t *testing.T) {
	// The default matches package.json; the parity harness re-stamps it via
	// -ldflags and fails if the two drift.
	if version == "" {
		t.Fatal("version constant must not be empty")
	}
}
