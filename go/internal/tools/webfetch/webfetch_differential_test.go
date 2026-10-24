package webfetch

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// gzipBytes is a helper for the forced Content-Encoding fixtures.
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

// zlibBytes wraps data in a zlib stream ("deflate" per HTTP).
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

// rawDeflateBytes is an unframed DEFLATE stream ("deflate" fallback).
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

// brotliHelloBR is `brotliCompressSync(Buffer.from("hello br"))` from Node.
// The standard library has no Brotli decoder, so this fixture documents the
// unresolved gap rather than a supported encoding.
var brotliHelloBR = []byte{0x8b, 0x03, 0x80, 0x68, 0x65, 0x6c, 0x6c, 0x6f, 0x20, 0x62, 0x72, 0x03}

// malformedUTF8Cases is the Node TextDecoder corpus for webfetch.
var malformedUTF8Cases = map[string]struct {
	bytes []byte
	want  string
}{
	"empty":            {nil, ""},
	"ascii":            {[]byte("AB"), "AB"},
	"bom only":         {[]byte{0xEF, 0xBB, 0xBF}, ""},
	"bom text":         {[]byte{0xEF, 0xBB, 0xBF, 0x41}, "A"},
	"bom mid":          {[]byte{0x41, 0xEF, 0xBB, 0xBF, 0x42}, "A\uFEFFB"},
	"bom truncated":    {[]byte{0xEF, 0xBB}, "\uFFFD"},
	"lone 80":          {[]byte{0x80}, "\uFFFD"},
	"lone BF":          {[]byte{0xBF}, "\uFFFD"},
	"overlong 2":       {[]byte{0xC0, 0x80}, "\uFFFD\uFFFD"},
	"overlong E0":      {[]byte{0xE0, 0x80, 0x80}, "\uFFFD\uFFFD\uFFFD"},
	"surrogate ED":     {[]byte{0xED, 0xA0, 0x80}, "\uFFFD\uFFFD\uFFFD"},
	"surrogate max":    {[]byte{0xED, 0xBF, 0xBF}, "\uFFFD\uFFFD\uFFFD"},
	"truncated 4":      {[]byte{0xF0, 0x9F, 0x92}, "\uFFFD"},
	"truncated 3":      {[]byte{0xE2, 0x82}, "\uFFFD"},
	"bad lead F5":      {[]byte{0xF5, 0x80, 0x80, 0x80}, "\uFFFD\uFFFD\uFFFD\uFFFD"},
	"stray then ascii": {[]byte{0x41, 0xFF, 0x42}, "A\uFFFDB"},
	"stray after lead": {[]byte{0xE2, 0x28, 0xA1}, "\uFFFD(\uFFFD"},
	"cesu surrogate":   {[]byte{0xED, 0xA0, 0xBD, 0xED, 0xB8, 0x80}, "\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD"},
	"bad third byte":   {[]byte{0xE1, 0x80, 0x41}, "\uFFFDA"},
	"truncated e1":     {[]byte{0xE1}, "\uFFFD"},
}

// testCapture collects the requests a fixture server receives.
type testCapture struct {
	mu       sync.Mutex
	requests []capturedHTTP
}

type capturedHTTP struct {
	method  string
	path    string
	headers http.Header
	body    string
}

func (c *testCapture) record(request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, capturedHTTP{
		method:  request.Method,
		path:    request.URL.Path,
		headers: request.Header.Clone(),
		body:    string(body),
	})
}

func (c *testCapture) snapshot() []capturedHTTP {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedHTTP(nil), c.requests...)
}

// TestHandlerRedirectLimitMatchesNodeFetch proves the default net/http limit
// (10) is not Node fetch's 20. Node follows twenty redirects and fails on the
// twenty-first with "fetch failed".
func TestHandlerRedirectLimitMatchesNodeFetch(t *testing.T) {
	server := newFixtureServer(t)

	for _, redirects := range []int{12, 20} {
		result := run(t, map[string]any{
			"url":     fmt.Sprintf("%s/chain?n=%d", server.URL, redirects),
			"timeout": float64(5),
		})
		if result.IsError {
			t.Fatalf("%d redirects: isError: %s", redirects, resultText(t, result))
		}
		if got := resultText(t, result); got != "chain end" {
			t.Fatalf("%d redirects: text = %q", redirects, got)
		}
	}

	result := run(t, map[string]any{
		"url":     server.URL + "/chain?n=21",
		"timeout": float64(5),
	})
	if !result.IsError {
		t.Fatalf("21 redirects: expected isError, got %q", resultText(t, result))
	}
	want := fmt.Sprintf("Unable to fetch %s/chain?n=21: fetch failed", server.URL)
	if got := resultText(t, result); got != want {
		t.Fatalf("21 redirects: text = %q, want %q", got, want)
	}
}

// TestRedirectPreservesNodeFetchSemantics checks the redirect header rules:
// same-origin keeps every header; a cross-origin hop drops Authorization but
// keeps ordinary headers (undici's fetch does exactly this).
func TestRedirectPreservesNodeFetchSemantics(t *testing.T) {
	t.Run("same origin keeps authorization", func(t *testing.T) {
		capture := &testCapture{}
		mux := http.NewServeMux()
		mux.HandleFunc("/start", func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, "/final", http.StatusFound)
		})
		mux.HandleFunc("/final", func(w http.ResponseWriter, request *http.Request) {
			capture.record(request)
			w.Header().Set("content-type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		headers := map[string]string{
			"User-Agent":      "ua-test",
			"Accept":          "text/plain",
			"Accept-Language": "en-US,en;q=0.9",
			"Authorization":   "Bearer sekret",
			"X-Custom":        "yes",
		}
		if _, err := fetchBounded(context.Background(), server.URL+"/start", headers, 5); err != nil {
			t.Fatalf("fetchBounded: %v", err)
		}
		requests := capture.snapshot()
		if len(requests) != 1 {
			t.Fatalf("requests = %d", len(requests))
		}
		got := requests[0]
		if got.method != http.MethodGet {
			t.Fatalf("method = %q", got.method)
		}
		for header, want := range map[string]string{
			"Authorization":   "Bearer sekret",
			"X-Custom":        "yes",
			"Accept-Language": "en-US,en;q=0.9",
			"User-Agent":      "ua-test",
			"Accept":          "text/plain",
			"Accept-Encoding": "gzip, deflate",
		} {
			if value := got.headers.Get(header); value != want {
				t.Fatalf("%s = %q, want %q", header, value, want)
			}
		}
	})

	t.Run("cross origin drops authorization", func(t *testing.T) {
		capture := &testCapture{}
		final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			capture.record(request)
			w.Header().Set("content-type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		}))
		t.Cleanup(final.Close)
		start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, final.URL+"/final", http.StatusFound)
		}))
		t.Cleanup(start.Close)

		headers := map[string]string{
			"User-Agent":      "ua-test",
			"Accept":          "text/plain",
			"Accept-Language": "en-US,en;q=0.9",
			"Authorization":   "Bearer sekret",
			"X-Custom":        "yes",
		}
		if _, err := fetchBounded(context.Background(), start.URL+"/start", headers, 5); err != nil {
			t.Fatalf("fetchBounded: %v", err)
		}
		requests := capture.snapshot()
		if len(requests) != 1 {
			t.Fatalf("requests = %d", len(requests))
		}
		got := requests[0]
		if authorization := got.headers.Get("Authorization"); authorization != "" {
			t.Fatalf("Authorization = %q, want empty", authorization)
		}
		for header, want := range map[string]string{
			"X-Custom":        "yes",
			"Accept-Language": "en-US,en;q=0.9",
			"User-Agent":      "ua-test",
			"Accept":          "text/plain",
			"Accept-Encoding": "gzip, deflate",
		} {
			if value := got.headers.Get(header); value != want {
				t.Fatalf("%s = %q, want %q", header, value, want)
			}
		}
	})
}

// TestOutgoingHeaders asserts the exact webfetch request shape: GET, the
// format-specific Accept, the OpenCode user agent, Accept-Language, and the
// Node-fetch-accept-encoding set. The Cloudflare retry keeps every header and
// only changes the user agent.
func TestOutgoingHeaders(t *testing.T) {
	capture := &testCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		capture.record(request)
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	for _, format := range []string{"markdown", "text", "html"} {
		result := run(t, map[string]any{"url": server.URL + "/x", "format": format})
		if result.IsError {
			t.Fatalf("%s: isError: %s", format, resultText(t, result))
		}
	}
	requests := capture.snapshot()
	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	for index, format := range []string{"markdown", "text", "html"} {
		request := requests[index]
		if request.method != http.MethodGet {
			t.Fatalf("%s: method = %q", format, request.method)
		}
		if got := request.headers.Get("User-Agent"); got != opencodeUserAgent {
			t.Fatalf("%s: User-Agent = %q", format, got)
		}
		if got := request.headers.Get("Accept"); got != acceptFor(format) {
			t.Fatalf("%s: Accept = %q, want %q", format, got, acceptFor(format))
		}
		if got := request.headers.Get("Accept-Language"); got != "en-US,en;q=0.9" {
			t.Fatalf("%s: Accept-Language = %q", format, got)
		}
		if got := request.headers.Get("Accept-Encoding"); got != "gzip, deflate" {
			t.Fatalf("%s: Accept-Encoding = %q, want %q", format, got, "gzip, deflate")
		}
	}

	// Cloudflare challenge: first request uses the honest bot UA, retry uses
	// "opencode" and preserves Accept/Accept-Language/Accept-Encoding.
	challengeCapture := &testCapture{}
	challenge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		challengeCapture.record(request)
		if request.UserAgent() != "opencode" {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("challenge"))
			return
		}
		w.Header().Set("content-type", "text/plain")
		_, _ = w.Write([]byte("honest bot"))
	}))
	t.Cleanup(challenge.Close)

	challengeResult := run(t, map[string]any{"url": challenge.URL + "/x", "format": "text"})
	if challengeResult.IsError {
		t.Fatalf("challenge: isError: %s", resultText(t, challengeResult))
	}
	challengeRequests := challengeCapture.snapshot()
	if len(challengeRequests) != 2 {
		t.Fatalf("challenge requests = %d", len(challengeRequests))
	}
	if got := challengeRequests[0].headers.Get("User-Agent"); got != opencodeUserAgent {
		t.Fatalf("challenge first User-Agent = %q", got)
	}
	retry := challengeRequests[1]
	if got := retry.headers.Get("User-Agent"); got != "opencode" {
		t.Fatalf("retry User-Agent = %q", got)
	}
	if got := retry.headers.Get("Accept"); got != acceptFor("text") {
		t.Fatalf("retry Accept = %q", got)
	}
	if got := retry.headers.Get("Accept-Language"); got != "en-US,en;q=0.9" {
		t.Fatalf("retry Accept-Language = %q", got)
	}
	if got := retry.headers.Get("Accept-Encoding"); got != "gzip, deflate" {
		t.Fatalf("retry Accept-Encoding = %q", got)
	}
}

// TestHandlerContentEncoding proves forced gzip and both deflate framings are
// decoded, and that the 5MB cap counts decoded bytes.
func TestHandlerContentEncoding(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		body     []byte
		want     string
	}{
		{"gzip", "gzip", gzipBytes(t, []byte("hello gzip")), "hello gzip"},
		{"zlib deflate", "deflate", zlibBytes(t, []byte("hello zlib")), "hello zlib"},
		{"raw deflate", "deflate", rawDeflateBytes(t, []byte("hello raw")), "hello raw"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "text/plain")
				w.Header().Set("content-encoding", test.encoding)
				_, _ = w.Write(test.body)
			}))
			t.Cleanup(server.Close)

			result := run(t, map[string]any{"url": server.URL + "/x", "format": "text"})
			if result.IsError {
				t.Fatalf("isError: %s", resultText(t, result))
			}
			if got := resultText(t, result); got != test.want {
				t.Fatalf("text = %q, want %q", got, test.want)
			}
		})
	}

	big := bytes.Repeat([]byte("x"), maxResponseBytes+1024)
	largeTests := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"gzip", "gzip", gzipBytes(t, big)},
		{"zlib deflate", "deflate", zlibBytes(t, big)},
		{"raw deflate", "deflate", rawDeflateBytes(t, big)},
	}
	for _, test := range largeTests {
		t.Run(test.name+" decoded too large", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "text/plain")
				w.Header().Set("content-encoding", test.encoding)
				_, _ = w.Write(test.body)
			}))
			t.Cleanup(server.Close)

			result := run(t, map[string]any{"url": server.URL + "/x", "format": "text", "timeout": float64(10)})
			if !result.IsError {
				t.Fatalf("expected isError, got %q", resultText(t, result))
			}
			want := fmt.Sprintf("Unable to fetch %s/x: Response too large (exceeds 5MB limit)", server.URL)
			if got := resultText(t, result); got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
		})
	}
}

// TestMalformedUTF8TextDecoder pins the TextDecoder (BOM-stripping) corpus.
func TestMalformedUTF8TextDecoder(t *testing.T) {
	for name, test := range malformedUTF8Cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "text/plain")
				_, _ = w.Write(test.bytes)
			}))
			t.Cleanup(server.Close)

			result := run(t, map[string]any{"url": server.URL + "/utf8", "format": "text"})
			if result.IsError {
				t.Fatalf("isError: %s", resultText(t, result))
			}
			if got := resultText(t, result); got != test.want {
				t.Fatalf("text = %q, want %q", got, test.want)
			}
		})
	}
}

// TestClosedPortTransportFailure compares the local closed-port failure with
// Node fetch: both surface as "fetch failed".
func TestClosedPortTransportFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	result := run(t, map[string]any{"url": url, "timeout": float64(5)})
	if !result.IsError {
		t.Fatalf("expected isError, got %q", resultText(t, result))
	}
	want := "Unable to fetch " + url + ": fetch failed"
	if got := resultText(t, result); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// TestJSCoercionMatrix covers Number(value) and String(value) for arrays,
// overrides, strings and scalars. Values come from the JSON shapes a handler
// receives; missing keys are tested through parseInput.
func TestJSCoercionMatrix(t *testing.T) {
	numberTests := []struct {
		name  string
		value any
		want  float64
		nan   bool
	}{
		{"null", nil, 0, false},
		{"true", true, 1, false},
		{"false", false, 0, false},
		{"number", float64(7), 7, false},
		{"empty string", "", 0, false},
		{"space string", " ", 0, false},
		{"decimal string", " 12.5 ", 12.5, false},
		{"hex string", "0x10", 16, false},
		{"infinity string", "Infinity", math.Inf(1), false},
		{"negative infinity string", "-Infinity", math.Inf(-1), false},
		{"empty array", []any{}, 0, false},
		{"singleton null", []any{nil}, 0, false},
		{"singleton empty array", []any{[]any{}}, 0, false},
		{"singleton number", []any{float64(7)}, 7, false},
		{"singleton string", []any{"7"}, 7, false},
		{"singleton exponent", []any{"1e3"}, 1000, false},
		{"singleton large", []any{float64(1e21)}, 1e21, false},
		{"singleton small", []any{float64(1e-7)}, 1e-7, false},
		{"singleton true", []any{true}, 0, true},
		{"multi", []any{float64(1), float64(2)}, 0, true},
		{"nested", []any{[]any{float64(1), float64(2)}}, 0, true},
		{"object element", []any{map[string]any{}}, 0, true},
		{"object", map[string]any{}, 0, true},
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
		{"singleton empty array", []any{[]any{}}, ""},
		{"array holes", []any{float64(1), nil, float64(2)}, "1,,2"},
		{"nested array", []any{[]any{float64(1), float64(2)}}, "1,2"},
		{"array object", []any{map[string]any{}}, "[object Object]"},
		{"number", float64(42), "42"},
		{"negative zero", math.Copysign(0, -1), "0"},
		{"large", float64(1e21), "1e+21"},
		{"small", float64(1e-7), "1e-7"},
	}
	for _, test := range stringTests {
		t.Run("String/"+test.name, func(t *testing.T) {
			if got := jsString(test.value); got != test.want {
				t.Fatalf("jsString(%v) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// TestParseInputScalarMatrix pins webfetch's Number(args.timeout) semantics.
func TestParseInputScalarMatrix(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{"missing", map[string]any{"url": "http://x"}, 30},
		{"null", map[string]any{"url": "http://x", "timeout": nil}, 30},
		{"empty array", map[string]any{"url": "http://x", "timeout": []any{}}, 30},
		{"singleton null", map[string]any{"url": "http://x", "timeout": []any{nil}}, 30},
		{"singleton number", map[string]any{"url": "http://x", "timeout": []any{float64(5)}}, 5},
		{"singleton string", map[string]any{"url": "http://x", "timeout": []any{"7"}}, 7},
		{"multielement", map[string]any{"url": "http://x", "timeout": []any{float64(1), float64(2)}}, 30},
		{"object array", map[string]any{"url": "http://x", "timeout": []any{map[string]any{}}}, 30},
		{"nested empty", map[string]any{"url": "http://x", "timeout": []any{[]any{}}}, 30},
		{"true", map[string]any{"url": "http://x", "timeout": true}, 1},
		{"false", map[string]any{"url": "http://x", "timeout": false}, 30},
		{"string exponent", map[string]any{"url": "http://x", "timeout": "1e2"}, 100},
		{"fraction truncates", map[string]any{"url": "http://x", "timeout": float64(120.9)}, 120},
		{"infinity is not finite", map[string]any{"url": "http://x", "timeout": "Infinity"}, 30},
		{"negative infinity defaults", map[string]any{"url": "http://x", "timeout": "-Infinity"}, 30},
		{"negative defaults", map[string]any{"url": "http://x", "timeout": float64(-5)}, 30},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseInput(test.args).timeout; got != test.want {
				t.Fatalf("timeout = %d, want %d", got, test.want)
			}
		})
	}
}

// TestChainFixture is a guard so the redirect fixture keeps its contract.
func TestChainFixture(t *testing.T) {
	server := newFixtureServer(t)
	response, err := http.Get(server.URL + "/chain?n=0")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "chain end" {
		t.Fatalf("body = %q", body)
	}
}
