// Package websearch ports src/tools/websearch.ts: the cave__websearch tool
// that calls the Exa or Parallel MCP endpoints over JSON-RPC.
package websearch

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// NoResults is returned when a provider payload carries no text.
const NoResults = "No search results found. Please try a different query."

// ExaURL and ParallelURL are the fixed MCP endpoints.
const (
	ExaURL      = "https://mcp.exa.ai/mcp"
	ParallelURL = "https://search.parallel.ai/mcp"
)

// Limits shared with the tool schema.
const (
	MaxNumResults         = 20
	MaxContextCharacters  = 50_000
	MaxResponseBytes      = 256 * 1024
	DefaultTimeoutSeconds = 25
	MaxTimeoutSeconds     = 120
)

// provider is the search backend.
type provider = string

const (
	providerExa      provider = "exa"
	providerParallel provider = "parallel"
)

// webSearchInput is WebSearchInput.
type webSearchInput struct {
	query                string
	provider             provider
	numResults           int
	livecrawl            string
	typ                  string
	contextMaxCharacters *int
	timeout              int
	redactSecrets        bool
}

const (
	// maxRedirects is Node fetch's redirect limit (undici follows 20 hops and
	// fails the request that would need a 21st).
	maxRedirects = 20
	// acceptEncoding is what Node fetch advertises. "br" is not advertised:
	// the standard library has no Brotli decoder and no new dependency is
	// allowed.
	acceptEncoding = "gzip, deflate"
)

var (
	// httpClient issues JSON-RPC requests. Tests may replace it.
	httpClient          = &http.Client{}
	errFetchFailed      = errors.New("fetch failed")
	errOperationAborted = errors.New("This operation was aborted")
)

// doRequest applies the fetch redirect policy to the package client. Tests
// replace httpClient, so the policy must be installed per request.
func doRequest(request *http.Request) (*http.Response, error) {
	client := *httpClient
	client.CheckRedirect = checkRedirect
	return client.Do(request)
}

// checkRedirect mirrors Node fetch: at most twenty followed redirects, and
// Authorization is dropped when a hop changes origin (scheme, host or port).
func checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errors.New("redirect count exceeded")
	}
	if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, request.URL) {
		request.Header.Del("Authorization")
	}
	return nil
}

// sameOrigin is the fetch "same origin" test (ports are part of the origin).
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// contentEncoding normalizes the Content-Encoding header; an empty result means
// no decoding is needed.
func contentEncoding(header http.Header) string {
	value := strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding")))
	if value == "" || value == "identity" {
		return ""
	}
	return value
}

// decodedBody wraps a response body with the matching standard-library
// decompressor. Unsupported encodings (notably "br") pass through unchanged:
// Brotli has no standard-library decoder and no new dependency is allowed.
func decodedBody(body io.Reader, encoding string) (io.Reader, func(), error) {
	switch encoding {
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(body)
		if err != nil {
			return nil, nil, err
		}
		return reader, func() { _ = reader.Close() }, nil
	case "deflate":
		buffered := bufio.NewReader(body)
		header, _ := buffered.Peek(2)
		if isZlibHeader(header) {
			reader, err := zlib.NewReader(buffered)
			if err != nil {
				return nil, nil, err
			}
			return reader, func() { _ = reader.Close() }, nil
		}
		// HTTP "deflate" is zlib-wrapped by the spec, but raw DEFLATE is
		// common; Node's fetch accepts both.
		reader := flate.NewReader(buffered)
		return reader, func() { _ = reader.Close() }, nil
	default:
		return body, func() {}, nil
	}
}

// isZlibHeader recognizes the two-byte zlib wrapper (RFC 1950).
func isZlibHeader(header []byte) bool {
	if len(header) < 2 {
		return false
	}
	cmf, flg := header[0], header[1]
	return cmf&0x0F == 8 && (uint16(cmf)<<8|uint16(flg))%31 == 0
}

// Tool is the cave__websearch tool definition. Annotations, description and
// inputSchema hold the exact JSON src/tools/websearch.ts declares.
// port of src/tools/websearch.ts:websearchTool
var Tool = types.ToolDefinition{
	Name:        "cave__websearch",
	Annotations: json.RawMessage(`{"readOnlyHint":true,"openWorldHint":true}`),
	Description: "Use instead of WebSearch (Exa/Parallel); results redacted and budget-compressed. Current year: " + strconv.Itoa(time.Now().Year()) + ".",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"provider":{"type":"string","enum":["exa","parallel"]},"numResults":{"type":"number","description":"Default 8, max 20"},"livecrawl":{"type":"string","enum":["fallback","preferred"],"description":"Exa only"},"type":{"type":"string","enum":["auto","fast","deep"],"description":"Exa only"},"contextMaxCharacters":{"type":"number","description":"Max 50000"},"timeout":{"type":"number","description":"Seconds, max 120"},"redact_secrets":{"type":"boolean","description":"false skips redaction"}},"required":["query"]}`),
	Handler:     handler,
}

// checksum is the SHA-1 hex digest used for Parallel session ids and the
// default provider split.
// port of src/tools/websearch.ts:checksum
func checksum(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// truthy reports whether the environment variable is one of 1/true/yes/on.
// port of src/tools/websearch.ts:truthy
func truthy(ctx context.Context, name string) bool {
	value := strings.ToLower(envValue(ctx, name))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

// envProvider returns the provider forced by the environment, if any.
// port of src/tools/websearch.ts:envProvider
func envProvider(ctx context.Context) provider {
	value := envValue(ctx, "CAVE_TOOLS_WEBSEARCH_PROVIDER")
	if value == "" {
		value = envValue(ctx, "OPENCODE_WEBSEARCH_PROVIDER")
	}
	if value == providerExa || value == providerParallel {
		return value
	}
	return ""
}

// SelectProvider picks the backend: explicit argument, environment override,
// enable flags, then a stable checksum split like opencode's. ctx replaces
// TS's implicit getEnv() AsyncLocalStorage lookup.
// port of src/tools/websearch.ts:selectProvider
func SelectProvider(ctx context.Context, query string, override provider) provider {
	if override != "" {
		return override
	}
	if env := envProvider(ctx); env != "" {
		return env
	}
	if truthy(ctx, "CAVE_TOOLS_ENABLE_PARALLEL") || truthy(ctx, "OPENCODE_ENABLE_PARALLEL") {
		return providerParallel
	}
	if truthy(ctx, "CAVE_TOOLS_ENABLE_EXA") || truthy(ctx, "OPENCODE_ENABLE_EXA") || truthy(ctx, "OPENCODE_EXPERIMENTAL_EXA") {
		return providerExa
	}
	// Stable default mirrors opencode's split without needing a session id.
	digest := checksum(query)
	parsed, _ := strconv.ParseUint(digest[:8], 16, 64)
	if parsed%2 == 0 {
		return providerExa
	}
	return providerParallel
}

// exaUrl appends the API key to the fixed Exa endpoint.
// port of src/tools/websearch.ts:exaUrl
func exaUrl(apiKey string) string {
	if apiKey == "" {
		return ExaURL
	}
	return ExaURL + "?exaApiKey=" + searchParamsEncode(apiKey)
}

// searchParamsEncode is URLSearchParams' application/x-www-form-urlencoded
// serializer: alphanumerics and *-._ stay, space becomes "+".
func searchParamsEncode(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			out.WriteByte(c)
		case c == ' ':
			out.WriteByte('+')
		default:
			out.WriteByte('%')
			out.WriteByte(hexDigits[c>>4])
			out.WriteByte(hexDigits[c&0x0f])
		}
	}
	return out.String()
}

// parsePayload extracts the first non-empty text block of one JSON-RPC body.
// port of src/tools/websearch.ts:parsePayload
func parsePayload(payload string) (string, bool) {
	trimmed := jsre.Trim(payload)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", false
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		return "", false
	}
	result, ok := root["result"].(map[string]any)
	if !ok {
		return "", false
	}
	content, ok := result["content"].([]any)
	if !ok {
		return "", false
	}
	for _, entry := range content {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		text, ok := object["text"].(string)
		if ok && text != "" {
			return text, true
		}
	}
	return "", false
}

// ParseWebsearchResponse accepts either a plain JSON-RPC body or an SSE stream
// and returns the first text block.
// port of src/tools/websearch.ts:parseWebsearchResponse
func ParseWebsearchResponse(body string) (string, bool) {
	if direct, ok := parsePayload(body); ok {
		return direct, true
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if data, ok := parsePayload(line[6:]); ok {
			return data, true
		}
	}
	return "", false
}

// collectBoundedResponse reads the response body under the 256KB cap.
// port of src/tools/websearch.ts:collectBoundedResponse
func collectBoundedResponse(response *http.Response) (string, error) {
	// Node's fetch removes Content-Length once it decodes a response, so the
	// declared check only applies to identity bodies.
	encoding := contentEncoding(response.Header)
	if encoding == "" {
		if declared := response.Header.Get("content-length"); declared != "" {
			if parsed, ok := parseDecimalPrefix(declared); ok && isSafeInteger(parsed) && parsed > MaxResponseBytes {
				return "", fmt.Errorf("websearch response exceeded %d bytes", MaxResponseBytes)
			}
		}
	}
	body := io.Reader(response.Body)
	if encoding != "" {
		decoded, closeDecoder, err := decodedBody(response.Body, encoding)
		if err != nil {
			return "", err
		}
		defer closeDecoder()
		body = decoded
	}
	// Stop at cap+1 so an oversized body is never buffered in full.
	data, err := io.ReadAll(io.LimitReader(body, MaxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxResponseBytes {
		return "", fmt.Errorf("websearch response exceeded %d bytes", MaxResponseBytes)
	}
	return decodeNodeUTF8(data), nil
}

// callMcp performs one JSON-RPC tools/call request against a fixed endpoint.
// port of src/tools/websearch.ts:callMcp
func callMcp(ctx context.Context, url string, tool string, args any, headers map[string]string, timeoutSeconds int) (string, bool, error) {
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	body, err := marshalRequest(tool, args)
	if err != nil {
		return "", false, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Encoding", acceptEncoding)
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := doRequest(request)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return "", false, errOperationAborted
		}
		return "", false, errFetchFailed
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", false, fmt.Errorf("websearch request failed with HTTP %d", response.StatusCode)
	}
	payload, err := collectBoundedResponse(response)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return "", false, errOperationAborted
		}
		return "", false, err
	}
	text, ok := ParseWebsearchResponse(payload)
	if !ok {
		return "", false, nil
	}
	return text, true, nil
}

// mcpRequest is the JSON-RPC envelope Node's JSON.stringify emits, in order.
type mcpRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Method  string    `json:"method"`
	Params  mcpParams `json:"params"`
}

type mcpParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type exaArguments struct {
	Query                jsonString `json:"query"`
	Type                 jsonString `json:"type"`
	NumResults           int        `json:"numResults"`
	Livecrawl            jsonString `json:"livecrawl"`
	ContextMaxCharacters *int       `json:"contextMaxCharacters,omitempty"`
}

type parallelArguments struct {
	Objective     jsonString   `json:"objective"`
	SearchQueries []jsonString `json:"search_queries"`
	SessionID     jsonString   `json:"session_id"`
}

// jsonString marshals exactly like JSON.stringify: no HTML escaping and
// U+2028/U+2029 left raw (encoding/json escapes them).
type jsonString string

func (value jsonString) MarshalJSON() ([]byte, error) {
	return []byte(quoteJSONString(string(value))), nil
}

// quoteJSONString is JSON.stringify's string quoting: quote, backslash,
// the usual control escapes and other C0 controls are escaped; everything
// else stays raw.
func quoteJSONString(value string) string {
	var out strings.Builder
	out.Grow(len(value) + 2)
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				out.WriteString(`\u`)
				out.WriteString(strconv.FormatInt(int64(r)+0x10000, 16)[1:])
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// marshalRequest encodes the JSON-RPC body like JSON.stringify: no HTML
// escaping and an omitted contextMaxCharacters when unset.
func marshalRequest(tool string, args any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(mcpRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: mcpParams{Name: tool, Arguments: args}})
	if err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// positiveInt is positiveInt(value, fallback, max): Number(value) || fallback,
// then clamp into [1, max] after truncation.
// port of src/tools/websearch.ts:positiveInt
func positiveInt(value any, fallback int, max int) int {
	number := jsNumber(value)
	if math.IsNaN(number) || number == 0 {
		number = float64(fallback)
	}
	return int(math.Max(1, math.Min(math.Trunc(number), float64(max))))
}

// parseInput normalizes the handler arguments.
// port of src/tools/websearch.ts:parseInput
func parseInput(args map[string]any) webSearchInput {
	input := webSearchInput{
		query:         jsre.Trim(jsString(jsOr(args["query"], ""))),
		numResults:    positiveInt(args["numResults"], 8, MaxNumResults),
		livecrawl:     "fallback",
		typ:           "auto",
		timeout:       positiveInt(args["timeout"], DefaultTimeoutSeconds, MaxTimeoutSeconds),
		redactSecrets: !jsIsFalse(args["redact_secrets"]),
	}
	if value, ok := args["provider"].(string); ok && (value == providerExa || value == providerParallel) {
		input.provider = value
	}
	if jsStringEquals(args["livecrawl"], "preferred") {
		input.livecrawl = "preferred"
	}
	if jsStringEquals(args["type"], "fast") || jsStringEquals(args["type"], "deep") {
		input.typ = args["type"].(string)
	}
	// undefined omits the argument; an explicit null reaches positiveInt as
	// Number(null) === 0, which falls back to 10000 (a present argument).
	if value, present := args["contextMaxCharacters"]; present {
		clamped := positiveInt(value, 10_000, MaxContextCharacters)
		input.contextMaxCharacters = &clamped
	}
	return input
}

// handler is websearchTool.handler.
// port of src/tools/websearch.ts:websearchTool.handler
func handler(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	input := parseInput(args)
	if input.query == "" {
		return errorResult("Error: query is required"), nil
	}
	selected := SelectProvider(ctx, input.query, input.provider)

	var text string
	var found bool
	var err error
	if selected == providerExa {
		text, found, err = callMcp(
			ctx,
			exaUrl(envValue(ctx, "EXA_API_KEY")),
			"web_search_exa",
			exaArguments{
				Query:                jsonString(input.query),
				Type:                 jsonString(input.typ),
				NumResults:           input.numResults,
				Livecrawl:            jsonString(input.livecrawl),
				ContextMaxCharacters: input.contextMaxCharacters,
			},
			nil,
			input.timeout,
		)
	} else {
		headers := map[string]string{"User-Agent": "cave-tools"}
		if apiKey := envValue(ctx, "PARALLEL_API_KEY"); apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
		text, found, err = callMcp(
			ctx,
			ParallelURL,
			"web_search",
			parallelArguments{
				Objective:     jsonString(input.query),
				SearchQueries: []jsonString{jsonString(input.query)},
				SessionID:     jsonString(checksum(input.query)),
			},
			headers,
			input.timeout,
		)
	}
	if err != nil {
		return errorResult("Unable to search the web for " + input.query + ": " + err.Error()), nil
	}

	output := NoResults
	if found && text != "" {
		output = text
	}
	if input.redactSecrets {
		output = compression.RedactSecrets(output)
	}
	full := output
	output = compression.ApplyBudget(ctx, output, "web")
	handle := compression.ArchiveHandle(full, output, "websearch:"+selected+":"+input.query)
	return types.ToolResult{Content: []types.Content{types.Text(output + handle)}}, nil
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

// envValue reads one variable from the call environment.
func envValue(ctx context.Context, name string) string {
	return runtime.GetEnv(ctx)[name]
}

// decodeNodeUTF8 decodes UTF-8 like Node's `Buffer.toString("utf-8")`: every
// ill-formed maximal subpart becomes U+FFFD and, unlike TextDecoder, a leading
// BOM is kept. websearch.ts decodes with Buffer.toString, not TextDecoder.
func decodeNodeUTF8(data []byte) string {
	var out strings.Builder
	out.Grow(len(data))
	for i := 0; i < len(data); {
		first := data[i]
		if first < 0x80 {
			out.WriteByte(first)
			i++
			continue
		}

		need := 0
		low, high := byte(0x80), byte(0xBF)
		switch {
		case first >= 0xC2 && first <= 0xDF:
			need = 1
		case first == 0xE0:
			need = 2
			low = 0xA0
		case first >= 0xE1 && first <= 0xEC:
			need = 2
		case first == 0xED:
			need = 2
			high = 0x9F
		case first >= 0xEE && first <= 0xEF:
			need = 2
		case first == 0xF0:
			need = 3
			low = 0x90
		case first >= 0xF1 && first <= 0xF3:
			need = 3
		case first == 0xF4:
			need = 3
			high = 0x8F
		default:
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}

		next := i + 1
		valid := true
		for consumed := 0; consumed < need; consumed++ {
			if next >= len(data) {
				valid = false
				break
			}
			current := data[next]
			deltaLow, deltaHigh := byte(0x80), byte(0xBF)
			if consumed == 0 {
				deltaLow, deltaHigh = low, high
			}
			if current < deltaLow || current > deltaHigh {
				valid = false
				break
			}
			next++
		}
		if !valid {
			out.WriteRune(utf8.RuneError)
			i = next
			continue
		}
		out.Write(data[i:next])
		i = next
	}
	return out.String()
}

// jsString mirrors String(value) for the JSON value shapes handlers receive.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return "undefined"
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberToString(v)
	case float32:
		return jsNumberToString(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return v.String()
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item == nil {
				continue
			}
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// jsOr is JavaScript's `value || fallback`.
func jsOr(value any, fallback any) any {
	if jsTruthy(value) {
		return value
	}
	return fallback
}

// jsStringEquals is `value === want` for a string operand, without panicking
// on non-comparable dynamic types.
func jsStringEquals(value any, want string) bool {
	text, ok := value.(string)
	return ok && text == want
}

// jsIsFalse is `value === false`.
func jsIsFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

// jsTruthy mirrors JavaScript truthiness for the JSON value shapes handlers
// receive.
func jsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	case float32:
		return v != 0 && !math.IsNaN(float64(v))
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return true
	}
}

// jsNumber is JavaScript's Number(value): NaN when the value is not numeric.
// A nil value is JSON null (Number(null) === 0); positiveInt treats 0 and NaN
// the same, matching `Number(value) || fallback`.
func jsNumber(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case bool:
		if v {
			return 1
		}
		return 0
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		number, err := v.Float64()
		if err != nil {
			return math.NaN()
		}
		return number
	case string:
		return numberFromString(v)
	case []any:
		// Number(object) runs ToPrimitive: Array's toString is join(","), so
		// [] and [null] become "" (0) and [1,2] becomes "1,2" (NaN).
		return numberFromString(jsString(v))
	default:
		return math.NaN()
	}
}

var (
	decimalNumberRE = jsre.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`, "")
	hexNumberRE     = jsre.MustCompile(`^0[xX][0-9a-fA-F]+$`, "")
	octalNumberRE   = jsre.MustCompile(`^0[oO][0-7]+$`, "")
	binaryNumberRE  = jsre.MustCompile(`^0[bB][01]+$`, "")
)

// numberFromString implements ToNumber for strings.
func numberFromString(value string) float64 {
	trimmed := jsre.Trim(value)
	if trimmed == "" {
		return 0
	}
	switch trimmed {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if hexNumberRE.MatchString(trimmed) {
		number, err := strconv.ParseUint(trimmed[2:], 16, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(number)
	}
	if octalNumberRE.MatchString(trimmed) {
		number, err := strconv.ParseUint(trimmed[2:], 8, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(number)
	}
	if binaryNumberRE.MatchString(trimmed) {
		number, err := strconv.ParseUint(trimmed[2:], 2, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(number)
	}
	if !decimalNumberRE.MatchString(trimmed) {
		return math.NaN()
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	return number
}

// parseDecimalPrefix is Number.parseInt(value, 10).
func parseDecimalPrefix(value string) (float64, bool) {
	index := 0
	for index < len(value) {
		r, size := utf8.DecodeRuneInString(value[index:])
		if !jsre.IsSpace(r) {
			break
		}
		index += size
	}
	start := index
	if index < len(value) && (value[index] == '+' || value[index] == '-') {
		index++
	}
	digits := index
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == digits {
		return 0, false
	}
	number, err := strconv.ParseFloat(value[start:index], 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return number, true
}

// isSafeInteger is Number.isSafeInteger.
func isSafeInteger(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value == math.Trunc(value) && math.Abs(value) <= 9007199254740991
}

// jsNumberToString formats a float64 like ECMAScript Number::toString(10).
func jsNumberToString(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 0) {
		if value > 0 {
			return "Infinity"
		}
		return "-Infinity"
	}
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}

	repr := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentPart, _ := strings.Cut(repr, "e")
	exponent, _ := strconv.Atoi(exponentPart)
	digits := strings.Replace(mantissa, ".", "", 1)
	n := len(digits)
	k := exponent + 1

	var out string
	switch {
	case k > 0 && k <= 21:
		if n <= k {
			out = digits + strings.Repeat("0", k-n)
		} else {
			out = digits[:k] + "." + digits[k:]
		}
	case k <= 0 && k > -6:
		out = "0." + strings.Repeat("0", -k) + digits
	default:
		if n == 1 {
			out = digits
		} else {
			out = digits[:1] + "." + digits[1:]
		}
		exponentOut := k - 1
		if exponentOut >= 0 {
			out += "e+" + strconv.Itoa(exponentOut)
		} else {
			out += "e-" + strconv.Itoa(-exponentOut)
		}
	}
	if negative {
		out = "-" + out
	}
	return out
}
