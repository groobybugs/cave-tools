package types

import (
	"encoding/json"
	"testing"
)

func TestToolResultMarshalsTSShape(t *testing.T) {
	tests := []struct {
		name   string
		result ToolResult
		want   string
	}{
		{
			name:   "text block",
			result: ToolResult{Content: []Content{Text("hello")}},
			want:   `{"content":[{"type":"text","text":"hello"}]}`,
		},
		{
			name:   "empty text stays present",
			result: ToolResult{Content: []Content{Text("")}},
			want:   `{"content":[{"type":"text","text":""}]}`,
		},
		{
			name:   "error result",
			result: ToolResult{Content: []Content{Text("boom")}, IsError: true},
			want:   `{"content":[{"type":"text","text":"boom"}],"isError":true}`,
		},
		{
			name: "image block",
			result: ToolResult{Content: []Content{
				Image("aGk=", "image/png"),
			}},
			want: `{"content":[{"type":"image","data":"aGk=","mimeType":"image/png"}]}`,
		},
		{
			name: "resource block",
			result: ToolResult{Content: []Content{
				ResourceBlock("file:///x.pdf", "application/pdf", "aGk="),
			}},
			want: `{"content":[{"type":"resource","resource":{"uri":"file:///x.pdf","mimeType":"application/pdf","blob":"aGk="}}]}`,
		},
		{
			name: "meta",
			result: ToolResult{
				Content: []Content{Text("x")},
				Meta:    map[string]any{"cave": "tools"},
			},
			want: `{"content":[{"type":"text","text":"x"}],"_meta":{"cave":"tools"}}`,
		},
		{
			name:   "empty content list",
			result: ToolResult{Content: []Content{}},
			want:   `{"content":[]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("marshal mismatch\n got: %s\nwant: %s", got, test.want)
			}
		})
	}
}

func TestContentDiscriminators(t *testing.T) {
	if got := Text("x").contentType(); got != "text" {
		t.Fatalf("text contentType = %q", got)
	}
	if got := Image("", "").contentType(); got != "image" {
		t.Fatalf("image contentType = %q", got)
	}
	if got := ResourceBlock("", "", "").contentType(); got != "resource" {
		t.Fatalf("resource contentType = %q", got)
	}
}
