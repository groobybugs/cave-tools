package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain isolates archives and stats in a temp data dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-websearch-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func testCtx(env map[string]string) context.Context {
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{Env: env}, func(ctx context.Context) context.Context {
		return ctx
	})
}

func run(t *testing.T, env map[string]string, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := Tool.Handler(testCtx(env), args)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return result
}

func resultText(t *testing.T, result types.ToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content blocks = %d", len(result.Content))
	}
	block, ok := result.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	return block.Text
}

func directPayload(text string) string {
	payload, _ := json.Marshal(map[string]any{
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	return string(payload)
}

// rewriteTransport points every request at the local fixture server while
// recording the original URL the tool asked for.
type rewriteTransport struct {
	base     *url.URL
	requests []string
	bodies   []string
}

func (t *rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, request.URL.String())
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
		request.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	t.bodies = append(t.bodies, string(body))
	clone := request.Clone(request.Context())
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	clone.Host = ""
	return http.DefaultTransport.RoundTrip(clone)
}

// installFixture swaps the package HTTP client for one that reaches handler.
func installFixture(t *testing.T, handler http.Handler) *rewriteTransport {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("server url: %v", err)
	}
	transport := &rewriteTransport{base: base}
	previous := httpClient
	httpClient = &http.Client{Transport: transport}
	t.Cleanup(func() { httpClient = previous })
	return transport
}

func TestToolDefinition(t *testing.T) {
	if Tool.Name != "cave__websearch" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if got := string(Tool.Annotations); got != `{"readOnlyHint":true,"openWorldHint":true}` {
		t.Fatalf("annotations = %s", got)
	}
	want := fmt.Sprintf("Use instead of WebSearch (Exa/Parallel); results redacted and budget-compressed. Current year: %d.", time.Now().Year())
	if Tool.Description != want {
		t.Fatalf("description = %q", Tool.Description)
	}
	if !strings.Contains(string(Tool.InputSchema), `"required":["query"]`) {
		t.Fatalf("inputSchema = %s", Tool.InputSchema)
	}
}

// Ports the parseWebsearchResponse assertions in test.mjs section 7c.
func TestParseWebsearchResponse(t *testing.T) {
	payload := directPayload("direct web result")
	if got, ok := ParseWebsearchResponse(payload); !ok || got != "direct web result" {
		t.Fatalf("direct = %q, %v", got, ok)
	}
	if got, ok := ParseWebsearchResponse("event: message\ndata: " + payload + "\n\n"); !ok || got != "direct web result" {
		t.Fatalf("sse = %q, %v", got, ok)
	}
}

func TestParseWebsearchResponseRejects(t *testing.T) {
	tests := []string{
		"",
		"not json",
		`["array"]`,
		`{"result":{}}`,
		`{"result":{"content":[]}}`,
		`{"result":{"content":[{"text":""}]}}`,
		`{"result":{"content":[{"text":1}]}}`,
		"\n\n",
	}
	for _, body := range tests {
		if got, ok := ParseWebsearchResponse(body); ok {
			t.Fatalf("ParseWebsearchResponse(%q) = %q, true", body, got)
		}
	}
}

func TestSelectProvider(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		override provider
		env      map[string]string
		want     provider
	}{
		{"override wins", "latest cave tools news", "exa", map[string]string{"CAVE_TOOLS_ENABLE_PARALLEL": "1"}, "exa"},
		{"env provider", "abc", "", map[string]string{"CAVE_TOOLS_WEBSEARCH_PROVIDER": "parallel"}, "parallel"},
		{"opencode env provider", "abc", "", map[string]string{"OPENCODE_WEBSEARCH_PROVIDER": "exa"}, "exa"},
		{"invalid env provider", "abc", "", map[string]string{"CAVE_TOOLS_WEBSEARCH_PROVIDER": "bogus"}, "exa"},
		{"parallel flag", "abc", "", map[string]string{"CAVE_TOOLS_ENABLE_PARALLEL": "yes"}, "parallel"},
		{"opencode parallel flag", "abc", "", map[string]string{"OPENCODE_ENABLE_PARALLEL": "on"}, "parallel"},
		{"exa flag", "query one", "", map[string]string{"CAVE_TOOLS_ENABLE_EXA": "TRUE"}, "exa"},
		{"opencode exa flag", "query one", "", map[string]string{"OPENCODE_ENABLE_EXA": "1"}, "exa"},
		{"experimental exa flag", "query one", "", map[string]string{"OPENCODE_EXPERIMENTAL_EXA": "On"}, "exa"},
		{"checksum even", "abc", "", map[string]string{}, "exa"},
		{"checksum odd", "hello", "", map[string]string{}, "parallel"},
		{"undefined env map", "abc", "", nil, "exa"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SelectProvider(testCtx(test.env), test.query, test.override); got != test.want {
				t.Fatalf("SelectProvider = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExaURL(t *testing.T) {
	if got := exaUrl(""); got != ExaURL {
		t.Fatalf("exaUrl(\"\") = %q", got)
	}
	if got, want := exaUrl("abc"), "https://mcp.exa.ai/mcp?exaApiKey=abc"; got != want {
		t.Fatalf("exaUrl = %q, want %q", got, want)
	}
	if got, want := exaUrl("a b+c/d~e"), "https://mcp.exa.ai/mcp?exaApiKey=a+b%2Bc%2Fd%7Ee"; got != want {
		t.Fatalf("exaUrl = %q, want %q", got, want)
	}
}

func TestParseInput(t *testing.T) {
	maxContext := 50000
	tests := []struct {
		name string
		args map[string]any
		want webSearchInput
	}{
		{
			name: "defaults",
			args: map[string]any{"query": "  x  "},
			want: webSearchInput{query: "x", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "explicit",
			args: map[string]any{
				"query": "q", "provider": "parallel", "numResults": float64(2), "livecrawl": "preferred",
				"type": "deep", "contextMaxCharacters": float64(100), "timeout": float64(3), "redact_secrets": false,
			},
			want: webSearchInput{query: "q", provider: "parallel", numResults: 2, livecrawl: "preferred", typ: "deep", contextMaxCharacters: intPtr(100), timeout: 3},
		},
		{
			name: "clamps",
			args: map[string]any{"query": "q", "numResults": float64(999), "contextMaxCharacters": float64(999999), "timeout": float64(999), "provider": "bogus", "livecrawl": "nope", "type": "bogus"},
			want: webSearchInput{query: "q", numResults: 20, livecrawl: "fallback", typ: "auto", contextMaxCharacters: &maxContext, timeout: 120, redactSecrets: true},
		},
		{
			name: "zero falls back",
			args: map[string]any{"query": "q", "numResults": float64(0), "timeout": float64(0), "contextMaxCharacters": float64(0)},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", contextMaxCharacters: intPtr(10_000), timeout: 25, redactSecrets: true},
		},
		{
			name: "string numbers",
			args: map[string]any{"query": "q", "numResults": "5", "timeout": "7"},
			want: webSearchInput{query: "q", numResults: 5, livecrawl: "fallback", typ: "auto", timeout: 7, redactSecrets: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseInput(test.args)
			if !inputsEqual(got, test.want) {
				t.Fatalf("parseInput = %+v, want %+v", got, test.want)
			}
		})
	}
}

func intPtr(value int) *int { return &value }

func inputsEqual(a, b webSearchInput) bool {
	if a.query != b.query || a.provider != b.provider || a.numResults != b.numResults ||
		a.livecrawl != b.livecrawl || a.typ != b.typ || a.timeout != b.timeout ||
		a.redactSecrets != b.redactSecrets {
		return false
	}
	if (a.contextMaxCharacters == nil) != (b.contextMaxCharacters == nil) {
		return false
	}
	return a.contextMaxCharacters == nil || *a.contextMaxCharacters == *b.contextMaxCharacters
}

func TestPositiveInt(t *testing.T) {
	tests := []struct {
		value    any
		fallback int
		max      int
		want     int
	}{
		{float64(5), 8, 20, 5},
		{float64(0), 8, 20, 8},
		{float64(-3), 8, 20, 1},
		{float64(99), 8, 20, 20},
		{"12", 8, 20, 12},
		{"abc", 8, 20, 8},
		{nil, 8, 20, 8},
		{2.9, 8, 20, 2},
	}
	for _, test := range tests {
		if got := positiveInt(test.value, test.fallback, test.max); got != test.want {
			t.Fatalf("positiveInt(%v) = %d, want %d", test.value, got, test.want)
		}
	}
}

// Ports test.mjs section 7c: the handler posts a tools/call to Exa and returns
// the first text block.
func TestHandlerExa(t *testing.T) {
	transport := installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(directPayload("direct web result")))
	}))

	result := run(t, map[string]string{"EXA_API_KEY": "test-key"}, map[string]any{
		"query": "latest cave tools news", "provider": "exa", "numResults": float64(2), "timeout": float64(1),
	})
	if result.IsError {
		t.Fatalf("isError: %s", resultText(t, result))
	}
	if got := resultText(t, result); !strings.Contains(got, "direct web result") {
		t.Fatalf("text = %q", got)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %v", transport.requests)
	}
	if !strings.Contains(transport.requests[0], "mcp.exa.ai") {
		t.Fatalf("url = %q", transport.requests[0])
	}
	if !strings.Contains(transport.requests[0], "exaApiKey=test-key") {
		t.Fatalf("url = %q", transport.requests[0])
	}
	if !strings.Contains(transport.bodies[0], "web_search_exa") {
		t.Fatalf("body = %q", transport.bodies[0])
	}
	wantBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"web_search_exa","arguments":{"query":"latest cave tools news","type":"auto","numResults":2,"livecrawl":"fallback"}}}`
	if transport.bodies[0] != wantBody {
		t.Fatalf("body = %q, want %q", transport.bodies[0], wantBody)
	}
}

func TestHandlerParallel(t *testing.T) {
	transport := installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte("event: message\ndata: " + directPayload("parallel result") + "\n\n"))
	}))

	result := run(t, map[string]string{"PARALLEL_API_KEY": "pk"}, map[string]any{
		"query": "abc", "provider": "parallel", "contextMaxCharacters": float64(250), "timeout": float64(1),
	})
	if result.IsError {
		t.Fatalf("isError: %s", resultText(t, result))
	}
	if got := resultText(t, result); !strings.Contains(got, "parallel result") {
		t.Fatalf("text = %q", got)
	}
	if !strings.Contains(transport.requests[0], "search.parallel.ai") {
		t.Fatalf("url = %q", transport.requests[0])
	}
	wantBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"web_search","arguments":{"objective":"abc","search_queries":["abc"],"session_id":"` + checksum("abc") + `"}}}`
	if transport.bodies[0] != wantBody {
		t.Fatalf("body = %q, want %q", transport.bodies[0], wantBody)
	}
	if transport.requests[0] != ParallelURL {
		t.Fatalf("url = %q", transport.requests[0])
	}
}

func TestHandlerNoResults(t *testing.T) {
	installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"content":[]}}`))
	}))
	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa"})
	if result.IsError {
		t.Fatalf("isError: %s", resultText(t, result))
	}
	if got := resultText(t, result); got != NoResults {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerHTTPError(t *testing.T) {
	installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa"})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	want := "Unable to search the web for abc: websearch request failed with HTTP 500"
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestHandlerResponseTooLarge(t *testing.T) {
	installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", MaxResponseBytes+1)))
	}))
	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa"})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	want := "Unable to search the web for abc: websearch response exceeded 262144 bytes"
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestHandlerTimeout(t *testing.T) {
	installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(directPayload("late")))
	}))
	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(1)})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	want := "Unable to search the web for abc: This operation was aborted"
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestHandlerMissingQuery(t *testing.T) {
	result := run(t, map[string]string{}, map[string]any{})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, result); got != "Error: query is required" {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerRedaction(t *testing.T) {
	secret := "Authorization: Bearer zbcd1234efgh5678ijkl9012mnop3456"
	installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(directPayload(secret)))
	}))

	redacted := resultText(t, run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa"}))
	if strings.Contains(redacted, "zbcd1234efgh5678ijkl9012mnop3456") {
		t.Fatalf("secret leaked: %q", redacted)
	}
	raw := resultText(t, run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "redact_secrets": false}))
	if !strings.Contains(raw, "zbcd1234efgh5678ijkl9012mnop3456") {
		t.Fatalf("redact_secrets=false still redacted: %q", raw)
	}
}
