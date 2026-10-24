// Server/daemon seams for the CLI.
//
// cli.ts imports dispatch.js (tool registry) and daemon-client.js lazily, and
// cli-tools.ts imports daemon-client.js directly. The Go port keeps the same
// layering (cli is the top entry layer) but wires the concrete server and
// daemon packages from a separate file so the argument parser, help renderer,
// status report, jobs/bins commands and raw/filter paths can be built and
// tested before those packages land. bindings_server.go / bindings_daemon.go
// install the production implementations; tests install fixtures.
package cli

import (
	"context"
	"errors"
	"io"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// errRuntimeUnlinked is reported only by a binary built without bindings.go
// (the pre-W4i/W4j partial state).
var errRuntimeUnlinked = errors.New("server runtime not linked")

// daemonCallOutcome mirrors daemon.CallOutcome without importing the package.
type daemonCallOutcome struct {
	Sent   bool
	Result types.ToolResult
}

// integration is the concrete server/daemon surface the CLI calls. Every
// field is a function so tests can replace one piece without a package stub.
type integration struct {
	ToolNames  func() []string
	IsToolName func(string) bool
	LoadTool   func(string) (types.ToolDefinition, error)
	CallTool   func(context.Context, string, map[string]any, *runtime.CallContext) (types.ToolResult, error)

	// DaemonCall runs one call through the shared daemon; explicitSession is
	// parsed.sessionId !== undefined in TS (kind is always "cli").
	DaemonCall    func(context.Context, string, map[string]any, *runtime.CallContext, bool) (daemonCallOutcome, error)
	DaemonEnabled func() bool

	StartDaemon func(context.Context, io.Writer, io.Writer) error
	StartServer func(context.Context, string, io.Reader, io.Writer, io.Writer) error
}

// hooks is installed by bindings.go in production and by tests.
var hooks integration

func toolNamesRuntime() []string {
	if hooks.ToolNames == nil {
		return nil
	}
	return hooks.ToolNames()
}

func isToolNameRuntime(name string) bool {
	return hooks.IsToolName != nil && hooks.IsToolName(name)
}

func loadToolRuntime(name string) (types.ToolDefinition, error) {
	if hooks.LoadTool == nil {
		return types.ToolDefinition{}, errRuntimeUnlinked
	}
	return hooks.LoadTool(name)
}

func callToolRuntime(ctx context.Context, name string, args map[string]any, call *runtime.CallContext) (types.ToolResult, error) {
	if hooks.CallTool == nil {
		return types.ToolResult{}, errRuntimeUnlinked
	}
	return hooks.CallTool(ctx, name, args, call)
}

func daemonEnabledRuntime() bool {
	return hooks.DaemonEnabled != nil && hooks.DaemonEnabled()
}

func daemonCallRuntime(ctx context.Context, name string, args map[string]any, call *runtime.CallContext, explicitSession bool) (daemonCallOutcome, error) {
	if hooks.DaemonCall == nil {
		return daemonCallOutcome{}, errRuntimeUnlinked
	}
	return hooks.DaemonCall(ctx, name, args, call, explicitSession)
}

func startDaemonRuntime(ctx context.Context, stdout, stderr io.Writer) error {
	if hooks.StartDaemon == nil {
		return errRuntimeUnlinked
	}
	return hooks.StartDaemon(ctx, stdout, stderr)
}

func startServerRuntime(ctx context.Context, version string, stdin io.Reader, stdout, stderr io.Writer) error {
	if hooks.StartServer == nil {
		return errRuntimeUnlinked
	}
	return hooks.StartServer(ctx, version, stdin, stdout, stderr)
}
