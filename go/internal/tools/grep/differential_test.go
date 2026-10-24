package grep

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, "HIT_TOKEN line "+strconv.Itoa(i+1))
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "many.ts"), []byte(strings.Join(many, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Fractional context needs a match past line 2 so line - context stays
	// above the Math.max(1, ...) clamp.
	if err := os.WriteFile(filepath.Join(fixtureDir, "ctx.txt"), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Missing-key coercion: String(undefined) is "undefined", String(null) is
	// "null"; the two lines make the difference visible in the matches.
	if err := os.WriteFile(filepath.Join(fixtureDir, "presence.txt"), []byte("undefined\nnull\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		args      string
		sortLines bool
	}{
		{name: "matches-a", args: `{"pattern":"ALPHA","path":"a.ts"}`},
		{name: "matches-b", args: `{"pattern":"ALPHA","path":"b.md"}`},
		{name: "glob", args: `{"pattern":"ALPHA","path":".","glob":"*.ts","limit":10}`},
		{name: "literal", args: `{"pattern":"$5.00","path":".","glob":"lit.txt","literal":true,"limit":5}`},
		{name: "regex-no-match", args: `{"pattern":"$5.00","path":".","glob":"lit.txt","limit":5}`},
		{name: "ignore-case", args: `{"pattern":"alpha","path":".","glob":"a.ts","ignoreCase":true,"limit":5}`},
		{name: "context", args: `{"pattern":"beta","path":".","glob":"a.ts","context":1,"limit":5}`},
		{name: "missing-path", args: `{"pattern":"x","path":"nope","limit":5}`},
		{name: "single-file", args: `{"pattern":"ALPHA","path":"a.ts"}`},
		{name: "per-file-cap", args: `{"pattern":"HIT_TOKEN","path":".","glob":"many.ts","limit":100}`},
		{name: "multi-file-sorted", args: `{"pattern":"ALPHA","path":".","limit":10}`, sortLines: true},
		{name: "limit-one-in-file", args: `{"pattern":"export","path":"a.ts","limit":1}`},
		{name: "invalid-pattern", args: `{"pattern":"(","path":"."}`},
		{name: "no-matches", args: `{"pattern":"NO_SUCH_TOKEN","path":"."}`},
		{name: "fractional-context", args: `{"pattern":"three","path":".","glob":"ctx.txt","context":1.5,"limit":5}`},
		{name: "fractional-context-tail", args: `{"pattern":"four","path":".","glob":"ctx.txt","context":1.5,"limit":5}`},
		{name: "fractional-context-integer-start", args: `{"pattern":"one","path":".","glob":"ctx.txt","context":1.5,"limit":5}`},
		{name: "fractional-context-half", args: `{"pattern":"three","path":".","glob":"ctx.txt","context":0.5,"limit":5}`},
		{name: "huge-context", args: `{"pattern":"three","path":".","glob":"ctx.txt","context":1e300,"limit":5}`},
		{name: "string-context-is-zero", args: `{"pattern":"three","path":".","glob":"ctx.txt","context":"1.5","limit":5}`},
		{name: "missing-pattern", args: `{}`},
		{name: "null-pattern", args: `{"pattern":null}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			tsJSON := runTypeScriptGrep(t, nodePath, distModule, testCase.args, fixtureDir, tsData)
			goJSON := runGoHandler(t, fixtureDir, testCase.args)
			if testCase.sortLines {
				tsJSON = normalizeLineOrder(tsJSON)
				goJSON = normalizeLineOrder(goJSON)
			}
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// normalizeLineOrder sorts match lines: ripgrep emits matches from parallel
// workers, so the order across files is not stable between two runs. Order
// inside one file is still covered by the single-file cases.
func normalizeLineOrder(value any) any {
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
		"CAVE_DIFF_EXPORT": "grepTool",
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
	distModule = filepath.Join(root, "dist", "tools", "grep.js")
	if _, err := os.Stat(distModule); err != nil {
		t.Skip("dist/tools/grep.js not built: run `pnpm build` first")
	}
	return nodePath, distModule
}

func runTypeScriptGrep(t *testing.T, nodePath, module, argsJSON, cwd, dataDir string) any {
	t.Helper()
	return runNodeJSON(t, nodePath, handlerScript, map[string]string{
		"CAVE_DIFF_MODULE":    module,
		"CAVE_DIFF_EXPORT":    "grepTool",
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

// TestDifferentialNaNLimitWithFakeRg pins the NaN-limit path against a
// deterministic fake rg: TS never breaks out of the record loop because
// matches.length > NaN is false, so a later malformed record must still throw
// even though slice(0, NaN) is empty. A Go int64 limit would stop after the
// first match and hide the error.
func TestDifferentialNaNLimitWithFakeRg(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	fixtureDir := t.TempDir()
	binDir := t.TempDir()
	rgPath := filepath.Join(binDir, "rg")
	script := "#!/bin/sh\n/bin/cat \"$RGFIXTURE\"\nexit \"$RGEXIT\"\n"
	if err := os.WriteFile(rgPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rgPath, 0o755); err != nil {
		t.Fatal(err)
	}

	match := `{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"hello\n"},"line_number":1}}` + "\n" +
		`{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"hello again\n"},"line_number":2}}` + "\n"
	cases := []struct {
		name string
		body string
		exit string
	}{
		{name: "malformed-record-after-match", body: match + "not json\n", exit: "0"},
		{name: "code-2-truncates", body: match, exit: "2"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rgFixture := filepath.Join(t.TempDir(), "rg.json")
			if err := os.WriteFile(rgFixture, []byte(testCase.body), 0o600); err != nil {
				t.Fatal(err)
			}
			tsData := t.TempDir()
			env := map[string]string{
				"PATH":                binDir,
				"RGFIXTURE":           rgFixture,
				"RGEXIT":              testCase.exit,
				"CAVE_TOOLS_DATA_DIR": tsData,
				"CLAUDE_CONFIG_DIR":   filepath.Join(tsData, "claude"),
			}
			args := `{"pattern":"hello","path":".","limit":"abc"}`
			tsJSON := runTypeScriptGrepEnv(t, nodePath, distModule, args, fixtureDir, env)
			goJSON := runGoHandlerEnv(t, fixtureDir, args, env)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// runTypeScriptGrepEnv is runTypeScriptGrep with extra environment values
// (used to point PATH at a fake rg).
func runTypeScriptGrepEnv(t *testing.T, nodePath, module, argsJSON, cwd string, env map[string]string) any {
	t.Helper()
	overrides := map[string]string{
		"CAVE_DIFF_MODULE": module,
		"CAVE_DIFF_EXPORT": "grepTool",
		"CAVE_DIFF_ARGS":   argsJSON,
	}
	for key, value := range env {
		overrides[key] = value
	}
	return runNodeJSON(t, nodePath, handlerScript, overrides, cwd)
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
