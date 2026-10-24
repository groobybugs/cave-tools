package bash

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
)

// Handler-level differential: run the TypeScript handler from dist/ and the Go
// handler on the same args, cwd and fixture, then compare the whole ToolResult
// JSON. Skips when node or the built dist/ tree is missing.
func TestDifferentialAgainstTypeScript(t *testing.T) {
	nodePath, distModule := differentialDeps(t, "bash")
	fixture := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixture, "note.txt"), []byte("hello fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args string
	}{
		{name: "echo", args: `{"command":"echo hello","description":"d"}`},
		{name: "soft-failure", args: `{"command":"sh -c 'echo out; echo err >&2; exit 7'","description":"d","allowFailure":true}`},
		{name: "hard-failure", args: `{"command":"sh -c 'exit 7'","description":"d"}`},
		{name: "bearer-redaction", args: `{"command":"echo 'Authorization: Bearer sk-live-abc123xyz789secret'","description":"d"}`},
		{name: "aws-redaction-off", args: `{"command":"echo 'api_key=AKIAIOSFODNN7EXAMPLE'","description":"d","redact_secrets":false}`},
		{name: "negative-timeout", args: `{"command":"echo hi","description":"d","timeout":-1}`},
		{name: "oversize-timeout", args: `{"command":"echo hi","description":"d","timeout":600001}`},
		{name: "string-timeout", args: `{"command":"echo hi","description":"d","timeout":"abc"}`},
		{name: "zero-timeout", args: `{"command":"echo no-timeout-ok","description":"d","timeout":0}`},
		{name: "budgeted-lines", args: `{"command":"seq 1 70","description":"d","allowFailure":true}`},
		{name: "compact-json", args: `{"command":"printf '{\\n  \"a\": 1,\\n  \"b\": 2\\n}'","description":"d"}`},
		{name: "missing-workdir", args: `{"command":"echo hi","description":"d","workdir":"nope-does-not-exist"}`},
		{name: "cli-mode", args: `{"command":"echo o; echo e >&2; exit 3","description":"cli","__cli":true}`},
		{name: "cli-mode-clean", args: `{"command":"echo clean","description":"cli","__cli":true}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			tsJSON := runTypeScriptHandler(t, nodePath, distModule, "bashTool", testCase.args, fixture, tsData)
			goJSON := runGoHandler(t, fixture, testCase.args)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// TestToolMetadataParity compares the tool declaration with dist/.
func TestToolMetadataParity(t *testing.T) {
	nodePath, distModule := differentialDeps(t, "bash")
	want := runNodeJSON(t, nodePath, toolScript, map[string]string{
		"CAVE_DIFF_MODULE": distModule,
		"CAVE_DIFF_EXPORT": "bashTool",
	}, filepath.Dir(distModule))
	declared, err := json.Marshal(struct {
		Name        string          `json:"name"`
		Annotations json.RawMessage `json:"annotations,omitempty"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}{Tool.Name, Tool.Annotations, Tool.Description, Tool.InputSchema})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, declared)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("metadata differs\nTS: %#v\nGo: %#v", want, got)
	}
}

const toolScript = `import { pathToFileURL } from "node:url";
const module = await import(pathToFileURL(process.env.CAVE_DIFF_MODULE).href);
const tool = module[process.env.CAVE_DIFF_EXPORT];
process.stdout.write(JSON.stringify({name: tool.name, annotations: tool.annotations, description: tool.description, inputSchema: tool.inputSchema}));`

const handlerScript = `import { pathToFileURL } from "node:url";
const module = await import(pathToFileURL(process.env.CAVE_DIFF_MODULE).href);
const tool = module[process.env.CAVE_DIFF_EXPORT];
const result = await tool.handler(JSON.parse(process.env.CAVE_DIFF_ARGS));
process.stdout.write(JSON.stringify(result));`

// differentialDeps locates node and one dist tool module, skipping when the
// reference build is absent.
func differentialDeps(t *testing.T, file string) (string, string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(root, "dist", "tools", file+".js")
	if _, err := os.Stat(module); err != nil {
		t.Skipf("dist/tools/%s.js not built: run `pnpm build` first", file)
	}
	return nodePath, module
}

// runTypeScriptHandler runs the TS tool handler and decodes its ToolResult.
func runTypeScriptHandler(t *testing.T, nodePath, module, export, argsJSON, cwd, dataDir string) any {
	t.Helper()
	return runNodeJSON(t, nodePath, handlerScript, map[string]string{
		"CAVE_DIFF_MODULE":    module,
		"CAVE_DIFF_EXPORT":    export,
		"CAVE_DIFF_ARGS":      argsJSON,
		"CAVE_TOOLS_DATA_DIR": dataDir,
		"CLAUDE_CONFIG_DIR":   filepath.Join(dataDir, "claude"),
	}, cwd)
}

// runGoHandler runs the Go tool handler through a call context whose cwd and
// env match the TS subprocess.
func runGoHandler(t *testing.T, cwd, argsJSON string) any {
	t.Helper()
	env := rt.EnvFromEnviron(os.Environ())
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args: %v", err)
	}
	var result any
	rt.RunWithContext(context.Background(), &rt.CallContext{
		SessionID: "differential",
		Cwd:       cwd,
		Env:       env,
		ClientID:  "local",
	}, func(ctx context.Context) struct{} {
		output, err := Tool.Handler(ctx, args)
		if err != nil {
			t.Fatalf("go handler: %v", err)
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("marshal go result: %v", err)
		}
		result = decodeJSON(t, encoded)
		return struct{}{}
	})
	return result
}

// runNodeJSON executes an ES module snippet and decodes its stdout as JSON.
func runNodeJSON(t *testing.T, nodePath, script string, env map[string]string, cwd string) any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodePath, "--input-type=module", "-e", script)
	cmd.Dir = cwd
	cmd.Env = nodeEnv(env)
	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("node failed: %v\n%s", err, stderr)
	}
	if ctx.Err() != nil {
		t.Fatalf("node timed out: %v", ctx.Err())
	}
	return decodeJSON(t, output)
}

// nodeEnv merges overrides into the process environment without duplicate keys.
func nodeEnv(overrides map[string]string) []string {
	env := rt.EnvFromEnviron(os.Environ())
	for key, value := range overrides {
		env[key] = value
	}
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	return entries
}

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return value
}

// TestDifferentialMissingCommand pins String(args.command): a missing key is
// `undefined`, not `null`. Both sides get a PATH whose first entry has scripts
// named after the two spellings, so the executed command is visible.
func TestDifferentialMissingCommand(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the fixture commands are POSIX shell scripts")
	}
	nodePath, distModule := differentialDeps(t, "bash")
	fixture := t.TempDir()
	binDir := t.TempDir()
	for name, output := range map[string]string{"undefined": "ran-undefined", "null": "ran-null"} {
		script := "#!/bin/sh\necho " + output + "\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		"PATH": binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	cases := []struct{ name, args string }{
		{name: "missing-command", args: `{}`},
		{name: "null-command", args: `{"command":null}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			overrides := map[string]string{
				"CAVE_TOOLS_DATA_DIR": tsData,
				"CLAUDE_CONFIG_DIR":   filepath.Join(tsData, "claude"),
			}
			for key, value := range env {
				overrides[key] = value
			}
			tsJSON := runTypeScriptHandlerEnv(t, nodePath, distModule, "bashTool", testCase.args, fixture, overrides)
			goJSON := runGoHandlerEnv(t, fixture, testCase.args, env)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// TestDifferentialFractionalTimeout pins Node's timer clamp: setTimeout(0.5)
// fires after 1ms, so a fractional timeout below 1ms still kills the process.
// Casting it to int64 would disable the timer entirely.
func TestDifferentialFractionalTimeout(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("`sleep` comes from the POSIX shell")
	}
	nodePath, distModule := differentialDeps(t, "bash")
	fixture := t.TempDir()
	tsData := t.TempDir()
	args := `{"command":"sleep 0.2","description":"fractional timeout","timeout":0.5}`
	tsJSON := runTypeScriptHandler(t, nodePath, distModule, "bashTool", args, fixture, tsData)
	goJSON := runGoHandler(t, fixture, args)
	if !reflect.DeepEqual(tsJSON, goJSON) {
		t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
	}
}

// TestDifferentialPathErrors pins the Node errno text for the two portable
// non-ENOENT stat failures a workdir can hit: ENOTDIR and ELOOP.
func TestDifferentialPathErrors(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("errno text and symlink creation differ on Windows")
	}
	nodePath, distModule := differentialDeps(t, "bash")
	fixture := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixture, "note.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop", filepath.Join(fixture, "loop")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	cases := []struct{ name, workdir string }{
		{name: "enotdir", workdir: "note.txt/child"},
		{name: "eloop", workdir: "loop"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			args := `{"command":"echo hi","description":"d","workdir":"` + testCase.workdir + `"}`
			tsData := t.TempDir()
			tsJSON := runTypeScriptHandler(t, nodePath, distModule, "bashTool", args, fixture, tsData)
			goJSON := runGoHandler(t, fixture, args)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// runTypeScriptHandlerEnv is runTypeScriptHandler with extra environment
// values merged over the hermetic CAVE_TOOLS defaults.
func runTypeScriptHandlerEnv(t *testing.T, nodePath, module, export, argsJSON, cwd string, extra map[string]string) any {
	t.Helper()
	env := map[string]string{
		"CAVE_DIFF_MODULE": module,
		"CAVE_DIFF_EXPORT": export,
		"CAVE_DIFF_ARGS":   argsJSON,
	}
	for key, value := range extra {
		env[key] = value
	}
	return runNodeJSON(t, nodePath, handlerScript, env, cwd)
}

// runGoHandlerEnv is runGoHandler with extra environment values.
func runGoHandlerEnv(t *testing.T, cwd, argsJSON string, extra map[string]string) any {
	t.Helper()
	env := rt.EnvFromEnviron(os.Environ())
	for key, value := range extra {
		env[key] = value
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args: %v", err)
	}
	var result any
	rt.RunWithContext(context.Background(), &rt.CallContext{
		SessionID: "differential",
		Cwd:       cwd,
		Env:       env,
		ClientID:  "local",
	}, func(ctx context.Context) struct{} {
		output, err := Tool.Handler(ctx, args)
		if err != nil {
			t.Fatalf("go handler: %v", err)
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("marshal go result: %v", err)
		}
		result = decodeJSON(t, encoded)
		return struct{}{}
	})
	return result
}
