//go:build parity

package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Differential harness: runs the TypeScript handler from dist/ and the Go
// handler on the same arguments, then compares the full ToolResult JSON.
//
//	pnpm build && cd go && go test -tags parity -run TestParity ./internal/tools/webfetch/

const tsRunner = `import { pathToFileURL } from "node:url";

const toolPath = process.argv[2];
const exportName = process.argv[3];
const realFetch = globalThis.fetch;
if (process.env.PARITY_FETCH_BASE) {
  const base = new URL(process.env.PARITY_FETCH_BASE);
  globalThis.fetch = (url, init) => {
    const target = new URL(String(url));
    target.protocol = base.protocol;
    target.host = base.host;
    return realFetch(target, init);
  };
}
let data = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { data += chunk; });
process.stdin.on("end", async () => {
  try {
    const mod = await import(pathToFileURL(toolPath).href);
    const result = await mod[exportName].handler(JSON.parse(data));
    process.stdout.write(JSON.stringify(result));
  } catch (error) {
    process.stderr.write(String((error && error.stack) || error));
    process.exit(1);
  }
});
`

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func writeRunner(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run-tool.mjs")
	if err := os.WriteFile(path, []byte(tsRunner), 0o600); err != nil {
		t.Fatalf("runner: %v", err)
	}
	return path
}

func runTS(t *testing.T, root, toolPath, exportName string, args map[string]any, env map[string]string, fetchBase string) string {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	command := exec.Command("node", writeRunner(t), filepath.Join(root, toolPath), exportName)
	command.Stdin = bytes.NewReader(encoded)
	command.Env = append(os.Environ(),
		"CAVE_TOOLS_DATA_DIR="+os.Getenv("CAVE_TOOLS_DATA_DIR"),
		"PARITY_FETCH_BASE="+fetchBase,
	)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node %s: %v\n%s", toolPath, err, stderr.String())
	}
	return stdout.String()
}

// TestParityHandler compares TS and Go ToolResult JSON for every case.
func TestParityHandler(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "webfetch.js")); err != nil {
		t.Skip("dist/tools/webfetch.js missing; run pnpm build")
	}
	server := newFixtureServer(t)
	base := server.URL

	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing url", map[string]any{"format": "markdown"}},
		{"bad scheme", map[string]any{"url": "ftp://example.com"}},
		{"zero url", map[string]any{"url": float64(0)}},
		{"unparseable url", map[string]any{"url": "http://"}},
		{"null url", map[string]any{"url": nil}},
		{"null format", map[string]any{"url": base + "/text", "format": nil}},
		{"null timeout", map[string]any{"url": base + "/text", "timeout": nil}},
		{"null redact secrets", map[string]any{"url": base + "/secret", "redact_secrets": nil}},
		{"space in host", map[string]any{"url": "https://exa mple.com"}},
		{"url trimmed", map[string]any{"url": "  " + base + "/text  ", "format": "text"}},
		{"text plain text format", map[string]any{"url": base + "/text", "format": "text"}},
		{"text plain markdown format", map[string]any{"url": base + "/text", "format": "markdown"}},
		{"html markdown", map[string]any{"url": base + "/html", "format": "markdown"}},
		{"html text", map[string]any{"url": base + "/html", "format": "text"}},
		{"html raw", map[string]any{"url": base + "/html", "format": "html"}},
		{"html default format", map[string]any{"url": base + "/html"}},
		{"meta markdown", map[string]any{"url": base + "/meta", "format": "markdown"}},
		{"meta text", map[string]any{"url": base + "/meta", "format": "text"}},
		{"long archived page", map[string]any{"url": base + "/long", "format": "markdown"}},
		{"image rejected", map[string]any{"url": base + "/img"}},
		{"binary rejected", map[string]any{"url": base + "/bin", "timeout": float64(10)}},
		{"404", map[string]any{"url": base + "/nonexistent", "timeout": float64(10)}},
		{"cloudflare retry", map[string]any{"url": base + "/challenge"}},
		{"too large", map[string]any{"url": base + "/big", "timeout": float64(10)}},
		{"negative timeout", map[string]any{"url": base + "/text", "timeout": float64(-5)}},
		{"huge timeout clamped", map[string]any{"url": base + "/text", "timeout": float64(9999)}},
		{"unknown format", map[string]any{"url": base + "/text", "format": "pdf"}},
		{"redacted", map[string]any{"url": base + "/secret"}},
		{"redaction off", map[string]any{"url": base + "/secret", "redact_secrets": false}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsJSON := runTS(t, root, "dist/tools/webfetch.js", "webfetchTool", testCase.args, nil, "")

			encoded, err := json.Marshal(testCase.args)
			if err != nil {
				t.Fatalf("args: %v", err)
			}
			var args map[string]any
			if err := json.Unmarshal(encoded, &args); err != nil {
				t.Fatalf("args: %v", err)
			}
			goResult, err := Tool.Handler(runtimeCtx(), args)
			if err != nil {
				t.Fatalf("go handler: %v", err)
			}
			goJSON, err := json.Marshal(goResult)
			if err != nil {
				t.Fatalf("go marshal: %v", err)
			}

			var tsValue, goValue any
			if err := json.Unmarshal([]byte(tsJSON), &tsValue); err != nil {
				t.Fatalf("ts result is not JSON: %v\n%s", err, tsJSON)
			}
			if err := json.Unmarshal(goJSON, &goValue); err != nil {
				t.Fatalf("go result is not JSON: %v", err)
			}
			if !reflect.DeepEqual(tsValue, goValue) {
				t.Fatalf("TS and Go disagree\n TS: %s\n Go: %s", tsJSON, goJSON)
			}
		})
	}
}

// runtimeCtx is the plain in-process call context (process cwd/env).
func runtimeCtx() context.Context {
	return context.Background()
}

// toolDefinitionJSON asks the TS module for the listing metadata.
func toolDefinitionJSON(t *testing.T, root, toolPath, exportName string) string {
	t.Helper()
	script := `const path = process.argv[2];
const name = process.argv[3];
import(path).then((mod) => {
  const tool = mod[name];
  process.stdout.write(JSON.stringify({ name: tool.name, annotations: tool.annotations, description: tool.description, inputSchema: tool.inputSchema }));
}, (error) => { console.error(error); process.exit(1); });
`
	runner := filepath.Join(t.TempDir(), "definition.mjs")
	if err := os.WriteFile(runner, []byte(script), 0o600); err != nil {
		t.Fatalf("runner: %v", err)
	}
	command := exec.Command("node", runner, filepath.Join(root, toolPath), exportName)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	return stdout.String()
}

// TestParityDefinition compares the tool's listing JSON byte for byte.
func TestParityDefinition(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "webfetch.js")); err != nil {
		t.Skip("dist/tools/webfetch.js missing; run pnpm build")
	}
	want := toolDefinitionJSON(t, root, "dist/tools/webfetch.js", "webfetchTool")
	got, err := json.Marshal(Tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != want {
		t.Fatalf("tool definition differs\n TS: %s\n Go: %s", want, got)
	}
}
