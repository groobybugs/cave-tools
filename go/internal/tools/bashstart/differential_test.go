package bashstart

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
)

// Handler-level differential: run the TS handler from dist/ and the Go handler
// on the same args and cwd, then compare the ToolResult JSON after normalizing
// the random job id, pid and per-side log path. Skips when node or dist/ is
// missing.
func TestDifferentialAgainstTypeScript(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	fixture := t.TempDir()

	cases := []struct {
		name string
		args string
	}{
		{name: "start-job", args: `{"command":"echo hello-bg","description":"fast echo job","__sessionId":"diff-start"}`},
		{name: "description-fallback", args: `{"command":"echo 'Authorization: Bearer sk-live-abcdefghijklmnop'","description":"","__sessionId":"diff-start"}`},
		{name: "missing-workdir", args: `{"command":"echo hi","description":"d","workdir":"nope-does-not-exist"}`},
		{name: "null-description", args: `{"command":"echo hi","description":null,"__sessionId":"diff-start"}`},
		{name: "missing-command", args: `{"__sessionId":"diff-start"}`},
		{name: "null-command", args: `{"command":null,"__sessionId":"diff-start"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			tsJSON := runTypeScriptStart(t, nodePath, distModule, testCase.args, fixture, tsData)
			goJSON := runGoHandler(t, fixture, testCase.args)
			tsText := normalizeStartOutput(t, tsJSON, tsData)
			goText := normalizeStartOutput(t, goJSON, os.Getenv("CAVE_TOOLS_DATA_DIR"))
			if !reflect.DeepEqual(tsText, goText) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsText, goText)
			}
		})
	}
}

var (
	jobIDLineRE   = regexp.MustCompile(`jobId: \S+`)
	jobIDQuotedRE = regexp.MustCompile(`"jobId": "[^"]+"`)
	logPathLineRE = regexp.MustCompile(`logPath: \S+`)
	pidLineRE     = regexp.MustCompile(`pid: \d+`)
)

// normalizeStartOutput replaces the nondeterministic job id, pid and log path
// so the deterministic shape can be compared.
func normalizeStartOutput(t *testing.T, value any, dataDir string) any {
	t.Helper()
	return normalizeValue(value, dataDir)
}

func normalizeValue(value any, dataDir string) any {
	switch typed := value.(type) {
	case string:
		normalized := jobIDLineRE.ReplaceAllString(typed, "jobId: <JOB>")
		normalized = jobIDQuotedRE.ReplaceAllString(normalized, `"jobId": "<JOB>"`)
		normalized = logPathLineRE.ReplaceAllString(normalized, "logPath: <LOG>")
		normalized = pidLineRE.ReplaceAllString(normalized, "pid: <PID>")
		if dataDir != "" {
			normalized = strings.ReplaceAll(normalized, dataDir, "<DATA>")
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeValue(item, dataDir)
		}
		return normalized
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeValue(item, dataDir)
		}
		return normalized
	default:
		return value
	}
}

// TestToolMetadataParity compares the tool declaration with dist/.
func TestToolMetadataParity(t *testing.T) {
	nodePath, distModule := differentialDeps(t)
	want := runNodeJSON(t, nodePath, toolScript, map[string]string{
		"CAVE_DIFF_MODULE": distModule,
		"CAVE_DIFF_EXPORT": "bashStartTool",
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
	distModule = filepath.Join(root, "dist", "tools", "bash-start.js")
	if _, err := os.Stat(distModule); err != nil {
		t.Skip("dist/tools/bash-start.js not built: run `pnpm build` first")
	}
	return nodePath, distModule
}

func runTypeScriptStart(t *testing.T, nodePath, module, argsJSON, cwd, dataDir string) any {
	t.Helper()
	return runNodeJSON(t, nodePath, handlerScript, map[string]string{
		"CAVE_DIFF_MODULE":    module,
		"CAVE_DIFF_EXPORT":    "bashStartTool",
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
