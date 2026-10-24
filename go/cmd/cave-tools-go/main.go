// Command cave-tools-go is the Go port of the cave-tools CLI and MCP server.
//
// Wave 0 ships only the version probe; every other command is a stub so the
// binary can be built, tested and wired into the parity harness while the
// remaining packages are ported (see docs/go-port.md).
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the package.json version this port targets. Override it at build
// time from package.json so the binary never drifts from the TS release:
//
//	go build -ldflags "-X main.version=$(node -p "require('./package.json').version")" ./cmd/cave-tools-go
//
// Without -ldflags the constant below is used (plain `go build` / `go test`).
var version = "0.6.1"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one CLI invocation and returns its exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "cave-tools-go %s\n", version)
		return 0
	}

	// TS treats no argv as the MCP server, so name that command in the stub
	// message instead of reporting an empty argv0.
	command := "mcp"
	if len(args) > 0 {
		command = args[0]
	}
	fmt.Fprintf(stderr, "cave-tools-go: not implemented yet: %s\n", command)
	return 2
}
