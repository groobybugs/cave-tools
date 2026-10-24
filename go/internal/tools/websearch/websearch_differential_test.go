package websearch

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// truncatedGzipHello is Node's zlib.gzipSync("hello truncated gzip body") cut
// before its trailer. Node returns the decompressed prefix without an error.
var truncatedGzipHello = []byte{
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
	0xcb, 0x48, 0xcd, 0xc9, 0xc9, 0x57, 0x28, 0x29, 0x2a, 0xcd,
	0x4b, 0x4e, 0x2c, 0x49, 0x4d, 0x51, 0x48, 0xaf, 0xca, 0x2c,
	0x50, 0x48, 0xca,
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buffer.Bytes()
}

func zlibBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zlib.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buffer.Bytes()
}

func rawDeflateBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer, err := flate.NewWriter(&buffer, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate writer: %v", err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("flate write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("flate close: %v", err)
	}
	return buffer.Bytes()
}

// countingReader reports how many bytes a bounded reader actually consumed.
type countingReader struct {
	remaining int64
	read      int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, io.EOF
	}
	limit := int64(len(buffer))
	if limit > reader.remaining {
		limit = reader.remaining
	}
	for index := int64(0); index < limit; index++ {
		buffer[index] = 'x'
	}
	reader.remaining -= limit
	reader.read += limit
	return int(limit), nil
}

func (reader *countingReader) Close() error { return nil }

// TestCollectBoundedResponseStopsAtCap proves the cap stops the read instead
// of buffering the whole body and checking afterwards.
func TestCollectBoundedResponseStopsAtCap(t *testing.T) {
	body := &countingReader{remaining: 8 << 20}
	response := &http.Response{Header: http.Header{}, Body: body}
	_, err := collectBoundedResponse(response)
	if err == nil {
		t.Fatal("expected error")
	}
	want := fmt.Sprintf("websearch response exceeded %d bytes", MaxResponseBytes)
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if body.read > int64(MaxResponseBytes)+1 {
		t.Fatalf("read %d bytes, want at most %d", body.read, MaxResponseBytes+1)
	}
}

// TestHandlerContentEncoding covers forced gzip and both deflate framings,
// plus decoded-size caps.
func TestHandlerContentEncoding(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		body     []byte
		want     string
	}{
		{"gzip", "gzip", gzipBytes(t, []byte(directPayload("gzip result"))), "gzip result"},
		{"zlib deflate", "deflate", zlibBytes(t, []byte(directPayload("zlib result"))), "zlib result"},
		{"raw deflate", "deflate", rawDeflateBytes(t, []byte(directPayload("raw result"))), "raw result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				w.Header().Set("content-encoding", test.encoding)
				_, _ = w.Write(test.body)
			}))
			result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
			if result.IsError {
				t.Fatalf("isError: %s", resultText(t, result))
			}
			if got := resultText(t, result); !strings.Contains(got, test.want) {
				t.Fatalf("text = %q", got)
			}
		})
	}

	largePayload := directPayload(strings.Repeat("x", MaxResponseBytes+1024))
	largeTests := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"gzip", "gzip", gzipBytes(t, []byte(largePayload))},
		{"zlib deflate", "deflate", zlibBytes(t, []byte(largePayload))},
		{"raw deflate", "deflate", rawDeflateBytes(t, []byte(largePayload))},
	}
	for _, test := range largeTests {
		t.Run(test.name+" decoded too large", func(t *testing.T) {
			installFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				w.Header().Set("content-encoding", test.encoding)
				_, _ = w.Write(test.body)
			}))
			result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
			if !result.IsError {
				t.Fatalf("expected isError, got %q", resultText(t, result))
			}
			want := fmt.Sprintf("Unable to search the web for abc: websearch response exceeded %d bytes", MaxResponseBytes)
			if got := resultText(t, result); got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
		})
	}
}

// TestDecodeNodeUTF8Corpus pins Buffer.toString("utf-8") semantics: unlike
// TextDecoder it keeps a leading BOM. websearch.ts uses Buffer.toString.
func TestDecodeNodeUTF8Corpus(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
		want  string
	}{
		{"empty", nil, ""},
		{"ascii", []byte("AB"), "AB"},
		{"bom only", []byte{0xEF, 0xBB, 0xBF}, "\uFEFF"},
		{"bom text", []byte{0xEF, 0xBB, 0xBF, 0x41}, "\uFEFFA"},
		{"bom mid", []byte{0x41, 0xEF, 0xBB, 0xBF, 0x42}, "A\uFEFFB"},
		{"bom truncated", []byte{0xEF, 0xBB}, "\uFFFD"},
		{"lone 80", []byte{0x80}, "\uFFFD"},
		{"lone BF", []byte{0xBF}, "\uFFFD"},
		{"overlong 2", []byte{0xC0, 0x80}, "\uFFFD\uFFFD"},
		{"overlong 3", []byte{0xE0, 0x80, 0x80}, "\uFFFD\uFFFD\uFFFD"},
		{"surrogate", []byte{0xED, 0xA0, 0x80}, "\uFFFD\uFFFD\uFFFD"},
		{"surrogate max", []byte{0xED, 0xBF, 0xBF}, "\uFFFD\uFFFD\uFFFD"},
		{"truncated 4", []byte{0xF0, 0x9F, 0x92}, "\uFFFD"},
		{"truncated 3", []byte{0xE2, 0x82}, "\uFFFD"},
		{"bad lead F5", []byte{0xF5, 0x80, 0x80, 0x80}, "\uFFFD\uFFFD\uFFFD\uFFFD"},
		{"stray then ascii", []byte{0x41, 0xFF, 0x42}, "A\uFFFDB"},
		{"stray after lead", []byte{0xE2, 0x28, 0xA1}, "\uFFFD(\uFFFD"},
		{"cesu surrogate", []byte{0xED, 0xA0, 0xBD, 0xED, 0xB8, 0x80}, "\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD"},
		{"bad third byte", []byte{0xE1, 0x80, 0x41}, "\uFFFDA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := decodeNodeUTF8(test.bytes); got != test.want {
				t.Fatalf("decode = %q, want %q", got, test.want)
			}
		})
	}
}

// mcpCapture records requests received by the redirect fixtures.
type mcpCapture struct {
	mu       sync.Mutex
	requests []capturedMCP
}

type capturedMCP struct {
	method  string
	headers http.Header
	body    string
}

func (capture *mcpCapture) record(request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.requests = append(capture.requests, capturedMCP{
		method:  request.Method,
		headers: request.Header.Clone(),
		body:    string(body),
	})
}

func (capture *mcpCapture) snapshot() []capturedMCP {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]capturedMCP(nil), capture.requests...)
}

// useDefaultHTTPClient bypasses installFixture so redirect URLs are real.
func useDefaultHTTPClient(t *testing.T) {
	t.Helper()
	previous := httpClient
	httpClient = &http.Client{}
	t.Cleanup(func() { httpClient = previous })
}

// TestCallMcpRedirectSemantics compares redirect method/body/header rules with
// Node fetch: 303/301/302 rewrite POST to GET and drop the body headers, while
// 307/308 replay body and headers. Authorization survives same-origin hops and
// is dropped cross-origin.
func TestCallMcpRedirectSemantics(t *testing.T) {
	useDefaultHTTPClient(t)

	headers := map[string]string{"Authorization": "Bearer pk", "User-Agent": "cave-tools"}
	payload := []byte(directPayload("redirected"))
	wantBody, err := marshalRequest("web_search", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	t.Run("same origin 307 keeps method body and authorization", func(t *testing.T) {
		capture := &mcpCapture{}
		mux := http.NewServeMux()
		mux.HandleFunc("/start", func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, "/final", http.StatusTemporaryRedirect)
		})
		mux.HandleFunc("/final", func(w http.ResponseWriter, request *http.Request) {
			capture.record(request)
			_, _ = w.Write(payload)
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		if _, ok, err := callMcp(context.Background(), server.URL+"/start", "web_search", map[string]any{"x": 1}, headers, 5); err != nil || !ok {
			t.Fatalf("callMcp = %v, %v", ok, err)
		}
		requests := capture.snapshot()
		if len(requests) != 1 {
			t.Fatalf("requests = %d", len(requests))
		}
		got := requests[0]
		if got.method != http.MethodPost {
			t.Fatalf("method = %q", got.method)
		}
		if got.body != string(wantBody) {
			t.Fatalf("body = %q, want %q", got.body, wantBody)
		}
		if value := got.headers.Get("Content-Type"); value != "application/json" {
			t.Fatalf("Content-Type = %q", value)
		}
		if value := got.headers.Get("Authorization"); value != "Bearer pk" {
			t.Fatalf("Authorization = %q", value)
		}
		if value := got.headers.Get("Accept-Encoding"); value != "gzip, deflate" {
			t.Fatalf("Accept-Encoding = %q, want gzip, deflate", value)
		}
	})

	t.Run("cross origin 307 drops authorization", func(t *testing.T) {
		capture := &mcpCapture{}
		final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			capture.record(request)
			_, _ = w.Write(payload)
		}))
		t.Cleanup(final.Close)
		start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, final.URL+"/final", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(start.Close)

		if _, ok, err := callMcp(context.Background(), start.URL+"/start", "web_search", map[string]any{"x": 1}, headers, 5); err != nil || !ok {
			t.Fatalf("callMcp = %v, %v", ok, err)
		}
		got := capture.snapshot()[0]
		if got.method != http.MethodPost || got.body != string(wantBody) {
			t.Fatalf("method/body = %q %q, want body %q", got.method, got.body, wantBody)
		}
		if value := got.headers.Get("Authorization"); value != "" {
			t.Fatalf("Authorization = %q, want empty", value)
		}
		if value := got.headers.Get("User-Agent"); value != "cave-tools" {
			t.Fatalf("User-Agent = %q", value)
		}
	})

	t.Run("same origin 302 rewrites post to get", func(t *testing.T) {
		capture := &mcpCapture{}
		mux := http.NewServeMux()
		mux.HandleFunc("/start", func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, "/final", http.StatusFound)
		})
		mux.HandleFunc("/final", func(w http.ResponseWriter, request *http.Request) {
			capture.record(request)
			_, _ = w.Write(payload)
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		if _, ok, err := callMcp(context.Background(), server.URL+"/start", "web_search", map[string]any{"x": 1}, headers, 5); err != nil || !ok {
			t.Fatalf("callMcp = %v, %v", ok, err)
		}
		got := capture.snapshot()[0]
		if got.method != http.MethodGet {
			t.Fatalf("method = %q", got.method)
		}
		if got.body != "" {
			t.Fatalf("body = %q", got.body)
		}
		if value := got.headers.Get("Content-Type"); value != "" {
			t.Fatalf("Content-Type = %q, want empty", value)
		}
		if value := got.headers.Get("Authorization"); value != "Bearer pk" {
			t.Fatalf("Authorization = %q", value)
		}
	})

	t.Run("cross origin 302 drops authorization", func(t *testing.T) {
		capture := &mcpCapture{}
		final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			capture.record(request)
			_, _ = w.Write(payload)
		}))
		t.Cleanup(final.Close)
		start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, final.URL+"/final", http.StatusFound)
		}))
		t.Cleanup(start.Close)

		if _, ok, err := callMcp(context.Background(), start.URL+"/start", "web_search", map[string]any{"x": 1}, headers, 5); err != nil || !ok {
			t.Fatalf("callMcp = %v, %v", ok, err)
		}
		got := capture.snapshot()[0]
		if got.method != http.MethodGet || got.body != "" {
			t.Fatalf("method/body = %q %q", got.method, got.body)
		}
		if value := got.headers.Get("Authorization"); value != "" {
			t.Fatalf("Authorization = %q, want empty", value)
		}
	})

	t.Run("twenty redirects succeed", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/chain", func(w http.ResponseWriter, request *http.Request) {
			n := 0
			_, _ = fmt.Sscanf(request.URL.Query().Get("n"), "%d", &n)
			if n > 0 {
				http.Redirect(w, request, fmt.Sprintf("/chain?n=%d", n-1), http.StatusFound)
				return
			}
			_, _ = w.Write(payload)
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		if _, ok, err := callMcp(context.Background(), server.URL+"/chain?n=20", "web_search", map[string]any{}, headers, 5); err != nil || !ok {
			t.Fatalf("callMcp = %v, %v", ok, err)
		}
	})

	t.Run("twenty-one redirects fail", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/chain", func(w http.ResponseWriter, request *http.Request) {
			n := 0
			_, _ = fmt.Sscanf(request.URL.Query().Get("n"), "%d", &n)
			if n > 0 {
				http.Redirect(w, request, fmt.Sprintf("/chain?n=%d", n-1), http.StatusFound)
				return
			}
			_, _ = w.Write(payload)
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		_, _, err := callMcp(context.Background(), server.URL+"/chain?n=21", "web_search", map[string]any{}, headers, 5)
		if !errors.Is(err, errFetchFailed) {
			t.Fatalf("error = %v, want fetch failed", err)
		}
	})
}

// TestParseInputScalarMatrix pins positiveInt/Number coercion for the numeric
// options.
func TestParseInputScalarMatrix(t *testing.T) {
	maxContext := 50000
	tests := []struct {
		name string
		args map[string]any
		want webSearchInput
	}{
		{
			name: "defaults",
			args: map[string]any{"query": "q"},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "empty array num results falls back",
			args: map[string]any{"query": "q", "numResults": []any{}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "singleton null num results falls back",
			args: map[string]any{"query": "q", "numResults": []any{nil}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "singleton number num results",
			args: map[string]any{"query": "q", "numResults": []any{float64(5)}},
			want: webSearchInput{query: "q", numResults: 5, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "singleton string num results",
			args: map[string]any{"query": "q", "numResults": []any{"5"}},
			want: webSearchInput{query: "q", numResults: 5, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "nested singleton num results",
			args: map[string]any{"query": "q", "numResults": []any{[]any{float64(5)}}},
			want: webSearchInput{query: "q", numResults: 5, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "multi num results falls back",
			args: map[string]any{"query": "q", "numResults": []any{float64(1), float64(2)}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "zero num results falls back",
			args: map[string]any{"query": "q", "numResults": float64(0)},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "negative num results clamps to one",
			args: map[string]any{"query": "q", "numResults": float64(-3)},
			want: webSearchInput{query: "q", numResults: 1, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "num results clamps to max",
			args: map[string]any{"query": "q", "numResults": float64(25)},
			want: webSearchInput{query: "q", numResults: 20, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "fraction truncates",
			args: map[string]any{"query": "q", "numResults": []any{float64(2.9)}},
			want: webSearchInput{query: "q", numResults: 2, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "true num results clamps to one",
			args: map[string]any{"query": "q", "numResults": true},
			want: webSearchInput{query: "q", numResults: 1, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "false num results falls back",
			args: map[string]any{"query": "q", "numResults": false},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "empty context array becomes present fallback",
			args: map[string]any{"query": "q", "contextMaxCharacters": []any{}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true, contextMaxCharacters: intPointer(10_000)},
		},
		{
			name: "singleton context",
			args: map[string]any{"query": "q", "contextMaxCharacters": []any{float64(123)}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true, contextMaxCharacters: intPointer(123)},
		},
		{
			name: "multi context falls back",
			args: map[string]any{"query": "q", "contextMaxCharacters": []any{float64(1), float64(2)}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true, contextMaxCharacters: intPointer(10_000)},
		},
		{
			name: "context clamps",
			args: map[string]any{"query": "q", "contextMaxCharacters": float64(60_000)},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true, contextMaxCharacters: &maxContext},
		},
		{
			name: "empty array timeout falls back",
			args: map[string]any{"query": "q", "timeout": []any{}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
		},
		{
			name: "singleton timeout",
			args: map[string]any{"query": "q", "timeout": []any{float64(3)}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 3, redactSecrets: true},
		},
		{
			name: "multi timeout falls back",
			args: map[string]any{"query": "q", "timeout": []any{float64(1), float64(2)}},
			want: webSearchInput{query: "q", numResults: 8, livecrawl: "fallback", typ: "auto", timeout: 25, redactSecrets: true},
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

func intPointer(value int) *int { return &value }

// TestJSCoercionMatrix covers Number(value) and String(value) for arrays and
// scalars in the websearch package.
func TestJSCoercionMatrix(t *testing.T) {
	numberTests := []struct {
		name  string
		value any
		want  float64
		nan   bool
	}{
		{"null", nil, 0, false},
		{"number", float64(7), 7, false},
		{"empty array", []any{}, 0, false},
		{"singleton null", []any{nil}, 0, false},
		{"singleton empty array", []any{[]any{}}, 0, false},
		{"singleton number", []any{float64(7)}, 7, false},
		{"singleton string", []any{"7"}, 7, false},
		{"singleton true", []any{true}, 0, true},
		{"multi", []any{float64(1), float64(2)}, 0, true},
		{"nested", []any{[]any{float64(1), float64(2)}}, 0, true},
		{"object", map[string]any{}, 0, true},
		{"infinity string", "Infinity", math.Inf(1), false},
	}
	for _, test := range numberTests {
		t.Run("Number/"+test.name, func(t *testing.T) {
			got := jsNumber(test.value)
			if test.nan {
				if got == got {
					t.Fatalf("jsNumber(%v) = %v, want NaN", test.value, got)
				}
				return
			}
			if got != test.want {
				t.Fatalf("jsNumber(%v) = %v, want %v", test.value, got, test.want)
			}
		})
	}

	stringTests := []struct {
		name  string
		value any
		want  string
	}{
		{"empty array", []any{}, ""},
		{"singleton null", []any{nil}, ""},
		{"nested empty", []any{[]any{}}, ""},
		{"array holes", []any{float64(1), nil, float64(2)}, "1,,2"},
		{"nested pair", []any{[]any{"a", "b"}}, "a,b"},
		{"array object", []any{map[string]any{}}, "[object Object]"},
		{"number", float64(42), "42"},
		{"large", float64(1e21), "1e+21"},
	}
	for _, test := range stringTests {
		t.Run("String/"+test.name, func(t *testing.T) {
			if got := jsString(test.value); got != test.want {
				t.Fatalf("jsString(%v) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// streamingBody serves pre-split chunks and records how much the tool read.
type streamingBody struct {
	chunks    [][]byte
	next      int
	served    int
	readCalls int
	closed    bool
}

func (body *streamingBody) Read(buffer []byte) (int, error) {
	if body.next >= len(body.chunks) {
		return 0, io.EOF
	}
	chunk := body.chunks[body.next]
	count := copy(buffer, chunk)
	body.served += count
	body.readCalls++
	if count == len(chunk) {
		body.next++
	} else {
		body.chunks[body.next] = chunk[count:]
	}
	return count, nil
}

func (body *streamingBody) Close() error {
	body.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// installStreamingResponse replaces the package client with a fake transport
// that returns one streaming body, like the TS Response fixtures.
func installStreamingResponse(t *testing.T, header http.Header, body io.ReadCloser) {
	t.Helper()
	previous := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       body,
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { httpClient = previous })
}

// Ports scripts/test-port-bugs.mjs 22: chunked body stops at cap and closes.
func TestStreamingResponseStopsAtCapAndCloses(t *testing.T) {
	chunks := make([][]byte, 320)
	for index := range chunks {
		chunks[index] = bytes.Repeat([]byte("a"), 1024)
	}
	body := &streamingBody{chunks: chunks}
	installStreamingResponse(t, http.Header{"Content-Type": []string{"application/json"}}, body)

	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
	if !result.IsError {
		t.Fatalf("expected isError, got %q", resultText(t, result))
	}
	want := fmt.Sprintf("Unable to search the web for abc: websearch response exceeded %d bytes", MaxResponseBytes)
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if !body.closed {
		t.Fatal("stream was not closed")
	}
	if body.served > 258*1024 {
		t.Fatalf("consumed %d bytes (%d reads), want at most %d", body.served, body.readCalls, 258*1024)
	}
}

// Ports scripts/test-port-bugs.mjs 23: declared oversize content-length is
// rejected without touching the stream.
func TestStreamingDeclaredOversizeDoesNotRead(t *testing.T) {
	chunks := make([][]byte, 320)
	for index := range chunks {
		chunks[index] = bytes.Repeat([]byte("a"), 1024)
	}
	body := &streamingBody{chunks: chunks}
	header := http.Header{
		"Content-Type":   []string{"application/json"},
		"Content-Length": []string{strconv.Itoa(MaxResponseBytes + 1)},
	}
	installStreamingResponse(t, header, body)

	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
	if !result.IsError {
		t.Fatalf("expected isError, got %q", resultText(t, result))
	}
	want := fmt.Sprintf("Unable to search the web for abc: websearch response exceeded %d bytes", MaxResponseBytes)
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if body.served != 0 || body.readCalls != 0 {
		t.Fatalf("read %d bytes in %d calls, want zero", body.served, body.readCalls)
	}
}

// Ports scripts/test-port-bugs.mjs 24: a null response body yields NO_RESULTS.
func TestNullResponseBodyYieldsNoResults(t *testing.T) {
	text, err := collectBoundedResponse(&http.Response{Header: http.Header{}})
	if err != nil {
		t.Fatalf("collectBoundedResponse: %v", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}

	installStreamingResponse(t, http.Header{}, nil)
	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
	if result.IsError {
		t.Fatalf("isError: %s", resultText(t, result))
	}
	if got := resultText(t, result); got != NoResults {
		t.Fatalf("text = %q, want %q", got, NoResults)
	}
}

// Ports scripts/test-port-bugs.mjs 25: UTF-8 split across stream chunks is
// decoded once from the concatenated bytes.
func TestStreamingSplitUTF8(t *testing.T) {
	payload := []byte(directPayload("héllo ✓"))
	checkmark := []byte("✓")
	cut := bytes.Index(payload, checkmark)
	if cut <= 0 {
		t.Fatal("fixture did not contain the multibyte character")
	}
	cut++
	body := &streamingBody{chunks: [][]byte{payload[:cut], payload[cut:]}}
	installStreamingResponse(t, http.Header{"Content-Type": []string{"application/json"}}, body)

	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
	if result.IsError {
		t.Fatalf("isError: %s", resultText(t, result))
	}
	if got := resultText(t, result); got != "héllo ✓" {
		t.Fatalf("text = %q, want %q", got, "héllo ✓")
	}
}

// TestDeclaredLengthRejectsEncodedBodyWithoutReading pins Node's rule: the
// declared Content-Length is checked before decoding. Node 24 keeps the header
// on gzip responses.
func TestDeclaredLengthRejectsEncodedBodyWithoutReading(t *testing.T) {
	body := &streamingBody{chunks: [][]byte{gzipBytes(t, []byte(directPayload("compressed result")))}}
	installStreamingResponse(t, http.Header{
		"Content-Type":     []string{"application/json"},
		"Content-Encoding": []string{"gzip"},
		"Content-Length":   []string{strconv.Itoa(MaxResponseBytes + 1)},
	}, body)

	result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
	if !result.IsError {
		t.Fatalf("expected isError, got %q", resultText(t, result))
	}
	want := fmt.Sprintf("Unable to search the web for abc: websearch response exceeded %d bytes", MaxResponseBytes)
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if body.served != 0 || body.readCalls != 0 {
		t.Fatalf("read %d bytes in %d calls, want zero", body.served, body.readCalls)
	}
}

// TestGzipBodyErrors pins Node's body-read results for empty, one-byte,
// corrupt, truncated and checksum-failing gzip bodies.
func TestGzipBodyErrors(t *testing.T) {
	full := gzipBytes(t, []byte(directPayload("hello truncated gzip body")))
	checksum := append([]byte(nil), full...)
	checksum[len(checksum)-5] ^= 0xFF

	tests := []struct {
		name    string
		body    []byte
		want    string
		isError bool
	}{
		{"empty", nil, NoResults, false},
		{"one byte", []byte{0x1F}, NoResults, false},
		{"corrupt header", []byte("not gzip at all"), "terminated", true},
		{"truncated trailer", truncatedGzipHello, NoResults, false},
		{"bad checksum", checksum, "terminated", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &streamingBody{}
			if test.body != nil {
				body.chunks = [][]byte{test.body}
			}
			installStreamingResponse(t, http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"gzip"},
			}, body)

			result := run(t, map[string]string{}, map[string]any{"query": "abc", "provider": "exa", "timeout": float64(2)})
			if result.IsError != test.isError {
				t.Fatalf("isError = %v, want %v: %q", result.IsError, test.isError, resultText(t, result))
			}
			if test.isError {
				want := "Unable to search the web for abc: " + test.want
				if got := resultText(t, result); got != want {
					t.Fatalf("text = %q, want %q", got, want)
				}
				return
			}
			if got := resultText(t, result); got != test.want {
				t.Fatalf("text = %q, want %q", got, test.want)
			}
		})
	}
}
