package ls

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
)

// Handler-level differential: run the TS handler from dist/ and the Go handler
// on the same args, cwd and fixture, then compare the whole ToolResult JSON.
// Skips when node or the built dist/ tree is missing.
func TestDifferentialAgainstTypeScript(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	fixtureDir := fixture(t)
	// Mixed-case names exercise the ICU collation both sides use.
	for name, content := range map[string]string{
		"Zebra": "z\n", "apple": "a\n", "a1": "1\n", "A1": "2\n",
	} {
		if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(fixtureDir, "Dir2"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args string
	}{
		{name: "listing", args: `{"path":".","limit":50}`},
		{name: "pagination", args: `{"path":".","limit":2,"offset":2}`},
		{name: "empty-dir", args: `{"path":"empty","limit":50}`},
		{name: "missing-dir", args: `{"path":"nope","limit":5}`},
		{name: "offset-beyond", args: `{"path":".","offset":50}`},
		{name: "limit-string", args: `{"path":".","limit":"3"}`},
		{name: "default-path", args: `{}`},
		{name: "null-path", args: `{"path":null}`},
		{name: "null-limit", args: `{"path":".","limit":null}`},
		{name: "null-offset", args: `{"path":".","offset":null}`},
		{name: "fractional-limit", args: `{"path":".","limit":2.5}`},
		{name: "fractional-offset", args: `{"path":".","offset":1.5,"limit":50}`},
		{name: "huge-limit", args: `{"path":".","limit":1e21}`},
	}

	if err := os.MkdirAll(filepath.Join(fixtureDir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			tsJSON := runTypeScriptLS(t, nodePath, distModule, testCase.args, fixtureDir, tsData)
			goJSON := runGoHandler(t, fixtureDir, testCase.args)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// TestToolMetadataParity compares the tool declaration with dist/.
func TestToolMetadataParity(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	want := runNodeJSON(t, nodePath, toolScript, map[string]string{
		"CAVE_DIFF_MODULE": distModule,
		"CAVE_DIFF_EXPORT": "lsTool",
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
	if got := decodeJSON(t, declared); !reflect.DeepEqual(want, got) {
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

func differentialDeps(t *testing.T) (nodePath, distModule string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	distModule = filepath.Join(root, "dist", "tools", "ls.js")
	if _, err := os.Stat(distModule); err != nil {
		t.Skip("dist/tools/ls.js not built: run `pnpm build` first")
	}
	return nodePath, distModule
}

func runTypeScriptLS(t *testing.T, nodePath, module, argsJSON, cwd, dataDir string) any {
	t.Helper()
	return runNodeJSON(t, nodePath, handlerScript, map[string]string{
		"CAVE_DIFF_MODULE":    module,
		"CAVE_DIFF_EXPORT":    "lsTool",
		"CAVE_DIFF_ARGS":      argsJSON,
		"CAVE_TOOLS_DATA_DIR": dataDir,
		"CLAUDE_CONFIG_DIR":   filepath.Join(dataDir, "claude"),
	}, cwd)
}

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
