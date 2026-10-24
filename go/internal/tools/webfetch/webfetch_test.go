package webfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestMain isolates archives and stats in a temp data dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-tools-go-webfetch-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func testCtx() context.Context {
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{Env: map[string]string{}}, func(ctx context.Context) context.Context {
		return ctx
	})
}

func run(t *testing.T, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := Tool.Handler(testCtx(), args)
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

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return string(data)
}

// fixturePage is the /html page from test.mjs section 12.
func fixturePage(t *testing.T) string { return readFixture(t, "page.html") }

// fixtureMeta is the /meta page from test.mjs section 12.
func fixtureMeta(t *testing.T) string { return readFixture(t, "meta.html") }

// newFixtureServer mirrors the local HTTP server the TS test spins up.
func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/text", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("hello plain text"))
	})
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixturePage(t)))
	})
	mux.HandleFunc("/meta", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixtureMeta(t)))
	})
	mux.HandleFunc("/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("Authorization: Bearer zbcd1234efgh5678ijkl9012mnop3456"))
	})
	mux.HandleFunc("/img", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03})
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 6*1024*1024)))
	})
	mux.HandleFunc("/long", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		var page strings.Builder
		page.WriteString("<html><body>")
		for i := 0; i < 200; i++ {
			page.WriteString("<p>" + strings.Repeat("word ", 20) + "</p>")
		}
		page.WriteString("</body></html>")
		_, _ = w.Write([]byte(page.String()))
	})
	mux.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "opencode" {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("challenge"))
			return
		}
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("honest bot"))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("late"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestToolDefinition(t *testing.T) {
	if Tool.Name != "cave__webfetch" {
		t.Fatalf("name = %q", Tool.Name)
	}
	if got := string(Tool.Annotations); got != `{"readOnlyHint":true,"openWorldHint":true}` {
		t.Fatalf("annotations = %s", got)
	}
	const wantDescription = "Use instead of WebFetch. Returns markdown/text/html; non-text content rejected, non-2xx is an error. Follows redirects; any http(s) URL incl. localhost."
	if Tool.Description != wantDescription {
		t.Fatalf("description = %q", Tool.Description)
	}
	if !strings.Contains(string(Tool.InputSchema), `"required":["url"]`) {
		t.Fatalf("inputSchema = %s", Tool.InputSchema)
	}
}

func TestAcceptFor(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{"markdown", "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"},
		{"text", "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"},
		{"html", "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"},
	}
	for _, test := range tests {
		if got := acceptFor(test.format); got != test.want {
			t.Fatalf("acceptFor(%q) = %q", test.format, got)
		}
	}
}

func TestParseInput(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want webFetchInput
	}{
		{
			name: "defaults",
			args: map[string]any{"url": "  https://example.com/x  "},
			want: webFetchInput{url: "https://example.com/x", format: "markdown", timeout: 30, redactSecrets: true},
		},
		{
			name: "explicit",
			args: map[string]any{"url": "http://x", "format": "html", "timeout": float64(5), "redact_secrets": false},
			want: webFetchInput{url: "http://x", format: "html", timeout: 5, redactSecrets: false},
		},
		{
			name: "timeout clamped",
			args: map[string]any{"url": "http://x", "timeout": float64(9999)},
			want: webFetchInput{url: "http://x", format: "markdown", timeout: 120, redactSecrets: true},
		},
		{
			name: "timeout truncated",
			args: map[string]any{"url": "http://x", "timeout": 2.9},
			want: webFetchInput{url: "http://x", format: "markdown", timeout: 2, redactSecrets: true},
		},
		{
			name: "negative timeout falls back",
			args: map[string]any{"url": "http://x", "timeout": float64(-5)},
			want: webFetchInput{url: "http://x", format: "markdown", timeout: 30, redactSecrets: true},
		},
		{
			name: "string timeout",
			args: map[string]any{"url": "http://x", "timeout": "7"},
			want: webFetchInput{url: "http://x", format: "markdown", timeout: 7, redactSecrets: true},
		},
		{
			name: "unknown format falls back",
			args: map[string]any{"url": "http://x", "format": "pdf"},
			want: webFetchInput{url: "http://x", format: "markdown", timeout: 30, redactSecrets: true},
		},
		{
			// JSON-decode can hand a map where TS sees a non-string value.
			name: "non-string format does not panic",
			args: map[string]any{"url": "http://x", "format": map[string]any{"a": 1}, "redact_secrets": map[string]any{}},
			want: webFetchInput{url: "http://x", format: "markdown", timeout: 30, redactSecrets: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseInput(test.args); got != test.want {
				t.Fatalf("parseInput = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestIsTextualMime(t *testing.T) {
	tests := []struct {
		mime string
		want bool
	}{
		{"", true},
		{"text/plain", true},
		{"text/html", true},
		{"application/json", true},
		{"application/ld+json", true},
		{"application/xml", true},
		{"image/svg+xml", true}, // endsWith("+xml") wins in TS too
		{"application/atom+xml", true},
		{"application/javascript", true},
		{"application/x-javascript", true},
		{"image/png", false},
		{"application/octet-stream", false},
	}
	for _, test := range tests {
		if got := isTextualMime(test.mime); got != test.want {
			t.Fatalf("isTextualMime(%q) = %v", test.mime, got)
		}
	}
}

func TestDecodeEntities(t *testing.T) {
	tests := []struct{ input, want string }{
		{"a&nbsp;b", "a b"},
		{"&amp;", "&"},
		{"&AMP;", "&"},
		{"&lt;b&gt;", "<b>"},
		{"&quot;q&apos;", `"q'`},
		{"&#39;", "'"},
		{"&#65;&#x42;", "AB"},
		{"&#X42;", "B"},
		{"&#0;", "\x00"},
		{"&#1114112;", ""},
		{"&#x110000;", ""},
		{"&#xD800;", "\uFFFD"},
		{"&#x;", "&#x;"},
		{"&unknown;", "&unknown;"},
	}
	for _, test := range tests {
		if got := decodeEntities(test.input); got != test.want {
			t.Fatalf("decodeEntities(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestParseAttrs(t *testing.T) {
	got := parseAttrs(`href="/x" class='c' disabled data-x=1 DATA-Y="2"`)
	want := map[string]string{"href": "/x", "class": "c", "disabled": "", "data-x": "1", "data-y": "2"}
	if len(got) != len(want) {
		t.Fatalf("attrs = %v", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("attrs[%q] = %q, want %q", key, got[key], value)
		}
	}
}

func TestExtractTextFromHtml(t *testing.T) {
	if got, want := extractTextFromHtml(fixturePage(t)), "Title\nHello link world\nSecond paragraph."; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if got, want := extractTextFromHtml(fixtureMeta(t)), "TestBody Title\nBody text survives.\nalphabetacode line 1\nline 2"; got != want {
		t.Fatalf("meta text = %q, want %q", got, want)
	}
}

func TestConvertHtmlToMarkdown(t *testing.T) {
	if got, want := convertHtmlToMarkdown(fixturePage(t)), "# Title\nHello [link](/x) world\nSecond paragraph."; got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
	wantMeta := "Test\n\n# Body Title\nBody text survives.\nalpha\nbeta\n\n---\n\n```\ncode line 1\nline 2\n```"
	if got := convertHtmlToMarkdown(fixtureMeta(t)); got != wantMeta {
		t.Fatalf("meta markdown = %q, want %q", got, wantMeta)
	}
}

func TestConvertHtmlToMarkdownFeatures(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"emphasis", "<p>a <em>b</em> <strong>c</strong> <b>d</b> <i>e</i></p>", "a *b* **c** **d** *e*"},
		// li and blockquote are BLOCK_TAGS, so their marker branches below are
		// unreachable: TS emits plain block text for both. Ported as-is.
		{"unordered list", "<ul><li>one</li><li>two</li></ul>", "one\ntwo"},
		{"ordered list", "<ol><li>one</li><li>two</li></ol>", "one\ntwo"},
		{"nested list", "<ul><li>a<ul><li>b</li></ul></li></ul>", "a\nb"},
		{"blockquote", "<blockquote>q</blockquote>", "q"},
		{"inline code", "<p>run <code>x</code></p>", "run `x`"},
		{"links", `<p><a href="/a">a</a> and <a href="/b">b</a></p>`, "[a](/a) and [b](/b)"},
		{"nested skip tags", "<noscript><style>x</style>hidden</noscript><p>shown</p>", "shown"},
		{"unknown tag inner text", "<custom>kept</custom>", "kept"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := convertHtmlToMarkdown(test.input); got != test.want {
				t.Fatalf("markdown = %q, want %q", got, test.want)
			}
		})
	}
}

// Ports test.mjs section 12: handler behavior against a local HTTP server.
func TestHandlerRejectsMissingAndBadURL(t *testing.T) {
	noURL := run(t, map[string]any{"format": "markdown"})
	if !noURL.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, noURL); !strings.Contains(got, "url is required") {
		t.Fatalf("text = %q", got)
	}

	badScheme := run(t, map[string]any{"url": "ftp://example.com"})
	if !badScheme.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, badScheme); !strings.Contains(got, "URL must start with http:// or https://") {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerFormats(t *testing.T) {
	server := newFixtureServer(t)
	base := server.URL

	textRes := run(t, map[string]any{"url": base + "/text", "format": "text"})
	if textRes.IsError {
		t.Fatalf("isError: %s", resultText(t, textRes))
	}
	if got := resultText(t, textRes); !strings.Contains(got, "hello plain text") {
		t.Fatalf("text = %q", got)
	}

	mdRes := run(t, map[string]any{"url": base + "/html", "format": "markdown"})
	if mdRes.IsError {
		t.Fatalf("isError: %s", resultText(t, mdRes))
	}
	md := resultText(t, mdRes)
	if md != "# Title\nHello [link](/x) world\nSecond paragraph." {
		t.Fatalf("markdown = %q", md)
	}

	htmlText := run(t, map[string]any{"url": base + "/html", "format": "text"})
	if got, want := resultText(t, htmlText), "Title\nHello link world\nSecond paragraph."; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}

	rawHTML := run(t, map[string]any{"url": base + "/html", "format": "html"})
	if got := resultText(t, rawHTML); got != fixturePage(t) {
		t.Fatalf("raw html = %q", got)
	}

	metaMd := run(t, map[string]any{"url": base + "/meta", "format": "markdown"})
	if metaMd.IsError {
		t.Fatalf("isError: %s", resultText(t, metaMd))
	}
	metaText := resultText(t, metaMd)
	for _, want := range []string{"Body Title", "Body text survives.", "```", "code line 1", "alpha\nbeta"} {
		if !strings.Contains(metaText, want) {
			t.Fatalf("meta markdown missing %q: %q", want, metaText)
		}
	}
	if !strings.Contains(metaText, "\n---\n") && !strings.HasPrefix(metaText, "---") {
		t.Fatalf("meta markdown missing rule: %q", metaText)
	}

	metaPlain := run(t, map[string]any{"url": base + "/meta", "format": "text"})
	if metaPlain.IsError {
		t.Fatalf("isError: %s", resultText(t, metaPlain))
	}
	plain := resultText(t, metaPlain)
	if !strings.Contains(plain, "Body text survives.") {
		t.Fatalf("meta text = %q", plain)
	}
	if strings.Contains(plain, "utf-8") || strings.Contains(plain, "x.css") {
		t.Fatalf("meta text leaked head markup: %q", plain)
	}
}

func TestHandlerRejectsBinaryAndImages(t *testing.T) {
	server := newFixtureServer(t)

	imgRes := run(t, map[string]any{"url": server.URL + "/img"})
	if !imgRes.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, imgRes); !strings.Contains(got, "Unsupported fetched image content type: image/png") {
		t.Fatalf("text = %q", got)
	}

	binRes := run(t, map[string]any{"url": server.URL + "/bin", "timeout": float64(10)})
	if !binRes.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, binRes); !strings.Contains(got, "Unsupported fetched file content type") {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerResponseTooLarge(t *testing.T) {
	server := newFixtureServer(t)
	bigRes := run(t, map[string]any{"url": server.URL + "/big", "timeout": float64(10)})
	if !bigRes.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, bigRes); !strings.Contains(got, "Response too large (exceeds 5MB limit)") {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerNon2xx(t *testing.T) {
	server := newFixtureServer(t)
	notFound := run(t, map[string]any{"url": server.URL + "/nonexistent", "timeout": float64(10)})
	if !notFound.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, notFound); !strings.Contains(got, "Request failed with status 404") {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerCloudflareRetry(t *testing.T) {
	server := newFixtureServer(t)
	result := run(t, map[string]any{"url": server.URL + "/challenge"})
	if result.IsError {
		t.Fatalf("isError: %s", resultText(t, result))
	}
	if got := resultText(t, result); !strings.Contains(got, "honest bot") {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerTimeout(t *testing.T) {
	server := newFixtureServer(t)
	result := run(t, map[string]any{"url": server.URL + "/slow", "timeout": float64(1)})
	if !result.IsError {
		t.Fatal("expected isError")
	}
	if got := resultText(t, result); !strings.Contains(got, "Request timed out after 1s") {
		t.Fatalf("text = %q", got)
	}
}

func TestHandlerRedaction(t *testing.T) {
	server := newFixtureServer(t)

	redacted := resultText(t, run(t, map[string]any{"url": server.URL + "/secret"}))
	if strings.Contains(redacted, "zbcd1234efgh5678ijkl9012mnop3456") {
		t.Fatalf("secret leaked: %q", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED:") {
		t.Fatalf("no redaction marker: %q", redacted)
	}

	raw := resultText(t, run(t, map[string]any{"url": server.URL + "/secret", "redact_secrets": false}))
	if !strings.Contains(raw, "zbcd1234efgh5678ijkl9012mnop3456") {
		t.Fatalf("redact_secrets=false still redacted: %q", raw)
	}
}

func TestDecodeTextDecoder(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"ascii", []byte("abc"), "abc"},
		{"bom stripped", []byte{0xEF, 0xBB, 0xBF, 'A'}, "A"},
		{"valid multibyte", []byte("héllo 💩"), "héllo 💩"},
		{"lone continuation", []byte{0x80}, "\uFFFD"},
		{"invalid lead", []byte{0xFF}, "\uFFFD"},
		{"overlong", []byte{0xC0, 0xAF}, "\uFFFD\uFFFD"},
		{"bad second byte", []byte{0xE0, 0x80}, "\uFFFD\uFFFD"},
		{"truncated valid prefix", []byte{0xF0, 0x9F}, "\uFFFD"},
		{"surrogate", []byte{0xED, 0xA0, 0x80}, "\uFFFD\uFFFD\uFFFD"},
		{"mid-string", []byte{'a', 0x80, 'b'}, "a\uFFFDb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := decodeTextDecoder(test.data); got != test.want {
				t.Fatalf("decode = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseDecimalPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  float64
		ok    bool
	}{
		{"123", 123, true},
		{"  42x", 42, true},
		{"+7", 7, true},
		{"-7", -7, true},
		{"abc", 0, false},
		{"", 0, false},
	}
	for _, test := range tests {
		got, ok := parseDecimalPrefix(test.input)
		if ok != test.ok || got != test.want {
			t.Fatalf("parseDecimalPrefix(%q) = %v, %v", test.input, got, ok)
		}
	}
}

func TestNumberFromString(t *testing.T) {
	tests := []struct {
		input string
		want  float64
		nan   bool
	}{
		{"", 0, false},
		{" 5 ", 5, false},
		{"0x10", 16, false},
		{"0o10", 8, false},
		{"0b10", 2, false},
		{"Infinity", 0, true}, // compared separately below
		{"1e400", 0, true},
		{"12abc", 0, true},
	}
	for _, test := range tests {
		got := numberFromString(test.input)
		switch test.input {
		case "Infinity":
			if !isInfinite(got, 1) {
				t.Fatalf("numberFromString(%q) = %v", test.input, got)
			}
			continue
		case "1e400":
			if !isInfinite(got, 1) {
				t.Fatalf("numberFromString(%q) = %v", test.input, got)
			}
			continue
		}
		if test.nan {
			if got == got {
				t.Fatalf("numberFromString(%q) = %v, want NaN", test.input, got)
			}
			continue
		}
		if got != test.want {
			t.Fatalf("numberFromString(%q) = %v, want %v", test.input, got, test.want)
		}
	}
}

func isInfinite(value, sign float64) bool {
	return value == sign*value && (value > 1e308 || value < -1e308)
}
