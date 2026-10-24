// Command cave-tools-go is the Go port of the cave-tools CLI and MCP server.
//
// main only handles process wiring: it dispatches the durable job worker
// selector before any flag parsing, stamps the package version and forwards
// argv/stdin/stdout/stderr to cli.Run.
package main

import (
	"context"
	"io"
	"os"

	"github.com/groobybugs/cave-tools/go/internal/cli"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
)

// version is the package.json version this port targets. Override it at build
// time from package.json so the binary never drifts from the TS release:
//
//	go build -ldflags "-X main.version=$(node -p "require('./package.json').version")" ./cmd/cave-tools-go
//
// Without -ldflags the constant below is used (plain `go build` / `go test`).
var version = "0.6.1"

func main() {
	os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

// run executes one binary invocation and returns its exit code. The durable
// job worker selector is checked before cli.Run so `__job-worker` can never be
// parsed as a normal command.
func run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) > 1 && argv[1] == runtime.JobWorkerCommand {
		return runtime.RunJobWorker(argv[2:])
	}
	args := []string{}
	if len(argv) > 1 {
		args = argv[1:]
	}
	return cli.Run(context.Background(), args, stdin, stdout, stderr, version)
}
