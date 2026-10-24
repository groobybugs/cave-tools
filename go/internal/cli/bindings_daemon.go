// Production daemon bindings for the CLI.
//
// The server bindings (tool registry, MCP server and daemon start callback)
// live in bindings_server.go once internal/server exports them; the daemon
// client itself is available now.
package cli

import (
	"context"

	"github.com/groobybugs/cave-tools/go/internal/daemon"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
)

func init() {
	hooks.DaemonEnabled = func() bool { return daemon.DaemonEnabled() }
	hooks.DaemonCall = func(ctx context.Context, tool string, args map[string]any, call *runtime.CallContext, explicitSession bool) (daemonCallOutcome, error) {
		outcome, err := daemon.GetDaemonClient(daemon.HelloOptions{
			Kind:            "cli",
			ExplicitSession: explicitSession,
		}).Call(ctx, tool, args, call)
		if err != nil {
			return daemonCallOutcome{}, err
		}
		return daemonCallOutcome{Sent: outcome.Sent, Result: outcome.Result}, nil
	}
}
