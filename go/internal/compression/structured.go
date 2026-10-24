package compression

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
)

// This file ports src/compression/structured.ts: semantic compression of bash
// output that is JSON or XML, gated behind CAVE_TOOLS_STRUCTURED_COMPRESSION.

// outputFormat is the detected payload shape.
// port of src/compression/structured.ts:OutputFormat
type outputFormat string

const (
	outputJSON outputFormat = "json"
	outputXML  outputFormat = "xml"
	outputText outputFormat = "text"
)

// commandKeyHint maps one command substring to the JSON keys it prioritizes.
// port of src/compression/structured.ts:COMMAND_KEY_HINTS
type commandKeyHint struct {
	pattern string
	keys    []string
}

var commandKeyHints = []commandKeyHint{
	{"docker inspect", []string{"State", "Config", "NetworkSettings", "Mounts", "HostConfig"}},
	{"docker ps", []string{"Names", "Status", "Ports", "Image"}},
	{"npm ls", []string{"name", "version", "dependencies"}},
	{"package.json", []string{"name", "version", "scripts", "dependencies", "devDependencies"}},
	{"tsconfig", []string{"compilerOptions", "include", "exclude"}},
	{"kubectl", []string{"metadata", "spec", "status"}},
	{"aws ", []string{"Arn", "Name", "Status", "State", "Id"}},
}

const (
	// port of src/compression/structured.ts:MAX_DEPTH
	maxDepth = 4
	// port of src/compression/structured.ts:MAX_ARRAY_ELEMENTS
	maxArrayElements = 3
	// port of src/compression/structured.ts:MAX_STRING_LENGTH
	maxStringLength = 200
	// port of src/compression/structured.ts:DEFAULT_MAX_KEYS
	defaultMaxKeys = 8
	// port of src/compression/structured.ts:HINT_FALLBACK_KEYS
	hintFallbackKeys = 5
	// port of src/compression/structured.ts:ERROR_KEEP_CAP
	errorKeepCap = 5

	// port of src/compression/structured.ts:QUALITY_RATIO
	qualityRatio = 0.6

	// port of src/compression/structured.ts:XML_RUN_MIN
	xmlRunMin = 4
	// port of src/compression/structured.ts:XML_KEEP_HEAD
	xmlKeepHead = 2
	// port of src/compression/structured.ts:XML_KEEP_TAIL
	xmlKeepTail = 1
	// port of src/compression/structured.ts:XML_MAX_DEPTH
	xmlMaxDepth = 6
)

// structuredCompressionOverride is the runtime toggle; nil follows the env.
// port of src/compression/structured.ts:structuredCompressionOverride
var structuredCompressionOverride *bool

var structuredMu sync.Mutex

// Regexes compiled from their TS sources through jsre.
var (
	// detectOutputFormat's /^\s*[[{]/ and /[}\]]\s*$/.
	jsonLikeStartRE = jsre.MustCompile(`^\s*[[{]`, "")
	jsonLikeEndRE   = jsre.MustCompile(`[}\]]\s*$`, "")

	// port of src/compression/structured.ts:ERROR_VALUE_RE
	errorValueRE = jsre.MustCompile(`\b(error|errors|exception|failed|failure|critical|fatal|crash|panic|abort|timeout|denied|rejected)\b`, "i")
	// port of src/compression/structured.ts:ERROR_KEY_RE
	errorKeyRE = jsre.MustCompile(`^(error|errors|message|msg|stack|stacktrace|stack_trace|trace|traceback|exception|reason|detail|details|warning|warnings)$`, "i")

	// port of src/compression/structured.ts:XML_ATTR_RE
	xmlAttrRE = jsre.MustCompile(`\s([A-Za-z_][\w.:-]*)="([^"]*)"`, "g")
	// port of src/compression/structured.ts:XML_MARKER_RE
	xmlMarkerRE = jsre.MustCompile(`^\s*<!-- \d+ <[^>]+> elements elided`, "")
	// port of src/compression/structured.ts:XML_XMLNS_RE
	xmlNSRE = jsre.MustCompile(`\s+xmlns(?::\w+)?="[^"]*"`, "g")

	// scanElem's /^<([A-Za-z_][\w.:-]*)/.
	xmlElemNameRE = jsre.MustCompile(`^<([A-Za-z_][\w.:-]*)`, "")
)

// IsStructuredCompressionEnabled reports whether semantic compression is on.
// port of src/compression/structured.ts:isStructuredCompressionEnabled
func IsStructuredCompressionEnabled(ctx context.Context) bool {
	structuredMu.Lock()
	override := structuredCompressionOverride
	structuredMu.Unlock()
	if override != nil {
		return *override
	}
	return rt.GetEnv(ctx)["CAVE_TOOLS_STRUCTURED_COMPRESSION"] != "off"
}

// SetStructuredCompression overrides the env toggle at runtime.
// port of src/compression/structured.ts:setStructuredCompression
func SetStructuredCompression(enabled bool) {
	structuredMu.Lock()
	value := enabled
	structuredCompressionOverride = &value
	structuredMu.Unlock()
}

// detectOutputFormat classifies the payload; only >50-line inputs qualify.
// port of src/compression/structured.ts:detectOutputFormat
func detectOutputFormat(text string) outputFormat {
	lines := strings.Split(text, "\n")
	if len(lines) <= 50 {
		return outputText
	}

	trimmed := jsre.TrimStart(text)

	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if isValidJSON(trimmed) {
			return outputJSON
		}
		// Could be truncated JSON — check structural shape before giving up.
		if jsonLikeStartRE.MatchString(trimmed) && jsonLikeEndRE.MatchString(jsre.TrimEnd(text)) {
			return outputJSON
		}
	}

	if strings.HasPrefix(trimmed, "<?xml") ||
		(strings.HasPrefix(trimmed, "<") && !strings.HasPrefix(trimmed, "<!DOCTYPE html")) {
		if strings.Contains(trimmed, "</") || strings.Contains(trimmed, "/>") {
			return outputXML
		}
	}

	return outputText
}

// keyHints is an insertion-ordered set (JS Set) of retained JSON keys.
type keyHints struct {
	set   map[string]struct{}
	order []string
}

func newKeyHints() *keyHints {
	return &keyHints{set: map[string]struct{}{}}
}

func (k *keyHints) add(key string) {
	if _, ok := k.set[key]; ok {
		return
	}
	k.set[key] = struct{}{}
	k.order = append(k.order, key)
}

func (k *keyHints) has(key string) bool {
	_, ok := k.set[key]
	return ok
}

func (k *keyHints) size() int { return len(k.order) }

// extractKeyHints resolves likely-relevant keys from the bash command.
// port of src/compression/structured.ts:extractKeyHints
func extractKeyHints(commandHint string) *keyHints {
	hints := newKeyHints()
	if commandHint == "" {
		return hints
	}
	lower := strings.ToLower(commandHint)
	for _, hint := range commandKeyHints {
		if strings.Contains(lower, strings.ToLower(hint.pattern)) {
			for _, key := range hint.keys {
				hints.add(key)
			}
		}
	}
	return hints
}

// ----- JSON value model -----------------------------------------------------
//
// JSON.stringify preserves object insertion order and formats numbers exactly
// like ECMAScript, so the parsed tree is kept as ordered objects plus float64
// numbers and serialized by the writer below instead of encoding/json.

// jsonObject is a JSON object that preserves key insertion order.
type jsonObject struct {
	keys   []string
	values map[string]any
}

func newJSONObject() *jsonObject {
	return &jsonObject{values: map[string]any{}}
}

func (o *jsonObject) set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// decodeJSONValue decodes one JSON value, preserving object key order.
func decodeJSONValue(dec *json.Decoder) (any, bool) {
	token, err := dec.Token()
	if err != nil {
		return nil, false
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := newJSONObject()
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, false
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, false
				}
				member, ok := decodeJSONValue(dec)
				if !ok {
					return nil, false
				}
				object.set(key, member)
			}
			if _, err := dec.Token(); err != nil {
				return nil, false
			}
			return object, true
		case '[':
			array := []any{}
			for dec.More() {
				member, ok := decodeJSONValue(dec)
				if !ok {
					return nil, false
				}
				array = append(array, member)
			}
			if _, err := dec.Token(); err != nil {
				return nil, false
			}
			return array, true
		default:
			return nil, false
		}
	case string:
		return value, true
	case json.Number:
		number, err := strconv.ParseFloat(value.String(), 64)
		if err != nil {
			if numErr, ok := err.(*strconv.NumError); !ok || numErr.Err != strconv.ErrRange {
				return nil, false
			}
		}
		return number, true
	case bool:
		return value, true
	case nil:
		return nil, true
	}
	return nil, false
}

// parseJSONValue is JSON.parse over the ordered value model.
func parseJSONValue(text string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	value, ok := decodeJSONValue(decoder)
	if !ok {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

// isValidJSON is JSON.parse's accept/reject decision.
func isValidJSON(text string) bool {
	return json.Valid([]byte(text))
}

// jsNumberString formats a float64 like ECMAScript Number::toString(10),
// which is what JSON.stringify emits.
func jsNumberString(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "null"
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

// jsQuoteString quotes a string like JSON.stringify: only control characters,
// quotes and backslashes are escaped; non-ASCII stays raw.
func jsQuoteString(value string) string {
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
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// writeJSON serializes the ordered value model; pretty mirrors
// JSON.stringify(value, null, 2).
func writeJSON(out *strings.Builder, value any, depth int, pretty bool) {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case float64:
		out.WriteString(jsNumberString(typed))
	case string:
		out.WriteString(jsQuoteString(typed))
	case []any:
		if len(typed) == 0 {
			out.WriteString("[]")
			return
		}
		if pretty {
			out.WriteString("[\n")
		} else {
			out.WriteByte('[')
		}
		for i, item := range typed {
			if i > 0 {
				if pretty {
					out.WriteString(",\n")
				} else {
					out.WriteByte(',')
				}
			}
			if pretty {
				out.WriteString(strings.Repeat("  ", depth+1))
			}
			writeJSON(out, item, depth+1, pretty)
		}
		if pretty {
			out.WriteString("\n" + strings.Repeat("  ", depth) + "]")
		} else {
			out.WriteByte(']')
		}
	case *jsonObject:
		if len(typed.keys) == 0 {
			out.WriteString("{}")
			return
		}
		if pretty {
			out.WriteString("{\n")
		} else {
			out.WriteByte('{')
		}
		for i, key := range typed.keys {
			if i > 0 {
				if pretty {
					out.WriteString(",\n")
				} else {
					out.WriteByte(',')
				}
			}
			if pretty {
				out.WriteString(strings.Repeat("  ", depth+1))
			}
			out.WriteString(jsQuoteString(key))
			if pretty {
				out.WriteString(": ")
			} else {
				out.WriteByte(':')
			}
			writeJSON(out, typed.values[key], depth+1, pretty)
		}
		if pretty {
			out.WriteString("\n" + strings.Repeat("  ", depth) + "}")
		} else {
			out.WriteByte('}')
		}
	default:
		out.WriteString("null")
	}
}

// marshalJSONIndent mirrors JSON.stringify(value, null, 2).
func marshalJSONIndent(value any) string {
	var out strings.Builder
	writeJSON(&out, value, 0, true)
	return out.String()
}

// marshalJSONCompact mirrors JSON.stringify(value).
func marshalJSONCompact(value any) string {
	var out strings.Builder
	writeJSON(&out, value, 0, false)
	return out.String()
}

// compressValue recursively caps arrays, keys, depth and string length.
// port of src/compression/structured.ts:compressValue
func compressValue(value any, relevantKeys *keyHints, depth int) any {
	if depth > maxDepth {
		switch typed := value.(type) {
		case []any:
			return fmt.Sprintf("[Array(%d)]", len(typed))
		case *jsonObject:
			return fmt.Sprintf("{Object(%d keys)}", len(typed.keys))
		}
		return value
	}

	if array, ok := value.([]any); ok {
		if len(array) <= maxArrayElements {
			kept := make([]any, 0, len(array))
			for _, item := range array {
				kept = append(kept, compressValue(item, relevantKeys, depth+1))
			}
			return kept
		}
		kept := make([]any, 0, maxArrayElements+errorKeepCap+1)
		for _, item := range array[:maxArrayElements] {
			kept = append(kept, compressValue(item, relevantKeys, depth+1))
		}
		// Error-bearing items beyond the cap carry the answer; never drop them
		// under a bare count stub. Bounded so pathological payloads stay capped.
		errorKeeps := 0
		for _, item := range array[maxArrayElements:] {
			if errorKeeps >= errorKeepCap {
				break
			}
			if !errorValueRE.MatchString(marshalJSONCompact(item)) {
				continue
			}
			kept = append(kept, compressValue(item, relevantKeys, depth+1))
			errorKeeps++
		}
		dropped := len(array) - maxArrayElements - errorKeeps
		suffix := ""
		if errorKeeps > 0 {
			suffix = fmt.Sprintf(", kept %d error-bearing", errorKeeps)
		}
		kept = append(kept, fmt.Sprintf("... %d more items (%d total)%s", dropped, len(array), suffix))
		return kept
	}

	if object, ok := value.(*jsonObject); ok {
		// Hint-driven retention: at depth 0/1 keep only the relevant keys the
		// caller is likely searching for; everything else is summarised.
		if relevantKeys.size() > 0 && depth <= 1 {
			result := newJSONObject()
			kept := 0
			omitted := make([]string, 0, len(object.keys))
			for _, key := range object.keys {
				if relevantKeys.has(key) {
					result.set(key, compressValue(object.values[key], relevantKeys, depth+1))
					kept++
				} else {
					omitted = append(omitted, key)
				}
			}

			if kept == 0 {
				// No hints matched — fall back to the first 5 keys so the call is
				// never empty.
				fallback := min(len(object.keys), hintFallbackKeys)
				for _, key := range object.keys[:fallback] {
					result.set(key, compressValue(object.values[key], relevantKeys, depth+1))
				}
				if len(object.keys) > hintFallbackKeys {
					result.set("...", fmt.Sprintf("%d more keys omitted", len(object.keys)-hintFallbackKeys))
				}
			} else if len(omitted) > 0 {
				message := fmt.Sprintf("%d keys omitted: %s", len(omitted), strings.Join(omitted[:min(5, len(omitted))], ", "))
				if len(omitted) > 5 {
					message += "..."
				}
				result.set("...", message)
			}
			return result
		}

		result := newJSONObject()
		if len(object.keys) <= defaultMaxKeys {
			for _, key := range object.keys {
				result.set(key, compressValue(object.values[key], relevantKeys, depth+1))
			}
			return result
		}
		for _, key := range object.keys[:defaultMaxKeys] {
			result.set(key, compressValue(object.values[key], relevantKeys, depth+1))
		}
		result.set("...", fmt.Sprintf("%d more keys omitted", len(object.keys)-defaultMaxKeys))
		return result
	}

	if text, ok := value.(string); ok && utf16Length(text) > maxStringLength {
		return utf16Prefix(text, maxStringLength) + fmt.Sprintf("... (%d chars)", utf16Length(text))
	}

	return value
}

// compressJSON semantically compresses a JSON payload, or returns nil when it
// does not shrink meaningfully.
// port of src/compression/structured.ts:compressJson
func compressJSON(text string, commandHint string) *string {
	trimmed := jsre.Trim(text)
	parsed, ok := parseJSONValue(trimmed)
	if !ok {
		return nil
	}

	relevantKeys := extractKeyHints(commandHint)
	compressed := compressValue(parsed, relevantKeys, 0)
	result := marshalJSONIndent(compressed)
	originalLines := len(strings.Split(text, "\n"))
	resultLines := len(strings.Split(result, "\n"))

	if float64(resultLines) >= float64(originalLines)*qualityRatio {
		return nil
	}

	retainedInfo := "Top-level keys retained"
	if relevantKeys.size() > 0 {
		retainedInfo = "Keys retained: " + strings.Join(relevantKeys.order, ", ")
	}

	output := fmt.Sprintf("%s\n\n[JSON compressed: %d of %d lines. %s]",
		result, resultLines, originalLines, retainedInfo)
	return &output
}

// ----- XML compression ------------------------------------------------------

// xmlElem is one element's line extent.
// port of src/compression/structured.ts:XmlElem
type xmlElem struct {
	name   string
	indent string
	lo     int
	hi     int
}

// xmlOutLine is one output slot: text plus whether it survived.
type xmlOutLine struct {
	text    string
	present bool
}

// errUnparseableXML is TS's UnparseableXml: the whole transform fails closed.
// port of src/compression/structured.ts:UnparseableXml
var errUnparseableXML = errors.New("unparseable xml")

// findTagEnd returns the index of the '>' closing the tag starting at s[0].
// port of src/compression/structured.ts:findTagEnd
func findTagEnd(s string) int {
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '>':
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// tagDepthDelta is the net opened-minus-closed tag count on one line; ok=false
// is TS's null (an unterminated construct).
// port of src/compression/structured.ts:tagDepthDelta
func tagDepthDelta(line string) (int, bool) {
	s := line
	delta := 0
	for {
		c := strings.Index(s, "<")
		if c < 0 {
			return delta, true
		}
		s = s[c:]
		if strings.HasPrefix(s, "<!--") {
			e := strings.Index(s, "-->")
			if e < 0 {
				return 0, false
			}
			s = s[e+3:]
			continue
		}
		if strings.HasPrefix(s, "<![CDATA[") {
			e := strings.Index(s, "]]>")
			if e < 0 {
				return 0, false
			}
			s = s[e+3:]
			continue
		}
		if strings.HasPrefix(s, "<?") {
			e := strings.Index(s, "?>")
			if e < 0 {
				return 0, false
			}
			s = s[e+2:]
			continue
		}
		if strings.HasPrefix(s, "<!") {
			e := strings.Index(s, ">")
			if e < 0 {
				return 0, false
			}
			s = s[e+1:]
			continue
		}
		tokenEnd := findTagEnd(s)
		if tokenEnd < 0 {
			return 0, false // tag split across lines: refuse to guess
		}
		token := s[:tokenEnd+1]
		if strings.HasPrefix(token, "</") {
			delta--
		} else if !strings.HasSuffix(token, "/>") {
			delta++
		}
		s = s[tokenEnd+1:]
	}
}

// scanElem classifies the line at i as an element and resolves its extent.
// port of src/compression/structured.ts:scanElem
func scanElem(lines []string, i int, hi int) (*xmlElem, error) {
	line := lines[i]
	indentEnd := 0
	for indentEnd < len(line) && (line[indentEnd] == ' ' || line[indentEnd] == '\t') {
		indentEnd++
	}
	indent := line[:indentEnd]
	rest := line[indentEnd:]
	if rest == "" ||
		rest[0] != '<' ||
		strings.HasPrefix(rest, "</") ||
		strings.HasPrefix(rest, "<!--") ||
		strings.HasPrefix(rest, "<?") ||
		strings.HasPrefix(rest, "<!") ||
		xmlMarkerRE.MatchString(line) {
		return nil, nil
	}
	nameMatch := xmlElemNameRE.FindStringSubmatch(rest)
	if nameMatch == nil {
		return nil, nil // `&lt;`, `<5`, other prose: never touched
	}

	openerEnd := findTagEnd(rest)
	if openerEnd < 0 {
		return nil, errUnparseableXML // tag split across lines
	}
	selfClosing := rest[openerEnd-1] == '/'
	name := nameMatch[1]
	depth := 0
	if !selfClosing {
		depth = 1
		delta, ok := tagDepthDelta(rest[openerEnd+1:])
		if !ok {
			return nil, errUnparseableXML
		}
		depth += delta
	}
	if depth <= 0 {
		return &xmlElem{name: name, indent: indent, lo: i, hi: i}, nil // self-closing or inline-complete
	}
	for j := i + 1; j <= hi; j++ {
		delta, ok := tagDepthDelta(lines[j])
		if !ok {
			return nil, errUnparseableXML
		}
		depth += delta
		if depth == 0 {
			return &xmlElem{name: name, indent: indent, lo: i, hi: j}, nil
		}
	}
	return nil, errUnparseableXML // unclosed element → fail closed
}

// openingTagToken returns the opening-tag token of the element at line.
// port of src/compression/structured.ts:openingTagToken
func openingTagToken(line string) *string {
	indentEnd := 0
	for indentEnd < len(line) && (line[indentEnd] == ' ' || line[indentEnd] == '\t') {
		indentEnd++
	}
	rest := line[indentEnd:]
	if !strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, "</") {
		return nil
	}
	end := findTagEnd(rest)
	if end < 0 {
		return nil
	}
	token := rest[:end+1]
	return &token
}

// summarizeXmlAttrs summarizes attributes over dropped elements' openers.
// port of src/compression/structured.ts:summarizeXmlAttrs
func summarizeXmlAttrs(elems []xmlElem, lines []string) string {
	attrNames := make([]string, 0, 2)
	valuesByName := map[string]*orderedCounts{}
	for _, elem := range elems {
		token := openingTagToken(lines[elem.lo])
		if token == nil {
			continue
		}
		for _, match := range xmlAttrRE.FindAllStringSubmatch(*token, -1) {
			name := match[1]
			value := match[2]
			values, ok := valuesByName[name]
			if !ok {
				values = newOrderedCounts()
				valuesByName[name] = values
				attrNames = append(attrNames, name)
			}
			previous, _ := values.get(value)
			values.set(value, previous+1)
		}
	}

	parts := make([]string, 0, 2)
	for _, name := range attrNames {
		if len(parts) >= 2 {
			break
		}
		values := valuesByName[name]
		distinct := append([]string{}, values.order...)
		if len(distinct) == 1 {
			parts = append(parts, fmt.Sprintf("all %s=%s", name, distinct[0]))
		} else {
			sort.Strings(distinct)
			parts = append(parts, fmt.Sprintf("%s: %d distinct, %s..%s",
				name, len(distinct), distinct[0], distinct[len(distinct)-1]))
		}
	}
	return utf16Prefix(strings.Join(parts, "; "), 140)
}

// xmlCompressRange collapses sibling runs in lines[lo..hi] into marker
// comments. Returns how many bytes were elided.
// port of src/compression/structured.ts:xmlCompressRange
func xmlCompressRange(lines []string, out []xmlOutLine, lo int, hi int, depth int) (int, error) {
	if lo > hi || depth > xmlMaxDepth {
		return 0, nil
	}

	elems := make([]xmlElem, 0, 8)
	i := lo
	for i <= hi {
		elem, err := scanElem(lines, i, hi)
		if err != nil {
			return 0, err
		}
		if elem == nil {
			i++
			continue
		}
		elems = append(elems, *elem)
		i = elem.hi + 1
	}

	elidedBytes := 0
	g := 0
	for g < len(elems) {
		h := g
		for g+1 < len(elems) && isXmlSibling(lines, elems[g], elems[g+1]) {
			g++
		}
		run := elems[h : g+1]
		g++

		if len(run) < xmlRunMin {
			for _, elem := range run {
				if elem.hi > elem.lo {
					elided, err := xmlCompressRange(lines, out, elem.lo+1, elem.hi-1, depth+1)
					if err != nil {
						return 0, err
					}
					elidedBytes += elided
				}
			}
			continue
		}

		n := len(run)
		keep := make([]bool, n)
		for k := 0; k < n; k++ {
			keep[k] = k < xmlKeepHead || k >= n-xmlKeepTail
		}
		for k := 0; k < n; k++ {
			if keep[k] {
				continue
			}
			span := strings.Join(lines[run[k].lo:run[k].hi+1], "\n")
			if errorValueRE.MatchString(span) || xmlHasErrorAttr(lines, run[k].lo) {
				keep[k] = true
			}
		}

		for k := 0; k < n; k++ {
			if !keep[k] {
				continue
			}
			if run[k].hi > run[k].lo {
				elided, err := xmlCompressRange(lines, out, run[k].lo+1, run[k].hi-1, depth+1)
				if err != nil {
					return 0, err
				}
				elidedBytes += elided
			}
		}
		for k := 0; k < n; {
			if keep[k] {
				k++
				continue
			}
			j := k
			for j < n && !keep[j] {
				j++
			}
			dropped := run[k:j]
			first := dropped[0]
			last := dropped[len(dropped)-1]
			elidedSpan := 0
			for p := first.lo; p <= last.hi; p++ {
				elidedSpan += utf16Length(lines[p]) + 1
			}
			summary := summarizeXmlAttrs(dropped, lines)
			marker := fmt.Sprintf("%s<!-- %d <%s> elements elided", first.indent, len(dropped), first.name)
			if summary != "" {
				marker += ": " + summary
			}
			marker += " -->"
			if utf16Length(marker) < elidedSpan {
				for p := first.lo; p <= last.hi; p++ {
					out[p].present = false
				}
				out[first.lo] = xmlOutLine{text: marker, present: true}
				elidedBytes += elidedSpan
			}
			k = j
		}
	}
	return elidedBytes, nil
}

// isXmlSibling reports whether a and b are same-name same-indent siblings with
// only blank lines between them.
// port of src/compression/structured.ts:isXmlSibling
func isXmlSibling(lines []string, a xmlElem, b xmlElem) bool {
	if a.name != b.name || a.indent != b.indent {
		return false
	}
	for k := a.hi + 1; k < b.lo; k++ {
		if jsre.Trim(lines[k]) != "" {
			return false
		}
	}
	return true
}

// xmlHasErrorAttr reports whether the element opener carries an error key attr.
// port of src/compression/structured.ts:xmlHasErrorAttr
func xmlHasErrorAttr(lines []string, start int) bool {
	token := openingTagToken(lines[start])
	if token == nil {
		return false
	}
	for _, match := range xmlAttrRE.FindAllStringSubmatch(*token, -1) {
		if errorKeyRE.MatchString(match[1]) {
			return true
		}
	}
	return false
}

// compressXML strips namespaces and collapses repeated sibling elements, or
// returns nil when structure cannot be fully accounted for.
// port of src/compression/structured.ts:compressXml
func compressXML(text string) *string {
	rawLines := strings.Split(text, "\n")
	originalCount := len(rawLines)
	lines := make([]string, len(rawLines))
	for i, line := range rawLines {
		lines[i] = xmlNSRE.ReplaceAllString(line, "")
	}

	out := make([]xmlOutLine, len(lines))
	for i, line := range lines {
		out[i] = xmlOutLine{text: line, present: true}
	}
	if _, err := xmlCompressRange(lines, out, 0, len(lines)-1, 0); err != nil {
		if errors.Is(err, errUnparseableXML) {
			return nil
		}
		return nil
	}

	kept := make([]string, 0, len(out))
	for _, line := range out {
		if line.present {
			kept = append(kept, line.text)
		}
	}
	resultCount := len(kept)
	if float64(resultCount) >= float64(originalCount)*qualityRatio {
		return nil
	}

	result := fmt.Sprintf("%s\n\n[XML compressed: %d of %d lines]",
		strings.Join(kept, "\n"), resultCount, originalCount)
	return &result
}

// CompressStructuredSemantic attempts semantic compression of a tool output.
// The second result is false when TS returns null.
// port of src/compression/structured.ts:compressStructuredSemantic
func CompressStructuredSemantic(ctx context.Context, text string, commandHint string) (string, bool) {
	if !IsStructuredCompressionEnabled(ctx) {
		return "", false
	}

	switch detectOutputFormat(text) {
	case outputJSON:
		if compressed := compressJSON(text, commandHint); compressed != nil {
			return *compressed, true
		}
		return "", false
	case outputXML:
		if compressed := compressXML(text); compressed != nil {
			return *compressed, true
		}
		return "", false
	default:
		return "", false
	}
}
