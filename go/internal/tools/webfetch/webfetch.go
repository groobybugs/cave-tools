// Package webfetch ports src/tools/webfetch.ts: the cave__webfetch tool that
// fetches http(s) URLs and returns text, hand-converted markdown or raw HTML.
package webfetch

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
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
	"github.com/groobybugs/cave-tools/go/internal/types"
)

const maxResponseBytes = 5 * 1024 * 1024 // 5MB
const defaultTimeoutSeconds = 30
const maxTimeoutSeconds = 120

// opencodeUserAgent mirrors opencode 2.0 (openCodeUserAgent): an honest bot
// string rather than a browser impersonation.
const opencodeUserAgent = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; OpenCode-User/1.0; +https://opencode.ai"

// webFetchInput is WebFetchInput.
type webFetchInput struct {
	url           string
	format        string
	timeout       int
	redactSecrets bool
}

// maxRedirects is Node fetch's redirect limit (undici follows 20 hops and
// fails the request that would need a 21st).
const maxRedirects = 20

// acceptEncoding is the set Node fetch advertises. The standard library has no
// Brotli decoder, so "br" is deliberately not advertised.
const acceptEncoding = "gzip, deflate"

// httpClient issues fetch requests. Tests may point it at a local server.
var httpClient = &http.Client{}

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

// supportedEncoding reports whether decodedBody can decode the encoding.
func supportedEncoding(encoding string) bool {
	switch encoding {
	case "gzip", "x-gzip", "deflate":
		return true
	default:
		return false
	}
}

// tolerantGzipReader matches Node's zlib streaming: a body cut before the
// trailer yields the decompressed prefix without an error, while checksum and
// header errors still fail the read as "terminated".
type tolerantGzipReader struct {
	reader *gzip.Reader
}

func (reader *tolerantGzipReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return count, err
}

// decodedBody wraps a response body with the matching standard-library
// decompressor. Unsupported encodings (notably "br") pass through unchanged:
// Brotli has no standard-library decoder and no new dependency is allowed.
func decodedBody(body io.Reader, encoding string) (io.Reader, func(), error) {
	switch encoding {
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(body)
		if err != nil {
			// Node treats an empty/incomplete gzip header as an empty body.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return strings.NewReader(""), func() {}, nil
			}
			return nil, nil, errTerminated
		}
		tolerant := &tolerantGzipReader{reader: reader}
		return tolerant, func() { _ = reader.Close() }, nil
	case "deflate":
		buffered := bufio.NewReader(body)
		header, _ := buffered.Peek(2)
		if isZlibHeader(header) {
			reader, err := zlib.NewReader(buffered)
			if err != nil {
				return nil, nil, errTerminated
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

var (
	errFetchFailed       = errors.New("fetch failed")
	errTerminated        = errors.New("terminated")
	errResponseTooLarge  = errors.New("Response too large (exceeds 5MB limit)")
	schemeRE             = jsre.MustCompile("^https?://", "i")
	nbspEntityRE         = jsre.MustCompile("&nbsp;", "gi")
	ampEntityRE          = jsre.MustCompile("&amp;", "gi")
	ltEntityRE           = jsre.MustCompile("&lt;", "gi")
	gtEntityRE           = jsre.MustCompile("&gt;", "gi")
	quotEntityRE         = jsre.MustCompile("&quot;", "gi")
	apos39EntityRE       = jsre.MustCompile("&#39;", "gi")
	aposEntityRE         = jsre.MustCompile("&apos;", "gi")
	decimalEntityRE      = jsre.MustCompile("&#(\\d+);", "g")
	hexEntityRE          = jsre.MustCompile("&#x([0-9a-f]+);", "gi")
	attrsRE              = jsre.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:.-]*)\s*(?:=\s*"([^"]*)"|\s*=\s*'([^']*)'|\s*=\s*([^\s>]+))?`, "g")
	trailingSpaceBeforeN = jsre.MustCompile("[ \t]+\n", "g")
	threeNewlinesRE      = jsre.MustCompile("\n{3,}", "g")
	twoSpaceRunsRE       = jsre.MustCompile("[ \t]{2,}", "g")
)

// Tool is the cave__webfetch tool definition. Annotations, description and
// inputSchema hold the exact JSON src/tools/webfetch.ts declares.
// port of src/tools/webfetch.ts:webfetchTool
var Tool = types.ToolDefinition{
	Name:        "cave__webfetch",
	Annotations: json.RawMessage(`{"readOnlyHint":true,"openWorldHint":true}`),
	Description: "Use instead of WebFetch. Returns markdown/text/html; non-text content rejected, non-2xx is an error. Follows redirects; any http(s) URL incl. localhost.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"format":{"type":"string","enum":["text","markdown","html"],"description":"Default markdown"},"timeout":{"type":"number","description":"Seconds, max 120"},"redact_secrets":{"type":"boolean","description":"false skips redaction"}},"required":["url"]}`),
	Handler:     handler,
}

// acceptFor returns the Accept header for a response format.
// port of src/tools/webfetch.ts:acceptFor
func acceptFor(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	}
	return ""
}

// parseInput normalizes the handler arguments.
// port of src/tools/webfetch.ts:parseInput
func parseInput(args map[string]any) webFetchInput {
	url := jsre.Trim(jsString(jsOr(args["url"], "")))
	format := "markdown"
	if jsStringEquals(args["format"], "text") || jsStringEquals(args["format"], "html") {
		format = args["format"].(string)
	}

	timeout := defaultTimeoutSeconds
	// Missing is Number(undefined) = NaN; an explicit JSON null is Number(null)
	// = 0. Both fall back to the default here, but keep the distinction local.
	rawTimeout := math.NaN()
	if value, present := args["timeout"]; present {
		rawTimeout = jsNumber(value)
	}
	if !math.IsNaN(rawTimeout) && !math.IsInf(rawTimeout, 0) && rawTimeout > 0 {
		timeout = int(math.Min(math.Trunc(rawTimeout), maxTimeoutSeconds))
	}
	return webFetchInput{
		url:           url,
		format:        format,
		timeout:       timeout,
		redactSecrets: !jsIsFalse(args["redact_secrets"]),
	}
}

// isCloudflareChallenge detects Cloudflare's bot challenge response.
// port of src/tools/webfetch.ts:isCloudflareChallenge
func isCloudflareChallenge(response *http.Response) bool {
	return response.StatusCode == 403 && response.Header.Get("cf-mitigated") == "challenge"
}

// cloudflareChallengeError is CloudflareChallengeError.
// port of src/tools/webfetch.ts:CloudflareChallengeError
type cloudflareChallengeError struct{}

func (*cloudflareChallengeError) Error() string { return "Cloudflare bot challenge" }

// fetchResult is FetchResult.
type fetchResult struct {
	buffer      []byte
	contentType string
}

// fetchBounded performs the GET with a hard timeout and a 5MB body cap.
// port of src/tools/webfetch.ts:fetchBounded
func fetchBounded(ctx context.Context, url string, headers map[string]string, timeoutSeconds int) (fetchResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil || request.URL.Host == "" {
		// Node's fetch rejects unparseable URLs before touching the network.
		return fetchResult{}, fmt.Errorf("Failed to parse URL from %s", url)
	}
	if user := request.URL.User; user != nil {
		username := user.Username()
		password, _ := user.Password()
		if username != "" || password != "" {
			return fetchResult{}, fmt.Errorf(
				"Request cannot be constructed from a URL that includes credentials: %s",
				request.URL.String(),
			)
		}
		// Node accepts empty userinfo but sends no Authorization header;
		// net/http would otherwise build one.
		request.URL.User = nil
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	// Set the header explicitly: net/http then leaves the body compressed and
	// this package owns decoding, so the size cap counts decoded bytes.
	if request.Header.Get("Accept-Encoding") == "" {
		request.Header.Set("Accept-Encoding", acceptEncoding)
	}

	response, err := doRequest(request)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return fetchResult{}, fmt.Errorf("Request timed out after %ds", timeoutSeconds)
		}
		// Node's fetch reports every transport-level failure as "fetch failed".
		return fetchResult{}, errFetchFailed
	}
	defer func() { _ = response.Body.Close() }()

	// Cloudflare bot challenge: caller retries with honest UA.
	if isCloudflareChallenge(response) {
		return fetchResult{}, &cloudflareChallengeError{}
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fetchResult{}, fmt.Errorf("Request failed with status %d", response.StatusCode)
	}

	// The declared length is checked before any decoding: Node 24 keeps
	// Content-Length on gzip responses and TS rejects it unconditionally.
	if declared := response.Header.Get("content-length"); declared != "" {
		if parsed, ok := parseDecimalPrefix(declared); ok && isSafeInteger(parsed) && parsed > maxResponseBytes {
			return fetchResult{}, errResponseTooLarge
		}
	}
	encoding := contentEncoding(response.Header)
	decoding := supportedEncoding(encoding)
	body := io.Reader(response.Body)
	closeDecoder := func() {}
	if encoding != "" {
		decoded, closeFn, decodeErr := decodedBody(response.Body, encoding)
		if decodeErr != nil {
			return fetchResult{}, decodeErr
		}
		body, closeDecoder = decoded, closeFn
	}
	buffer, err := readBoundedBody(body)
	closeDecoder()
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return fetchResult{}, fmt.Errorf("Request timed out after %ds", timeoutSeconds)
		}
		if errors.Is(err, errResponseTooLarge) {
			return fetchResult{}, errResponseTooLarge
		}
		if decoding {
			return fetchResult{}, errTerminated
		}
		return fetchResult{}, err
	}
	return fetchResult{buffer: buffer, contentType: response.Header.Get("content-type")}, nil
}

// readBoundedBody reads at most MAX_RESPONSE_BYTES from a response body.
// port of src/tools/webfetch.ts:readBoundedBody
func readBoundedBody(body io.Reader) ([]byte, error) {
	buffer, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(buffer) > maxResponseBytes {
		return nil, errResponseTooLarge
	}
	return buffer, nil
}

// isTextualMime reports whether a MIME type is safe to decode as text.
// port of src/tools/webfetch.ts:isTextualMime
func isTextualMime(mime string) bool {
	return mime == "" ||
		strings.HasPrefix(mime, "text/") ||
		mime == "application/json" ||
		strings.HasSuffix(mime, "+json") ||
		mime == "application/xml" ||
		strings.HasSuffix(mime, "+xml") ||
		mime == "application/javascript" ||
		mime == "application/x-javascript"
}

// tagToken is TagToken.
type tagToken struct {
	tag     string
	attrs   map[string]string
	closing bool
	self    bool
}

// htmlToken is one text or tag token from the tokenizer.
type htmlToken struct {
	isText bool
	text   string
	tag    tagToken
}

var containerSkipTags = map[string]struct{}{
	"script": {}, "style": {}, "noscript": {}, "iframe": {}, "object": {},
}

var voidTags = map[string]struct{}{
	"meta": {}, "link": {}, "base": {}, "img": {}, "input": {}, "area": {},
	"source": {}, "track": {}, "col": {}, "wbr": {}, "embed": {},
}

var blockTags = map[string]struct{}{
	"p": {}, "div": {}, "section": {}, "article": {}, "header": {}, "footer": {},
	"main": {}, "aside": {}, "li": {}, "tr": {}, "blockquote": {},
}

var headingTags = map[string]string{
	"h1": "#", "h2": "##", "h3": "###", "h4": "####", "h5": "#####", "h6": "######",
}

func hasTag(tags map[string]struct{}, tag string) bool {
	_, ok := tags[tag]
	return ok
}

// parseAttrs extracts the attributes of one tag body.
// port of src/tools/webfetch.ts:parseAttrs
func parseAttrs(span string) map[string]string {
	attrs := map[string]string{}
	for _, match := range attrsRE.FindAllStringSubmatch(span, -1) {
		value := ""
		switch {
		case match[2] != "":
			value = match[2]
		case match[3] != "":
			value = match[3]
		case match[4] != "":
			value = match[4]
		}
		attrs[strings.ToLower(match[1])] = value
	}
	return attrs
}

// tokenizeHtml splits HTML into text and tag tokens.
// port of src/tools/webfetch.ts:tokenizeHtml
func tokenizeHtml(html string) []htmlToken {
	tokens := make([]htmlToken, 0, 16)
	i := 0
	for i < len(html) {
		lt := strings.Index(html[i:], "<")
		if lt == -1 {
			tokens = append(tokens, htmlToken{isText: true, text: html[i:]})
			break
		}
		lt += i
		if lt > i {
			tokens = append(tokens, htmlToken{isText: true, text: html[i:lt]})
		}
		gt := strings.Index(html[lt:], ">")
		if gt == -1 {
			tokens = append(tokens, htmlToken{isText: true, text: html[lt:]})
			break
		}
		gt += lt
		inner := html[lt+1 : gt]
		closing := strings.HasPrefix(inner, "/")
		body := inner
		if closing {
			body = inner[1:]
		}
		self := strings.HasSuffix(body, "/")
		tagBody := body
		if self {
			tagBody = body[:len(body)-1]
		}

		space, spaceSize := searchSpace(tagBody)
		var tag string
		attrs := map[string]string{}
		if space == -1 {
			tag = strings.ToLower(tagBody)
		} else {
			tag = strings.ToLower(tagBody[:space])
			attrs = parseAttrs(tagBody[space+spaceSize:])
		}
		tokens = append(tokens, htmlToken{tag: tagToken{tag: tag, attrs: attrs, closing: closing, self: self}})
		i = gt + 1
	}
	return tokens
}

// searchSpace is s.search(/\s/): the byte offset and width of the first
// JavaScript-whitespace code point, or -1.
func searchSpace(s string) (int, int) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if jsre.IsSpace(r) {
			return i, size
		}
		i += size
	}
	return -1, 0
}

// decodeEntities resolves the named, decimal and hex entities the TS
// converter knows.
// port of src/tools/webfetch.ts:decodeEntities
func decodeEntities(text string) string {
	text = nbspEntityRE.ReplaceAllString(text, " ")
	text = ampEntityRE.ReplaceAllString(text, "&")
	text = ltEntityRE.ReplaceAllString(text, "<")
	text = gtEntityRE.ReplaceAllString(text, ">")
	text = quotEntityRE.ReplaceAllString(text, `"`)
	text = apos39EntityRE.ReplaceAllString(text, "'")
	text = aposEntityRE.ReplaceAllString(text, "'")
	text = decimalEntityRE.ReplaceAllStringFunc(text, func(match string) string {
		number, err := strconv.ParseFloat(match[2:len(match)-1], 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return ""
		}
		if number > 0x10ffff {
			return ""
		}
		return string(rune(int(number)))
	})
	text = hexEntityRE.ReplaceAllStringFunc(text, func(match string) string {
		number, err := strconv.ParseUint(match[3:len(match)-1], 16, 64)
		if err != nil || number > 0x10ffff {
			return ""
		}
		return string(rune(number))
	})
	return text
}

// extractTextFromHtml drops skip tags and emits the remaining text.
// port of src/tools/webfetch.ts:extractTextFromHtml
func extractTextFromHtml(html string) string {
	tokens := tokenizeHtml(html)
	var out strings.Builder
	lastByte := byte(0)
	skipDepth := 0
	for _, token := range tokens {
		if !token.isText {
			tag := token.tag.tag
			if hasTag(voidTags, tag) {
				continue
			}
			if hasTag(containerSkipTags, tag) {
				if !token.tag.closing {
					skipDepth++
				} else if skipDepth > 0 {
					skipDepth--
				}
			} else if !token.tag.closing && hasTag(blockTags, tag) && out.Len() > 0 && lastByte != '\n' {
				out.WriteByte('\n')
				lastByte = '\n'
			}
			continue
		}
		if skipDepth == 0 {
			decoded := decodeEntities(token.text)
			out.WriteString(decoded)
			if len(decoded) > 0 {
				lastByte = decoded[len(decoded)-1]
			}
		}
	}
	text := threeNewlinesRE.ReplaceAllString(out.String(), "\n\n")
	return jsre.Trim(text)
}

// convertHtmlToMarkdown renders a lightweight markdown subset: headings,
// links, lists, emphasis, code, blockquotes and rules.
// port of src/tools/webfetch.ts:convertHtmlToMarkdown
func convertHtmlToMarkdown(html string) string {
	tokens := tokenizeHtml(html)
	out := make([]string, 0, 16)
	skipDepth := 0
	preDepth := 0
	type listFrame struct {
		ordered bool
		index   int
	}
	listStack := make([]listFrame, 0, 4)
	linkHrefStack := make([]string, 0, 4)

	pushText := func(text string) {
		if skipDepth > 0 {
			return
		}
		if decoded := decodeEntities(text); decoded != "" {
			out = append(out, decoded)
		}
	}

	lastIsNewline := func() bool {
		return len(out) > 0 && out[len(out)-1] == "\n"
	}

	for _, token := range tokens {
		if token.isText {
			pushText(token.text)
			continue
		}
		tag := token.tag.tag
		attrs := token.tag.attrs
		closing := token.tag.closing
		self := token.tag.self
		if hasTag(voidTags, tag) {
			continue
		}
		if hasTag(containerSkipTags, tag) {
			if !closing && !self {
				skipDepth++
			} else if closing && skipDepth > 0 {
				skipDepth--
			}
			continue
		}
		if skipDepth > 0 {
			continue
		}

		if marker, ok := headingTags[tag]; ok && !closing {
			out = append(out, "\n\n"+marker+" ")
		} else if tag == "br" && !closing {
			out = append(out, "\n")
		} else if tag == "a" && !closing && attrs["href"] != "" {
			linkHrefStack = append(linkHrefStack, attrs["href"])
			out = append(out, "[")
		} else if tag == "a" && closing {
			href := ""
			if len(linkHrefStack) > 0 {
				href = linkHrefStack[len(linkHrefStack)-1]
				linkHrefStack = linkHrefStack[:len(linkHrefStack)-1]
			}
			out = append(out, "]("+href+")")
		} else if (tag == "strong" || tag == "b") && !closing {
			out = append(out, "**")
		} else if (tag == "strong" || tag == "b") && closing {
			out = append(out, "**")
		} else if (tag == "em" || tag == "i") && !closing {
			out = append(out, "*")
		} else if (tag == "em" || tag == "i") && closing {
			out = append(out, "*")
		} else if (tag == "code" || tag == "kbd" || tag == "samp") && !closing {
			if preDepth == 0 {
				out = append(out, "`")
			}
		} else if (tag == "code" || tag == "kbd" || tag == "samp") && closing {
			if preDepth == 0 {
				out = append(out, "`")
			}
		} else if tag == "pre" && !closing {
			preDepth++
			out = append(out, "\n```\n")
		} else if tag == "pre" && closing {
			if preDepth > 0 {
				preDepth--
			}
			out = append(out, "\n```\n")
		} else if tag == "blockquote" && !closing {
			out = append(out, "\n> ")
		} else if tag == "hr" && !closing {
			out = append(out, "\n\n---\n\n")
		} else if tag == "ul" && !closing {
			listStack = append(listStack, listFrame{})
			out = append(out, "\n")
		} else if tag == "ul" && closing {
			if len(listStack) > 0 {
				listStack = listStack[:len(listStack)-1]
			}
			out = append(out, "\n")
		} else if tag == "ol" && !closing {
			listStack = append(listStack, listFrame{ordered: true})
			out = append(out, "\n")
		} else if tag == "ol" && closing {
			if len(listStack) > 0 {
				listStack = listStack[:len(listStack)-1]
			}
			out = append(out, "\n")
		} else if tag == "li" && !closing {
			if len(listStack) > 0 {
				frame := &listStack[len(listStack)-1]
				frame.index++
				marker := "- "
				if frame.ordered {
					marker = strconv.Itoa(frame.index) + ". "
				}
				out = append(out, "\n"+strings.Repeat("  ", len(listStack)-1)+marker)
			} else {
				out = append(out, "\n- ")
			}
		} else if hasTag(blockTags, tag) || tag == "body" || tag == "html" {
			if !closing && !self && len(out) > 0 && !lastIsNewline() {
				out = append(out, "\n")
			}
			if (closing || self) && len(out) > 0 && !lastIsNewline() {
				out = append(out, "\n")
			}
		}
	}

	text := strings.Join(out, "")
	text = trailingSpaceBeforeN.ReplaceAllString(text, "\n")
	text = threeNewlinesRE.ReplaceAllString(text, "\n\n")
	text = twoSpaceRunsRE.ReplaceAllString(text, " ")
	return jsre.Trim(text)
}

// handler is webfetchTool.handler.
// port of src/tools/webfetch.ts:webfetchTool.handler
func handler(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	input := parseInput(args)
	if input.url == "" {
		return errorResult("Error: url is required"), nil
	}
	if !schemeRE.MatchString(input.url) {
		return errorResult("URL must start with http:// or https://"), nil
	}
	url := input.url

	headers := map[string]string{
		"User-Agent":      opencodeUserAgent,
		"Accept":          acceptFor(input.format),
		"Accept-Language": "en-US,en;q=0.9",
	}

	result, err := fetchBounded(ctx, url, headers, input.timeout)
	if err != nil {
		var challenge *cloudflareChallengeError
		// Retry with honest UA only on Cloudflare bot challenge.
		if errors.As(err, &challenge) {
			retryHeaders := make(map[string]string, len(headers)+1)
			for key, value := range headers {
				retryHeaders[key] = value
			}
			retryHeaders["User-Agent"] = "opencode"
			result, err = fetchBounded(ctx, url, retryHeaders, input.timeout)
		}
	}
	if err != nil {
		return failure(url, err), nil
	}

	mime := strings.ToLower(jsre.Trim(strings.Split(result.contentType, ";")[0]))

	// Upstream opencode 2.0 rejects images instead of attaching them.
	if strings.HasPrefix(mime, "image/") {
		return failure(url, fmt.Errorf("Unsupported fetched image content type: %s", mime)), nil
	}
	if !isTextualMime(mime) {
		return failure(url, fmt.Errorf("Unsupported fetched file content type: %s", mime)), nil
	}

	content := decodeTextDecoder(result.buffer)
	var output string
	switch {
	case input.format == "html":
		output = content
	case strings.Contains(result.contentType, "text/html"):
		if input.format == "text" {
			output = extractTextFromHtml(content)
		} else {
			output = convertHtmlToMarkdown(content)
		}
	default:
		output = content
	}

	if input.redactSecrets {
		output = compression.RedactSecrets(output)
	}
	full := output
	output = compression.ApplyBudget(ctx, output, "web")
	handle := compression.ArchiveHandle(full, output, "webfetch:"+url)
	return types.ToolResult{Content: []types.Content{types.Text(output + handle)}}, nil
}

func failure(url string, err error) types.ToolResult {
	return errorResult("Unable to fetch " + url + ": " + err.Error())
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

// decodeTextDecoder decodes UTF-8 like Node's `new TextDecoder()`: a leading
// BOM is dropped and every ill-formed maximal subpart becomes U+FFFD.
func decodeTextDecoder(data []byte) string {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
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
// A nil value is JSON null (Number(null) === 0); callers that need
// Number(undefined) check map presence before calling.
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

// parseDecimalPrefix is Number.parseInt(value, 10): leading whitespace and
// sign are accepted, parsing stops at the first non-digit.
func parseDecimalPrefix(value string) (float64, bool) {
	i := 0
	for i < len(value) {
		r, size := utf8.DecodeRuneInString(value[i:])
		if !jsre.IsSpace(r) {
			break
		}
		i += size
	}
	start := i
	if i < len(value) && (value[i] == '+' || value[i] == '-') {
		i++
	}
	digits := i
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == digits {
		return 0, false
	}
	number, err := strconv.ParseFloat(value[start:i], 64)
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
