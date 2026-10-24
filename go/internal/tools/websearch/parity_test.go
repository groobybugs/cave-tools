//go:build parity

package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
)

// Differential harness: runs the TypeScript handler from dist/ and the Go
// handler on the same arguments and environment, then compares the full
// ToolResult JSON.
//
//	pnpm build && cd go && go test -tags parity -run TestParity ./internal/tools/websearch/

const tsRunner = `import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const toolPath = process.argv[2];
const exportName = process.argv[3];
const realFetch = globalThis.fetch;
if (process.env.PARITY_FETCH_BASE) {
  const base = new URL(process.env.PARITY_FETCH_BASE);
  globalThis.fetch = (url, init) => {
    const target = new URL(String(url));
    if (process.env.PARITY_CAPTURE_FILE) {
      appendFileSync(
        process.env.PARITY_CAPTURE_FILE,
        JSON.stringify({ url: String(url), body: String((init && init.body) || "") }) + "\n",
      );
    }
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

// capturedRequest is one fetch the TS handler performed.
type capturedRequest struct {
	URL  string `json:"url"`
	Body string `json:"body"`
}

// runTS returns the TS ToolResult JSON plus every request the handler made.
func runTS(t *testing.T, root string, args map[string]any, env map[string]string, fetchBase string) (string, []capturedRequest) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "run-tool.mjs")
	if err := os.WriteFile(runner, []byte(tsRunner), 0o600); err != nil {
		t.Fatalf("runner: %v", err)
	}
	captureFile := filepath.Join(dir, "capture.jsonl")
	command := exec.Command("node", runner, filepath.Join(root, "dist/tools/websearch.js"), "websearchTool")
	command.Stdin = bytes.NewReader(encoded)
	command.Env = append(os.Environ(),
		"PARITY_FETCH_BASE="+fetchBase,
		"PARITY_CAPTURE_FILE="+captureFile,
	)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	requests := make([]capturedRequest, 0, 1)
	if data, err := os.ReadFile(captureFile); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var request capturedRequest
			if err := json.Unmarshal([]byte(line), &request); err != nil {
				t.Fatalf("capture line: %v", err)
			}
			requests = append(requests, request)
		}
	}
	return stdout.String(), requests
}

// longResult is a search answer long enough for applyBudget + archiveHandle.
func longResult() string {
	var out strings.Builder
	for i := 0; i < 200; i++ {
		out.WriteString("result line " + strconv.Itoa(i) + "\n")
	}
	return out.String()
}

// TestParityHandler compares TS and Go ToolResult JSON for every case.
func TestParityHandler(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "websearch.js")); err != nil {
		t.Skip("dist/tools/websearch.js missing; run pnpm build")
	}

	type fixture struct {
		status      int
		contentType string
		body        string
		sleep       time.Duration
	}
	cases := []struct {
		name    string
		args    map[string]any
		env     map[string]string
		fixture fixture
	}{
		{
			name: "missing query",
			args: map[string]any{},
		},
		{
			name:    "exa direct",
			args:    map[string]any{"query": "latest cave tools news", "provider": "exa", "numResults": float64(2), "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("direct web result")},
		},
		{
			name:    "exa sse",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "text/event-stream", body: "event: message\ndata: " + directPayload("sse result") + "\n\n"},
		},
		{
			name:    "parallel",
			args:    map[string]any{"query": "abc", "provider": "parallel", "timeout": float64(2)},
			env:     map[string]string{"PARALLEL_API_KEY": "pk"},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("parallel result")},
		},
		{
			name:    "no results",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: `{"result":{"content":[]}}`},
		},
		{
			name:    "non json payload",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "text/plain", body: "not json at all"},
		},
		{
			name:    "empty text then second block",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: `{"result":{"content":[{"text":""},{"text":"second"}]}}`},
		},
		{
			name:    "http error",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 500, body: "boom"},
		},
		{
			name:    "response too large",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: strings.Repeat("x", MaxResponseBytes+1)},
		},
		{
			name:    "redacted",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("Authorization: Bearer zbcd1234efgh5678ijkl9012mnop3456")},
		},
		{
			name:    "redaction off",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2), "redact_secrets": false},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("Authorization: Bearer zbcd1234efgh5678ijkl9012mnop3456")},
		},
		{
			name:    "query trimmed",
			args:    map[string]any{"query": "  abc  ", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("trimmed")},
		},
		{
			name:    "context max characters",
			args:    map[string]any{"query": "abc", "provider": "exa", "contextMaxCharacters": float64(1234), "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("ctx")},
		},
		{
			name:    "null context max characters",
			args:    map[string]any{"query": "abc", "provider": "exa", "contextMaxCharacters": nil, "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("null context")},
		},
		{
			name:    "null timeout",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": nil},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("null timeout")},
		},
		{
			name:    "null livecrawl and type",
			args:    map[string]any{"query": "abc", "provider": "exa", "livecrawl": nil, "type": nil, "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("null livecrawl")},
		},
		{
			name:    "livecrawl preferred deep",
			args:    map[string]any{"query": "abc", "provider": "exa", "livecrawl": "preferred", "type": "deep", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("preferred deep")},
		},
		{
			name:    "null num results",
			args:    map[string]any{"query": "abc", "provider": "exa", "numResults": nil, "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("null numResults")},
		},
		{
			name:    "null provider",
			args:    map[string]any{"query": "abc", "provider": nil, "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("null provider")},
		},
		{
			name:    "null redact secrets",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2), "redact_secrets": nil},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("Authorization: Bearer zbcd1234efgh5678ijkl9012mnop3456")},
		},
		{
			name: "null query",
			args: map[string]any{"query": nil},
		},
		{
			// JSON.stringify leaves U+2028/U+2029 raw and only escapes C0
			// controls; Go's encoder must match byte for byte.
			name:    "jsonrpc string escaping",
			args:    map[string]any{"query": "line\u2028sep \"q\" \\\n\t\u0001 </&>", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("escaped")},
		},
		{
			name:    "jsonrpc string escaping parallel",
			args:    map[string]any{"query": "line\u2028sep \"q\" \\\n\t\u0001 </&>", "provider": "parallel", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("escaped parallel")},
		},
		{
			name:    "env provider",
			args:    map[string]any{"query": "abc", "timeout": float64(2)},
			env:     map[string]string{"CAVE_TOOLS_WEBSEARCH_PROVIDER": "parallel"},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("env provider")},
		},
		{
			name:    "checksum default",
			args:    map[string]any{"query": "hello", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("checksum")},
		},
		{
			name:    "api keys from env",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			env:     map[string]string{"EXA_API_KEY": "a b+c/d"},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("exa key")},
		},
		{
			name:    "long archived result",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload(longResult())},
		},
		{
			name:    "timeout",
			args:    map[string]any{"query": "abc", "provider": "exa", "timeout": float64(1)},
			fixture: fixture{status: 200, contentType: "application/json", body: directPayload("late"), sleep: 2 * time.Second},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := testCase.fixture
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if fixture.sleep > 0 {
					time.Sleep(fixture.sleep)
				}
				if fixture.contentType != "" {
					w.Header().Set("content-type", fixture.contentType)
				}
				if fixture.status != 0 {
					w.WriteHeader(fixture.status)
				}
				if fixture.body != "" {
					_, _ = io.WriteString(w, fixture.body)
				}
			})

			transport := installFixture(t, handler)
			tsURL := "http://" + transport.base.Host

			tsJSON, tsRequests := runTS(t, root, testCase.args, testCase.env, tsURL)

			encoded, err := json.Marshal(testCase.args)
			if err != nil {
				t.Fatalf("args: %v", err)
			}
			var args map[string]any
			if err := json.Unmarshal(encoded, &args); err != nil {
				t.Fatalf("args: %v", err)
			}
			env := runtime.EnvFromEnviron(os.Environ())
			for key, value := range testCase.env {
				env[key] = value
			}
			ctx := runtime.RunWithContext(context.Background(), &runtime.CallContext{Env: env}, func(ctx context.Context) context.Context {
				return ctx
			})
			goResult, err := Tool.Handler(ctx, args)
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

			// The outgoing JSON-RPC request must match too: null-vs-missing
			// arguments only show up in the body, not in the ToolResult.
			if len(tsRequests) != len(transport.requests) {
				t.Fatalf("request count TS=%d Go=%d\n TS: %+v\n Go: %+v", len(tsRequests), len(transport.requests), tsRequests, transport.requests)
			}
			for index := range tsRequests {
				if tsRequests[index].URL != transport.requests[index] {
					t.Fatalf("request URL %d\n TS: %s\n Go: %s", index, tsRequests[index].URL, transport.requests[index])
				}
				if tsRequests[index].Body != transport.bodies[index] {
					t.Fatalf("request body %d\n TS: %s\n Go: %s", index, tsRequests[index].Body, transport.bodies[index])
				}
			}
		})
	}
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
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "websearch.js")); err != nil {
		t.Skip("dist/tools/websearch.js missing; run pnpm build")
	}
	want := toolDefinitionJSON(t, root, "dist/tools/websearch.js", "websearchTool")
	got, err := json.Marshal(Tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != want {
		t.Fatalf("tool definition differs\n TS: %s\n Go: %s", want, got)
	}
}
