package compression

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func ensureStructuredOn(t *testing.T) {
	t.Helper()
	t.Setenv("CAVE_TOOLS_STRUCTURED_COMPRESSION", "on")
}

func mustCompress(t *testing.T, text string, commandHint string) string {
	t.Helper()
	compressed, ok := CompressStructuredSemantic(context.Background(), text, commandHint)
	if !ok {
		t.Fatalf("expected compression for %d-byte input", len(text))
	}
	return compressed
}

// TestJSONSemanticCompression ports the semantic JSON assertions in test.mjs
// ("2b2. Testing semantic structured compression").
func TestJSONSemanticCompression(t *testing.T) {
	ensureStructuredOn(t)
	bigValue := strings.Repeat("x", 250)
	manyItems := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		manyItems = append(manyItems, map[string]any{"idx": i, "value": bigValue})
	}
	type nested struct {
		Metadata map[string]any `json:"metadata"`
		Spec     map[string]any `json:"spec"`
		Status   map[string]any `json:"status"`
		Items    []any          `json:"items"`
		Extra1   string         `json:"extra1"`
		Extra2   string         `json:"extra2"`
		Extra3   string         `json:"extra3"`
		Extra4   string         `json:"extra4"`
		Extra5   string         `json:"extra5"`
		Idx      int            `json:"idx"`
	}
	rows := make([]nested, 0, 60)
	for i := 0; i < 60; i++ {
		rows = append(rows, nested{
			Metadata: map[string]any{"name": "demo", "version": 1},
			Spec:     map[string]any{"replicas": 3, "image": "demo:1.0"},
			Status:   map[string]any{"ready": true, "conditions": []any{"ok", "ok", "ok"}},
			Items:    manyItems,
			Extra1:   "filler1",
			Extra2:   "filler2",
			Extra3:   "filler3",
			Extra4:   "filler4",
			Extra5:   "filler5",
			Idx:      i,
		})
	}
	wrapped, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wrappedJSON := string(wrapped)
	if len(strings.Split(wrappedJSON, "\n")) <= 50 {
		t.Fatal("fixture must clear the 50-line gate")
	}

	compressed := mustCompress(t, wrappedJSON, "my-command")
	if !strings.Contains(compressed, "[JSON compressed:") {
		t.Fatal("JSON annotation footer missing")
	}
	if len(strings.Split(compressed, "\n")) >= len(strings.Split(wrappedJSON, "\n")) {
		t.Fatal("compressed JSON must be shorter")
	}
	if !strings.Contains(compressed, "...") {
		t.Fatal("long arrays must be elided into '...' summaries")
	}
	if !strings.Contains(compressed, "chars)") {
		t.Fatal("long strings must be truncated with a char count")
	}

	kubectlCompressed := mustCompress(t, wrappedJSON, "kubectl get pods")
	if !strings.Contains(kubectlCompressed, "Keys retained: metadata, spec, status") {
		t.Fatal("kubectl hint keys must be advertised in the footer")
	}

	nonStructured := strings.Repeat("not even close to JSON or XML\n", 60)
	if _, ok := CompressStructuredSemantic(context.Background(), nonStructured, "ls"); ok {
		t.Fatal("text input must fall through")
	}

	smallJSON, err := json.MarshalIndent(map[string]any{"a": 1, "b": 2, "c": []any{1, 2, 3}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := CompressStructuredSemantic(context.Background(), string(smallJSON), "echo"); ok {
		t.Fatal("small JSON must be skipped")
	}
}

// TestXMLSemanticCompression ports the namespace/repetition XML assertions.
func TestXMLSemanticCompression(t *testing.T) {
	ensureStructuredOn(t)
	xmlLines := []string{`<?xml version="1.0" encoding="UTF-8"?>`}
	xmlLines = append(xmlLines, `<root xmlns="http://example.com/ns" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	for i := 0; i < 60; i++ {
		xmlLines = append(xmlLines, "  <item>n"+itoa(i)+"</item>")
	}
	xmlLines = append(xmlLines, `</root>`)
	longXML := strings.Join(xmlLines, "\n")

	compressed := mustCompress(t, longXML, "")
	if strings.Contains(compressed, "xmlns") {
		t.Fatal("namespaces must be stripped")
	}
	if !strings.Contains(compressed, "[XML compressed:") {
		t.Fatal("XML annotation footer missing")
	}

	// Multi-line XML: sibling runs collapse as whole elements (children never
	// leak through as orphans), the marker carries an attribute summary, and an
	// error-bearing element mid-run is force-kept.
	rss := []string{`<?xml version="1.0"?>`, `<rss version="2.0">`, `  <channel>`, `    <title>Feed</title>`}
	for i := 1; i <= 30; i++ {
		rss = append(rss, `    <item id="a`+itoa(i)+`" status="ok">`)
		rss = append(rss, "      <title>Story "+itoa(i)+"</title>")
		if i == 15 {
			rss = append(rss, `      <desc>connection timeout while fetching upstream.</desc>`)
		} else {
			rss = append(rss, "      <desc>Routine summary text for item "+itoa(i)+".</desc>")
		}
		rss = append(rss, `    </item>`)
	}
	rss = append(rss, `  </channel>`, `</rss>`)
	rssText := strings.Join(rss, "\n")

	rssCompressed := mustCompress(t, rssText, "")
	if !strings.Contains(rssCompressed, "Story 1") {
		t.Fatal("head element must be kept")
	}
	if !strings.Contains(rssCompressed, "Story 30") {
		t.Fatal("tail element must be kept")
	}
	if !strings.Contains(rssCompressed, "connection timeout") {
		t.Fatal("error element must be force-kept")
	}
	if !strings.Contains(rssCompressed, "elements elided") {
		t.Fatal("elision marker missing")
	}
	if !strings.Contains(rssCompressed, "all status=ok") {
		t.Fatal("marker must carry the invariant attribute summary")
	}
	if strings.Contains(rssCompressed, "<title>Story 5</title>") {
		t.Fatal("elided children must not leak as orphans")
	}
	if len(rssCompressed) >= len(rssText) {
		t.Fatal("output must be smaller")
	}

	// XML idempotence: a second pass must not change the payload (or the
	// shrunken payload falls under the 50-line gate and stays as-is).
	secondPass, ok := CompressStructuredSemantic(context.Background(), rssCompressed, "")
	if ok && secondPass != rssCompressed {
		t.Fatal("second pass must be stable")
	}
}

// TestJSONErrorBearingArrayItems ports the error-keep assertions. The TS
// fixture appends a comment, which makes the payload invalid JSON and skips
// those assertions; this variant keeps the payload valid so the branch runs.
func TestJSONErrorBearingArrayItems(t *testing.T) {
	ensureStructuredOn(t)
	results := []any{
		map[string]any{"name": "t1", "status": "passed"},
		map[string]any{"name": "t2", "status": "passed"},
		map[string]any{"name": "t3", "status": "passed"},
		map[string]any{"name": "t4", "status": "failed", "message": "assertion error: timeout"},
		map[string]any{"name": "t5", "status": "passed"},
	}
	noise := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		noise = append(noise, "padding "+itoa(i))
	}
	payload, err := json.MarshalIndent(map[string]any{"results": results, "noise": noise}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	compressed := mustCompress(t, string(payload), "")
	if !strings.Contains(compressed, "t4") {
		t.Fatal("failed test item must survive the array cap")
	}
	if !strings.Contains(compressed, "error-bearing") {
		t.Fatal("stub must note error keeps")
	}
}

// TestStructuredCompressionToggle ports the toggle assertions from test.mjs.
func TestStructuredCompressionToggle(t *testing.T) {
	ensureStructuredOn(t)
	ctx := context.Background()
	if !IsStructuredCompressionEnabled(ctx) {
		t.Fatal("semantic compression must default to enabled")
	}

	rows := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		rows = append(rows, map[string]any{"idx": i, "value": "payload " + itoa(i)})
	}
	wrappedBytes, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wrappedJSON := string(wrappedBytes)
	SetStructuredCompression(false)
	if IsStructuredCompressionEnabled(ctx) {
		t.Fatal("toggle off not observed")
	}
	if _, ok := CompressStructuredSemantic(ctx, wrappedJSON, "kubectl get pods"); ok {
		t.Fatal("toggle off must bypass compression")
	}
	SetStructuredCompression(true)
	if !IsStructuredCompressionEnabled(ctx) {
		t.Fatal("toggle on not observed")
	}
}
