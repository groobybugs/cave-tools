package runtime

import (
	"context"
	"os"
	"testing"
)

func TestGetContextEmpty(t *testing.T) {
	if got := GetContext(context.Background()); got != nil {
		t.Fatalf("GetContext on plain context = %#v, want nil", got)
	}
	// nil contexts must not panic.
	if got := GetContext(nil); got != nil {
		t.Fatalf("GetContext(nil) = %#v, want nil", got)
	}
}

func TestRunWithContextPropagates(t *testing.T) {
	pid := 4242
	call := &CallContext{
		SessionID:        "s1",
		Cwd:              "/tmp/example",
		Env:              map[string]string{"CAVE": "yes"},
		ClientID:         "conn-7",
		ClientPID:        &pid,
		SessionNamespace: "cli",
	}

	got := RunWithContext(context.Background(), call, func(ctx context.Context) *CallContext {
		return GetContext(ctx)
	})
	if got != call {
		t.Fatalf("RunWithContext returned %p, want the same CallContext %p", got, call)
	}

	RunWithContext(context.Background(), call, func(ctx context.Context) bool {
		if cwd := GetCwd(ctx); cwd != "/tmp/example" {
			t.Errorf("GetCwd = %q, want /tmp/example", cwd)
		}
		if env := GetEnv(ctx); env["CAVE"] != "yes" {
			t.Errorf("GetEnv[CAVE] = %q, want yes", env["CAVE"])
		}
		if id := GetClientID(ctx); id != "conn-7" {
			t.Errorf("GetClientID = %q, want conn-7", id)
		}
		if ctx.Value(contextKey{}) == nil {
			t.Error("derived context lost the CallContext value")
		}
		return true
	})
}

func TestProcessFallbacks(t *testing.T) {
	ctx := context.Background()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if got := GetCwd(ctx); got != wd {
		t.Fatalf("GetCwd fallback = %q, want %q", got, wd)
	}
	if got := GetClientID(ctx); got != "local" {
		t.Fatalf("GetClientID fallback = %q, want local", got)
	}
	t.Setenv("CAVE_TOOLS_CONTEXT_PROBE", "set")
	if got := GetEnv(ctx)["CAVE_TOOLS_CONTEXT_PROBE"]; got != "set" {
		t.Fatalf("GetEnv fallback missed process env: %q", got)
	}
}

func TestLocalContext(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	call := LocalContext("session-9")
	if call.SessionID != "session-9" {
		t.Errorf("SessionID = %q", call.SessionID)
	}
	if call.Cwd != wd {
		t.Errorf("Cwd = %q, want %q", call.Cwd, wd)
	}
	if call.ClientID != "local" {
		t.Errorf("ClientID = %q", call.ClientID)
	}
	if call.ClientPID != nil || call.SessionNamespace != "" {
		t.Errorf("local context must leave daemon fields empty: %#v", call)
	}
	if call.Env["PATH"] != os.Getenv("PATH") {
		t.Errorf("Env did not copy the process environment")
	}
}

func TestEnvFromEnviron(t *testing.T) {
	env := EnvFromEnviron([]string{"A=1", "B=x=y", "NOEQUALS"})
	if env["A"] != "1" || env["B"] != "x=y" {
		t.Fatalf("EnvFromEnviron = %#v", env)
	}
	if _, ok := env["NOEQUALS"]; ok {
		t.Fatalf("entry without = must be skipped: %#v", env)
	}
}

func TestContextIsolation(t *testing.T) {
	base := context.Background()
	first := &CallContext{SessionID: "a", Cwd: "/a", ClientID: "a"}
	second := &CallContext{SessionID: "b", Cwd: "/b", ClientID: "b"}

	nested := RunWithContext(base, first, func(ctx context.Context) string {
		inner := RunWithContext(ctx, second, func(innerCtx context.Context) string {
			return GetCwd(innerCtx)
		})
		if inner != "/b" {
			t.Errorf("inner GetCwd = %q, want /b", inner)
		}
		return GetCwd(ctx)
	})
	if nested != "/a" {
		t.Fatalf("outer GetCwd = %q, want /a after inner run", nested)
	}
	if GetContext(base) != nil {
		t.Fatal("parent context was mutated by a child run")
	}
}

func TestCallContextEnvIsNotCopied(t *testing.T) {
	// TS returns the process.env / stored env object itself, so mutations by
	// the caller are visible to the next reader.
	env := map[string]string{"K": "v1"}
	call := &CallContext{Env: env}
	RunWithContext(context.Background(), call, func(ctx context.Context) bool {
		GetEnv(ctx)["K"] = "v2"
		return true
	})
	if env["K"] != "v2" {
		t.Fatalf("GetEnv must return the stored map, got %q", env["K"])
	}
}
