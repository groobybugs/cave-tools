//go:build parity

package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/testenv"
	"github.com/groobybugs/cave-tools/go/internal/types"
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
	command.Env = append(testenv.Environ(),
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

	type parityCase struct {
		name string
		args map[string]any
	}
	cases := []parityCase{
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
		{"list markers markdown", map[string]any{"url": base + "/lists", "format": "markdown"}},
		{"list markers text", map[string]any{"url": base + "/lists", "format": "text"}},
		{"prototype tags markdown", map[string]any{"url": base + "/prototype", "format": "markdown"}},
		{"prototype tags text", map[string]any{"url": base + "/prototype", "format": "text"}},
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
		{"redirect chain 12", map[string]any{"url": base + "/chain?n=12", "timeout": float64(5)}},
		{"redirect chain 20", map[string]any{"url": base + "/chain?n=20", "timeout": float64(5)}},
		{"redirect chain 21", map[string]any{"url": base + "/chain?n=21", "timeout": float64(5)}},
		{"forced gzip", map[string]any{"url": base + "/gzip", "format": "text"}},
		{"forced zlib deflate", map[string]any{"url": base + "/zlib", "format": "text"}},
		{"forced raw deflate", map[string]any{"url": base + "/rawdeflate", "format": "text"}},
		{"gzip decoded too large", map[string]any{"url": base + "/gzip-big", "timeout": float64(10)}},
		{"gzip lying content length", map[string]any{"url": base + "/gzip-lying-cl", "timeout": float64(10)}},
		{"gzip empty body", map[string]any{"url": base + "/gzip-empty", "format": "text"}},
		{"gzip one byte body", map[string]any{"url": base + "/gzip-onebyte", "format": "text"}},
		{"gzip corrupt body", map[string]any{"url": base + "/gzip-corrupt", "format": "text"}},
		{"gzip truncated body", map[string]any{"url": base + "/gzip-truncated", "format": "text"}},
		{"gzip checksum body", map[string]any{"url": base + "/gzip-checksum", "format": "text"}},
		{"url singleton array", map[string]any{"url": []any{base + "/text"}, "format": "text"}},
		{"url empty array", map[string]any{"url": []any{}}},
		{"url singleton null", map[string]any{"url": []any{nil}}},
		{"url number", map[string]any{"url": float64(42)}},
		{"url boolean", map[string]any{"url": true}},
		{"url object", map[string]any{"url": map[string]any{}}},
		{"url multi array", map[string]any{"url": []any{"a", "b"}}},
		{"timeout singleton one times out", map[string]any{"url": base + "/sleep?ms=1100", "timeout": []any{float64(1)}}},
		{"timeout empty array succeeds", map[string]any{"url": base + "/sleep?ms=1100", "timeout": []any{}}},
		{"timeout singleton null succeeds", map[string]any{"url": base + "/sleep?ms=1100", "timeout": []any{nil}}},
	}

	utf8Names := make([]string, 0, len(malformedUTF8Cases))
	for name := range malformedUTF8Cases {
		utf8Names = append(utf8Names, name)
	}
	sort.Strings(utf8Names)
	for _, name := range utf8Names {
		cases = append(cases, parityCase{
			name: "utf8 " + name,
			args: map[string]any{"url": base + "/utf8?case=" + url.QueryEscape(name), "format": "text"},
		})
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
	command.Env = testenv.Environ()
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

// runNodeScript executes a small Node program with extra argv entries.
func runNodeScript(t *testing.T, script string, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.mjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("probe: %v", err)
	}
	command := exec.Command("node", append([]string{path}, args...)...)
	command.Env = testenv.Environ()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node probe: %v\n%s", err, stderr.String())
	}
	return stdout.String()
}

// TestParityCoercionMatrix compares jsNumber/jsString with the real Node
// Number()/String() coercions for every array and scalar JSON shape.
func TestParityCoercionMatrix(t *testing.T) {
	values := []any{
		nil,
		true,
		false,
		float64(0),
		math.Copysign(0, -1),
		float64(1.5),
		float64(1e21),
		float64(1e-7),
		"",
		" ",
		"0x10",
		"Infinity",
		"-Infinity",
		"1e3",
		"12.5",
		[]any{},
		[]any{nil},
		[]any{[]any{}},
		[]any{float64(7)},
		[]any{"7"},
		[]any{"1e3"},
		[]any{float64(1), float64(2)},
		[]any{[]any{float64(1), float64(2)}},
		[]any{map[string]any{}},
		[]any{"a", nil, "b"},
		map[string]any{},
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("values: %v", err)
	}
	script := `const values = JSON.parse(process.argv[2]);
const out = values.map((value) => {
  const number = Number(value);
  return {
    number: Number.isNaN(number) ? "NaN" : String(number),
    string: String(value),
  };
});
process.stdout.write(JSON.stringify(out));`
	output := runNodeScript(t, script, string(encoded))

	var expected []struct {
		Number string `json:"number"`
		String string `json:"string"`
	}
	if err := json.Unmarshal([]byte(output), &expected); err != nil {
		t.Fatalf("node output: %v\n%s", err, output)
	}
	if len(expected) != len(values) {
		t.Fatalf("node results = %d, values = %d", len(expected), len(values))
	}
	for index, value := range values {
		if got := jsNumberToString(jsNumber(value)); got != expected[index].Number {
			t.Fatalf("jsNumber(%v) string = %q, Node = %q", value, got, expected[index].Number)
		}
		if value == nil {
			continue // String(null) is outside the internal jsString contract
		}
		if got := jsString(value); got != expected[index].String {
			t.Fatalf("jsString(%v) = %q, Node = %q", value, got, expected[index].String)
		}
	}
}

// TestParityRedirectHeaderSemantics compares net/http redirects with a real
// Node fetch: same-origin keeps Authorization, a cross-origin hop drops it,
// and ordinary headers survive both.
func TestParityRedirectHeaderSemantics(t *testing.T) {
	type observed struct {
		method        string
		authorization string
		custom        string
		language      string
		userAgent     string
	}
	type capture struct {
		mu       sync.Mutex
		observed []observed
	}
	record := func(c *capture, request *http.Request) {
		_ = request.Body.Close()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.observed = append(c.observed, observed{
			method:        request.Method,
			authorization: request.Header.Get("Authorization"),
			custom:        request.Header.Get("X-Custom"),
			language:      request.Header.Get("Accept-Language"),
			userAgent:     request.Header.Get("User-Agent"),
		})
	}
	take := func(c *capture) []observed {
		c.mu.Lock()
		defer c.mu.Unlock()
		return append([]observed(nil), c.observed...)
	}

	headers := map[string]string{
		"Authorization":   "Bearer sekret",
		"X-Custom":        "yes",
		"Accept-Language": "en-US,en;q=0.9",
		"User-Agent":      "ua-test",
	}
	script := `const url = process.argv[2];
fetch(url, {
  headers: {
    Authorization: "Bearer sekret",
    "X-Custom": "yes",
    "Accept-Language": "en-US,en;q=0.9",
    "User-Agent": "ua-test",
  },
}).then(async (response) => {
  await response.text();
  process.stdout.write(JSON.stringify({ ok: true, status: response.status }));
}).catch((error) => {
  process.stdout.write(JSON.stringify({ ok: false, error: String(error) }));
});`

	t.Run("same origin", func(t *testing.T) {
		captured := &capture{}
		mux := http.NewServeMux()
		mux.HandleFunc("/start", func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, "/final", http.StatusFound)
		})
		mux.HandleFunc("/final", func(w http.ResponseWriter, request *http.Request) {
			record(captured, request)
			w.Header().Set("content-type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		output := runNodeScript(t, script, server.URL+"/start")
		if !bytes.Contains([]byte(output), []byte(`"ok":true`)) {
			t.Fatalf("node fetch failed: %s", output)
		}
		nodeCapture := take(captured)
		if _, err := fetchBounded(context.Background(), server.URL+"/start", headers, 5); err != nil {
			t.Fatalf("fetchBounded: %v", err)
		}
		goCapture := take(captured)[len(nodeCapture):]
		if len(nodeCapture) != 1 || len(goCapture) != 1 {
			t.Fatalf("captures node=%d go=%d", len(nodeCapture), len(goCapture))
		}
		if nodeCapture[0] != goCapture[0] {
			t.Fatalf("same-origin headers differ\n node: %+v\n   go: %+v", nodeCapture[0], goCapture[0])
		}
		if got := goCapture[0].authorization; got != "Bearer sekret" {
			t.Fatalf("Authorization = %q", got)
		}
	})

	t.Run("cross origin", func(t *testing.T) {
		captured := &capture{}
		final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			record(captured, request)
			w.Header().Set("content-type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		}))
		t.Cleanup(final.Close)
		start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, final.URL+"/final", http.StatusFound)
		}))
		t.Cleanup(start.Close)

		output := runNodeScript(t, script, start.URL+"/start")
		if !bytes.Contains([]byte(output), []byte(`"ok":true`)) {
			t.Fatalf("node fetch failed: %s", output)
		}
		nodeCapture := take(captured)
		if _, err := fetchBounded(context.Background(), start.URL+"/start", headers, 5); err != nil {
			t.Fatalf("fetchBounded: %v", err)
		}
		goCapture := take(captured)[len(nodeCapture):]
		if len(nodeCapture) != 1 || len(goCapture) != 1 {
			t.Fatalf("captures node=%d go=%d", len(nodeCapture), len(goCapture))
		}
		if nodeCapture[0] != goCapture[0] {
			t.Fatalf("cross-origin headers differ\n node: %+v\n   go: %+v", nodeCapture[0], goCapture[0])
		}
		if got := goCapture[0].authorization; got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		if got := goCapture[0].custom; got != "yes" {
			t.Fatalf("X-Custom = %q", got)
		}
	})
}

// TestParityBrotliKnownGap documents the unresolved Brotli gap: Node fetch
// decodes Content-Encoding: br, the Go standard library cannot. This test
// asserts the known difference instead of pretending parity.
func TestParityBrotliKnownGap(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "webfetch.js")); err != nil {
		t.Skip("dist/tools/webfetch.js missing; run pnpm build")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/plain")
		w.Header().Set("content-encoding", "br")
		_, _ = w.Write(brotliHelloBR)
	}))
	t.Cleanup(server.Close)
	args := map[string]any{"url": server.URL + "/br", "format": "text"}

	tsJSON := runTS(t, root, "dist/tools/webfetch.js", "webfetchTool", args, nil, "")
	var tsResult struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal([]byte(tsJSON), &tsResult); err != nil {
		t.Fatalf("ts result: %v\n%s", err, tsJSON)
	}
	if tsResult.IsError || len(tsResult.Content) != 1 || tsResult.Content[0].Text != "hello br" {
		t.Fatalf("TS brotli result = %s", tsJSON)
	}

	encoded, _ := json.Marshal(args)
	var goArgs map[string]any
	if err := json.Unmarshal(encoded, &goArgs); err != nil {
		t.Fatalf("args: %v", err)
	}
	goResult, err := Tool.Handler(context.Background(), goArgs)
	if err != nil {
		t.Fatalf("go handler: %v", err)
	}
	goJSON, err := json.Marshal(goResult)
	if err != nil {
		t.Fatalf("go marshal: %v", err)
	}
	goText := ""
	if len(goResult.Content) == 1 {
		if block, ok := goResult.Content[0].(types.TextContent); ok {
			goText = block.Text
		}
	}
	t.Logf("KNOWN BROTLI GAP: TS=%q (decoded) Go=%q (raw bytes as text) goJSON=%s", tsResult.Content[0].Text, goText, goJSON)
	if goText == tsResult.Content[0].Text {
		t.Fatalf("Go unexpectedly decoded Brotli; update the known-gap documentation and test")
	}
	if !strings.Contains(goText, "hello br") {
		t.Fatalf("raw Brotli bytes missing literal substring: %q", goText)
	}
}

// TestParityClosedPortFetchFailed compares a local transport failure with Node.
func TestParityClosedPortFetchFailed(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "webfetch.js")); err != nil {
		t.Skip("dist/tools/webfetch.js missing; run pnpm build")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	cases := []struct {
		name string
		url  string
	}{
		{"closed port", fmt.Sprintf("http://127.0.0.1:%d/", port)},
		{"invalid host", "http://does-not-exist.invalid/"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			args := map[string]any{"url": testCase.url, "timeout": float64(5)}
			tsJSON := runTS(t, root, "dist/tools/webfetch.js", "webfetchTool", args, nil, "")

			encoded, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("args: %v", err)
			}
			var goArgs map[string]any
			if err := json.Unmarshal(encoded, &goArgs); err != nil {
				t.Fatalf("args: %v", err)
			}
			goResult, err := Tool.Handler(context.Background(), goArgs)
			if err != nil {
				t.Fatalf("go handler: %v", err)
			}
			goJSON, err := json.Marshal(goResult)
			if err != nil {
				t.Fatalf("go marshal: %v", err)
			}
			if tsJSON != string(goJSON) {
				t.Fatalf("TS and Go disagree\n TS: %s\n Go: %s", tsJSON, goJSON)
			}
		})
	}
}

// TestParityURLCredentials compares Node's fetch credential rejection with Go
// for nonempty userinfo, and confirms empty userinfo is allowed without an
// Authorization header.
func TestParityURLCredentials(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "webfetch.js")); err != nil {
		t.Skip("dist/tools/webfetch.js missing; run pnpm build")
	}
	type observation struct {
		authorization string
	}
	var mu sync.Mutex
	var seen []observation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		seen = append(seen, observation{authorization: request.Header.Get("Authorization")})
		mu.Unlock()
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("echo"))
	}))
	t.Cleanup(server.Close)
	take := func() []observation {
		mu.Lock()
		defer mu.Unlock()
		return append([]observation(nil), seen...)
	}
	host := strings.TrimPrefix(server.URL, "http://")

	credentials := []string{"user:pass", "user", ":pass", "", ":"}
	for _, value := range credentials {
		t.Run("credentials "+value, func(t *testing.T) {
			url := "http://" + value + "@" + host + "/echo"
			args := map[string]any{"url": url, "format": "text"}

			before := len(take())
			tsJSON := runTS(t, root, "dist/tools/webfetch.js", "webfetchTool", args, nil, "")
			tsSeen := take()[before:]

			before = len(take())
			goResult, err := Tool.Handler(context.Background(), args)
			if err != nil {
				t.Fatalf("go handler: %v", err)
			}
			goJSON, err := json.Marshal(goResult)
			if err != nil {
				t.Fatalf("go marshal: %v", err)
			}
			goSeen := take()[before:]

			if tsJSON != string(goJSON) {
				t.Fatalf("TS and Go disagree\n TS: %s\n Go: %s", tsJSON, goJSON)
			}
			if len(tsSeen) != len(goSeen) {
				t.Fatalf("request count TS=%d Go=%d", len(tsSeen), len(goSeen))
			}
			for index := range tsSeen {
				if tsSeen[index].authorization != goSeen[index].authorization {
					t.Fatalf("Authorization TS=%q Go=%q", tsSeen[index].authorization, goSeen[index].authorization)
				}
				if tsSeen[index].authorization != "" {
					t.Fatalf("Authorization = %q, want empty", tsSeen[index].authorization)
				}
			}
		})
	}

	t.Run("credential rejection without path", func(t *testing.T) {
		url := "http://user:pass@" + host
		args := map[string]any{"url": url, "format": "text"}
		tsJSON := runTS(t, root, "dist/tools/webfetch.js", "webfetchTool", args, nil, "")
		goResult, err := Tool.Handler(context.Background(), args)
		if err != nil {
			t.Fatalf("go handler: %v", err)
		}
		goJSON, err := json.Marshal(goResult)
		if err != nil {
			t.Fatalf("go marshal: %v", err)
		}
		if tsJSON != string(goJSON) {
			t.Fatalf("TS and Go disagree\n TS: %s\n Go: %s", tsJSON, goJSON)
		}
	})
}
