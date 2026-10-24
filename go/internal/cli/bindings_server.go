// Production server bindings for the CLI and daemon start.
//
// internal/server owns tool listing/dispatch and the stdio MCP server;
// internal/daemon starts with that registry injected, which keeps the
// server -> daemon import direction impossible (no cycle).
package cli

import (
	"context"
	"io"

	"github.com/groobybugs/cave-tools/go/internal/daemon"
	"github.com/groobybugs/cave-tools/go/internal/server"
)

func init() {
	hooks.ToolNames = func() []string { return server.ToolNames }
	hooks.IsToolName = server.IsToolName
	hooks.LoadTool = server.LoadTool
	hooks.CallTool = server.CallTool
	hooks.StartServer = func(ctx context.Context, version string, stdin io.Reader, stdout, stderr io.Writer) error {
		return server.StartServer(ctx, version, stdin, stdout, stderr)
	}
	hooks.StartDaemon = func(ctx context.Context, stdout, stderr io.Writer) error {
		return daemon.StartDaemon(ctx, server.AllTools, server.CallTool)
	}
}
