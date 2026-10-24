// Package runtime holds the per-call execution context.
//
// Port of src/runtime/context.ts.
//
// AsyncLocalStorage -> context.Context mapping:
//
// TS keeps the current CallContext in an AsyncLocalStorage store, so any
// function can read it implicitly through getContext()/getCwd()/getEnv()/
// getClientId() without threading a parameter. Go has no implicit propagation,
// so the CallContext travels explicitly inside a context.Context value:
//
//	runWithContext(ctx, fn)   -> RunWithContext(ctx, call, fn)
//	getContext()              -> GetContext(ctx)
//	getCwd()                  -> GetCwd(ctx)
//	getEnv()                  -> GetEnv(ctx)
//	getClientId()             -> GetClientID(ctx)
//	localContext(sessionId)   -> LocalContext(sessionID)
//
// RunWithContext is the analogue of the AsyncLocalStorage.run call: it derives
// a child context carrying the CallContext and passes it to fn. Everything fn
// calls must take that same ctx to observe the call's cwd/env/session, exactly
// as TS code relies on the ambient store instead of a parameter.
package runtime

import (
	"context"
	"os"
	"strings"
)

// CallContext is the per-call context. In-process calls use this process's
// cwd/env; the daemon runs each call with the calling client's cwd/env so
// clients never see each other's environment or working directory.
// port of src/runtime/context.ts:CallContext
type CallContext struct {
	SessionID string
	Cwd       string
	// Env is NodeJS.ProcessEnv: every variable of one environment block.
	Env map[string]string
	// ClientID says who is calling: "local" in-process, a per-connection id in
	// the daemon.
	ClientID string
	// ClientPID is daemon only: the client process, which owns this call's
	// stats row.
	ClientPID *int
	// SessionNamespace is daemon only: session-key namespace when not per
	// connection (explicit CLI --session uses "cli" so separate CLI processes
	// share read state).
	SessionNamespace string
}

type contextKey struct{}

// RunWithContext runs fn with call as the current call context.
// port of src/runtime/context.ts:runWithContext
func RunWithContext[T any](ctx context.Context, call *CallContext, fn func(context.Context) T) T {
	return fn(context.WithValue(ctx, contextKey{}, call))
}

// GetContext returns the CallContext carried by ctx, or nil outside a call.
// port of src/runtime/context.ts:getContext
func GetContext(ctx context.Context) *CallContext {
	if ctx == nil {
		return nil
	}
	call, _ := ctx.Value(contextKey{}).(*CallContext)
	return call
}

// GetCwd returns the call's working directory, falling back to the process cwd.
// port of src/runtime/context.ts:getCwd
func GetCwd(ctx context.Context) string {
	if call := GetContext(ctx); call != nil {
		return call.Cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		// TS process.cwd() throws here; Go callers get "" rather than a panic.
		return ""
	}
	return wd
}

// GetEnv returns the call's environment, falling back to the process env.
// port of src/runtime/context.ts:getEnv
func GetEnv(ctx context.Context) map[string]string {
	if call := GetContext(ctx); call != nil {
		return call.Env
	}
	return EnvFromEnviron(os.Environ())
}

// GetClientID returns the caller id, defaulting to "local".
// port of src/runtime/context.ts:getClientId
func GetClientID(ctx context.Context) string {
	if call := GetContext(ctx); call != nil {
		return call.ClientID
	}
	return "local"
}

// LocalContext builds the in-process call context for a session.
// port of src/runtime/context.ts:localContext
func LocalContext(sessionID string) *CallContext {
	wd, _ := os.Getwd()
	return &CallContext{
		SessionID: sessionID,
		Cwd:       wd,
		Env:       EnvFromEnviron(os.Environ()),
		ClientID:  "local",
	}
}

// EnvFromEnviron converts os.Environ() into the map shape CallContext.Env uses.
// The first "=" splits the entry, matching how Node builds process.env keys.
func EnvFromEnviron(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}
