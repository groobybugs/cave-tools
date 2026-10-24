package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCPNumericOverflowCasesMatchNode replays tools/call frames whose
// arguments carry a JSON number outside float64 range against the real MCP
// pipe. Expectations come from testdata/gen_overflow_mcp_cases.mjs, which ran
// the same frames through the TypeScript server: JSON.parse yields Infinity,
// so each numeric schema field must behave exactly as TS does (read
// offset/limit, bash timeout, edit lines, ls offset, grep/find limits).
func TestMCPNumericOverflowCasesMatchNode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "overflow_mcp_cases.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		NodeVersion string `json:"nodeVersion"`
		Template    string `json:"template"`
		Cases       []struct {
			Name    string `json:"name"`
			Frame   string `json:"frame"`
			IsError bool   `json:"isError"`
			Text    string `json:"text"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	t.Logf("fixture generated with node %s (%d cases): %s", fixture.NodeVersion, len(fixture.Cases), fixture.Template)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "files", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", "note.txt"), []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", "sub", "hit.txt"), []byte("root\nother\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CAVE_TOOLS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("CAVE_TOOLS_DAEMON", "0")
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	pipe := startMCPPipe(t)
	pipe.initialize(t)

	for _, tc := range fixture.Cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			frame := strings.ReplaceAll(tc.Frame, "@@DIR@@", dir)
			if _, err := pipe.in.Write([]byte(frame + "\n")); err != nil {
				t.Fatalf("write frame: %v", err)
			}
			response := pipe.read(t)
			result, ok := response["result"].(map[string]any)
			if !ok {
				t.Fatalf("response = %#v", response)
			}
			if got := result["isError"] == true; got != tc.IsError {
				t.Fatalf("isError = %v, want %v (%#v)", got, tc.IsError, result)
			}
			if got := firstText(t, result); got != tc.Text {
				t.Fatalf("text = %q, want %q", got, tc.Text)
			}
		})
	}
}
