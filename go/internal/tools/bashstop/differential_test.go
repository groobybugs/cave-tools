package bashstop

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
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// Handler-level differential: insert the same job rows on both sides, run the
// TS handler from dist/ and the Go handler, then compare the whole ToolResult
// JSON. Skips when node or dist/ is missing.
func TestDifferentialAgainstTypeScript(t *testing.T) {
	nodePath, distModule, distDB := differentialDeps(t)
	fixture := t.TempDir()
	logPath := filepath.Join(fixture, "diff.log")
	if err := os.WriteFile(logPath, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := func(items ...map[string]any) string {
		encoded, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	finishedJob := func(id, session string, exitCode any) map[string]any {
		return map[string]any{
			"jobId": id, "pid": 4242, "sessionId": session, "command": "exit 7",
			"workdir": nil, "startedAt": 1000, "endedAt": 2000, "exitCode": exitCode,
			"signal": nil, "state": "exited", "logPath": logPath,
		}
	}

	cases := []struct {
		name string
		jobs string
		args string
	}{
		{name: "unsupported-signal", args: `{"jobId":"j-diff-stop","signal":"SIGBOGUS"}`},
		{name: "unknown-job", args: `{"jobId":"j-diff-stop-missing"}`},
		{name: "missing-job-id", args: `{}`},
		{name: "null-job-id", args: `{"jobId":null}`},
		{
			name: "already-finished",
			jobs: jobs(finishedJob("j-diff-stop-done", "diff-stop", 7)),
			args: `{"jobId":"j-diff-stop-done"}`,
		},
		{
			name: "already-finished-null-code",
			jobs: jobs(finishedJob("j-diff-stop-null", "diff-stop", nil)),
			args: `{"jobId":"j-diff-stop-null"}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsData := t.TempDir()
			tsJSON := runTypeScriptStop(t, nodePath, distModule, distDB, testCase.jobs, testCase.args, fixture, tsData)
			if testCase.jobs != "" {
				var records []storage.JobRecord
				if err := json.Unmarshal([]byte(testCase.jobs), &records); err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					if err := storage.InsertJob(record); err != nil {
						t.Fatal(err)
					}
				}
			}
			goJSON := runGoHandler(t, fixture, testCase.args)
			if !reflect.DeepEqual(tsJSON, goJSON) {
				t.Fatalf("TS and Go disagree\nTS: %#v\nGo: %#v", tsJSON, goJSON)
			}
		})
	}
}

// TestToolMetadataParity compares the tool declaration with dist/.
func TestToolMetadataParity(t *testing.T) {
	nodePath, distModule, _ := differentialDeps(t)
	want := runNodeJSON(t, nodePath, toolScript, map[string]string{
		"CAVE_DIFF_MODULE": distModule,
		"CAVE_DIFF_EXPORT": "bashStopTool",
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
const tools = await import(pathToFileURL(process.env.CAVE_DIFF_MODULE).href);
if (process.env.CAVE_DIFF_JOB) {
  const db = await import(pathToFileURL(process.env.CAVE_DIFF_DB).href);
  for (const job of JSON.parse(process.env.CAVE_DIFF_JOB)) db.insertJob(job);
}
const result = await tools[process.env.CAVE_DIFF_EXPORT].handler(JSON.parse(process.env.CAVE_DIFF_ARGS));
process.stdout.write(JSON.stringify(result));`

func differentialDeps(t *testing.T) (nodePath, distModule, distDB string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	distModule = filepath.Join(root, "dist", "tools", "bash-stop.js")
	distDB = filepath.Join(root, "dist", "storage", "db.js")
	if _, err := os.Stat(distModule); err != nil {
		t.Skip("dist/tools/bash-stop.js not built: run `pnpm build` first")
	}
	if _, err := os.Stat(distDB); err != nil {
		t.Skip("dist/storage/db.js not built: run `pnpm build` first")
	}
	return nodePath, distModule, distDB
}

func runTypeScriptStop(t *testing.T, nodePath, module, distDB, jobsJSON, argsJSON, cwd, dataDir string) any {
	t.Helper()
	env := map[string]string{
		"CAVE_DIFF_MODULE":    module,
		"CAVE_DIFF_EXPORT":    "bashStopTool",
		"CAVE_DIFF_ARGS":      argsJSON,
		"CAVE_DIFF_DB":        distDB,
		"CAVE_TOOLS_DATA_DIR": dataDir,
		"CLAUDE_CONFIG_DIR":   filepath.Join(dataDir, "claude"),
	}
	if jobsJSON != "" {
		env["CAVE_DIFF_JOB"] = jobsJSON
	}
	return runNodeJSON(t, nodePath, handlerScript, env, cwd)
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
