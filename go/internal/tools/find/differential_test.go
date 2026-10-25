package find

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// Handler-level differential: run the TS handler from dist/ and the Go handler
// on the same args, cwd and fixture, then compare the whole ToolResult JSON.
// Skips when node or the built dist/ tree is missing.
func TestDifferentialAgainstTypeScript(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	fixtureDir := fixture(t)

	cases := []struct {
		name string
		args string
	}{
		{name: "markdown", args: `{"pattern":"*.md","path":".","limit":50}`},
		{name: "recursive", args: `{"pattern":"**/*.ts","path":".","limit":50}`},
		{name: "hidden", args: `{"pattern":"*","path":".","limit":50}`},
		{name: "no-hidden", args: `{"pattern":"*","path":".","limit":50,"hidden":false}`},
		{name: "missing-path", args: `{"pattern":"*","path":"missing","limit":5}`},
		{name: "no-files", args: `{"pattern":"*.nope","path":"."}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			tsJSON := normalizeFindOrder(runTypeScriptFind(t, nodePath, distModule, testCase.args, fixtureDir, tsData))
			goJSON := normalizeFindOrder(runGoHandler(t, fixtureDir, testCase.args))
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// normalizeFindOrder sorts the path lines of a find result. fd emits entries
// in discovery order, which is not stable between two runs of the same binary;
// the test compares the set of paths, not their order.
func normalizeFindOrder(value any) any {
	result, ok := value.(map[string]any)
	if !ok {
		return value
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return value
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		return value
	}
	text, ok := block["text"].(string)
	if !ok {
		return value
	}
	body := text
	notice := ""
	if index := strings.Index(text, "\n\n["); index >= 0 {
		body, notice = text[:index], text[index:]
	}
	lines := strings.Split(body, "\n")
	sort.Strings(lines)
	block["text"] = strings.Join(lines, "\n") + notice
	return value
}

// TestToolMetadataParity compares the tool declaration with dist/.
func TestToolMetadataParity(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	want := runNodeJSON(t, nodePath, toolScript, map[string]string{
		"CAVE_DIFF_MODULE": distModule,
		"CAVE_DIFF_EXPORT": "findTool",
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
	distModule = filepath.Join(root, "dist", "tools", "find.js")
	if _, err := os.Stat(distModule); err != nil {
		t.Skip("dist/tools/find.js not built: run `pnpm build` first")
	}
	return nodePath, distModule
}

func runTypeScriptFind(t *testing.T, nodePath, module, argsJSON, cwd, dataDir string) any {
	t.Helper()
	return runNodeJSON(t, nodePath, handlerScript, map[string]string{
		"CAVE_DIFF_MODULE":    module,
		"CAVE_DIFF_EXPORT":    "findTool",
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
	env := testenv.ScrubMap(rt.EnvFromEnviron(os.Environ()))
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

// TestDifferentialFallbackOrder exercises the no-fd/no-rg findWithNode path on
// both sides with the same PATH (two existing but non-executable files, so the
// resolvers pick them, the spawns fail and neither side tries a download).
// Results are compared raw: TS sorts with localeCompare, so B.ts/a.ts order is
// part of the contract and must not be normalized away.
func TestDifferentialFallbackOrder(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	fixtureDir := t.TempDir()
	for _, name := range []string{"B.ts", "a.ts", "C.ts", "b.ts", "undefined", "null"} {
		if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binDir := t.TempDir()
	for _, name := range []string{"fd", "rg"} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		args string
	}{
		{name: "locale-order", args: `{"pattern":"*","limit":100}`},
		{name: "fractional-limit", args: `{"pattern":"*","limit":2.5}`},
		{name: "string-fractional-limit", args: `{"pattern":"*","limit":"2.5"}`},
		{name: "huge-limit", args: `{"pattern":"*","limit":1e21}`},
		{name: "missing-pattern", args: `{}`},
		{name: "null-pattern", args: `{"pattern":null}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			env := map[string]string{
				"PATH":                binDir,
				"CAVE_TOOLS_DATA_DIR": tsData,
				"CLAUDE_CONFIG_DIR":   filepath.Join(tsData, "claude"),
			}
			tsJSON := runTypeScriptFindEnv(t, nodePath, distModule, testCase.args, fixtureDir, env)
			goJSON := runGoHandlerEnv(t, fixtureDir, testCase.args, env)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// runTypeScriptFindEnv is runTypeScriptFind with extra environment values.
func runTypeScriptFindEnv(t *testing.T, nodePath, module, argsJSON, cwd string, extra map[string]string) any {
	t.Helper()
	env := map[string]string{
		"CAVE_DIFF_MODULE": module,
		"CAVE_DIFF_EXPORT": "findTool",
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
