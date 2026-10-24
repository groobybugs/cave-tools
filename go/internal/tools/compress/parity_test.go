//go:build parity

package compress

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
)

// Differential harness: runs the TypeScript handler from dist/ and the Go
// handler on the same arguments, then compares the full ToolResult JSON.
//
//	pnpm build && cd go && go test -tags parity -run TestParity ./internal/tools/compress/

const tsRunner = `import { pathToFileURL } from "node:url";

const toolPath = process.argv[2];
const exportName = process.argv[3];
let data = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { data += chunk; });
process.stdin.on("end", async () => {
  try {
    const mod = await import(pathToFileURL(toolPath).href);
    const result = await mod[exportName].handler(JSON.parse(data));
    process.stdout.write(JSON.stringify(result));
  } catch (error) {
    process.stderr.write(String((error && error.stack) || error));
    process.exit(1);
  }
});
`

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func runTS(t *testing.T, root string, args map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	runner := filepath.Join(t.TempDir(), "run-tool.mjs")
	if err := os.WriteFile(runner, []byte(tsRunner), 0o600); err != nil {
		t.Fatalf("runner: %v", err)
	}
	command := exec.Command("node", runner, filepath.Join(root, "dist/tools/compress.js"), "compressTool")
	command.Stdin = bytes.NewReader(encoded)
	command.Env = append(os.Environ(), "CAVE_TOOLS_DATA_DIR="+os.Getenv("CAVE_TOOLS_DATA_DIR"))
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	return stdout.String()
}

// TestParityHandler compares TS and Go ToolResult JSON for every case.
func TestParityHandler(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "compress.js")); err != nil {
		t.Skip("dist/tools/compress.js missing; run pnpm build")
	}

	archived := "archived line one\narchived line two\n"
	archiveID, _, err := compression.StoreArchive(archived, "parity")
	if err != nil {
		t.Fatalf("store archive: %v", err)
	}
	longLines := ""
	for i := 0; i < 300; i++ {
		longLines += "line " + strconv.Itoa(i) + "\n"
	}

	cases := []struct {
		name string
		args map[string]any
	}{
		{"blank lines", map[string]any{"text": "Line 1\n\n\n\nLine 2\n\n\nLine 3", "tool_name": "bash"}},
		{"json compact", map[string]any{"text": "{\n  \"a\": 1,\n  \"b\": [2, 3]\n}"}},
		{"jsonl compact", map[string]any{"text": "{ \"a\": 1 }\n{ \"b\": 2 }\n\n{ \"c\": 3 }"}},
		{"xml compact", map[string]any{"text": "<root>\n  <a>1</a>\n  <b>2</b>\n</root>"}},
		{"plain text", map[string]any{"text": "not json"}},
		{"ansi", map[string]any{"text": "\x1b[31mred\x1b[0m plain", "tool_name": "bash"}},
		{"structured off", map[string]any{"text": "{\n  \"a\": 1\n}", "structured": false}},
		{"read budget", map[string]any{"text": longLines, "tool_name": "read"}},
		{"grep budget", map[string]any{"text": longLines, "tool_name": "grep"}},
		{"bash budget", map[string]any{"text": longLines, "tool_name": "bash"}},
		{"command hint", map[string]any{"text": "a b\nc d", "command_hint": "ls"}},
		{"empty text", map[string]any{"text": ""}},
		{"missing text", map[string]any{}},
		{"null text", map[string]any{"text": nil}},
		{"null structured", map[string]any{"text": "{\n  \"a\": 1\n}", "structured": nil}},
		{"null tool name", map[string]any{"text": "hello", "tool_name": nil}},
		{"null action", map[string]any{"action": nil, "text": "hello"}},
		{"null archive id", map[string]any{"action": "expand", "archive_id": nil}},
		{"null command hint", map[string]any{"text": "hello", "command_hint": nil}},
		{"numeric text", map[string]any{"text": 123}},
		{"zero text", map[string]any{"text": 0}},
		{"false text", map[string]any{"text": false}},
		{"expand missing id", map[string]any{"action": "expand"}},
		{"expand unknown", map[string]any{"action": "expand", "archive_id": "nope"}},
		{"expand round trip", map[string]any{"action": "expand", "archive_id": archiveID}},
		{"empty action", map[string]any{"action": "", "text": "hello"}},
		{"array text", map[string]any{"text": []any{float64(1), nil, "x"}}},
		{"empty array text", map[string]any{"text": []any{}}},
		{"singleton null text", map[string]any{"text": []any{nil}}},
		{"nested array text", map[string]any{"text": []any{[]any{"a", nil, "b"}}}},
		{"object text", map[string]any{"text": map[string]any{"a": 1}}},
		{"array object text", map[string]any{"text": []any{map[string]any{}}}},
		{"boolean text", map[string]any{"text": true}},
		{"array tool name", map[string]any{"text": longLines, "tool_name": []any{"grep"}}},
		{"array command hint", map[string]any{"text": "a b\nc d", "command_hint": []any{"ls"}}},
		{"array action expand", map[string]any{"action": []any{"expand"}, "archive_id": []any{archiveID}}},
		{"array action compress", map[string]any{"action": []any{"compress"}, "text": "hello"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tsJSON := runTS(t, root, testCase.args)

			encoded, err := json.Marshal(testCase.args)
			if err != nil {
				t.Fatalf("args: %v", err)
			}
			var args map[string]any
			if err := json.Unmarshal(encoded, &args); err != nil {
				t.Fatalf("args: %v", err)
			}
			goResult, err := Tool.Handler(testCtx(), args)
			if err != nil {
				t.Fatalf("go handler: %v", err)
			}
			goJSON, err := json.Marshal(goResult)
			if err != nil {
				t.Fatalf("go marshal: %v", err)
			}

			var tsValue, goValue any
			if err := json.Unmarshal([]byte(tsJSON), &tsValue); err != nil {
				t.Fatalf("ts result is not JSON: %v\n%s", err, tsJSON)
			}
			if err := json.Unmarshal(goJSON, &goValue); err != nil {
				t.Fatalf("go result is not JSON: %v", err)
			}
			if !reflect.DeepEqual(tsValue, goValue) {
				t.Fatalf("TS and Go disagree\n TS: %s\n Go: %s", tsJSON, goJSON)
			}
		})
	}
}

// toolDefinitionJSON asks the TS module for the listing metadata.
func toolDefinitionJSON(t *testing.T, root, toolPath, exportName string) string {
	t.Helper()
	script := `const path = process.argv[2];
const name = process.argv[3];
import(path).then((mod) => {
  const tool = mod[name];
  process.stdout.write(JSON.stringify({ name: tool.name, annotations: tool.annotations, description: tool.description, inputSchema: tool.inputSchema }));
}, (error) => { console.error(error); process.exit(1); });
`
	runner := filepath.Join(t.TempDir(), "definition.mjs")
	if err := os.WriteFile(runner, []byte(script), 0o600); err != nil {
		t.Fatalf("runner: %v", err)
	}
	command := exec.Command("node", runner, filepath.Join(root, toolPath), exportName)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	return stdout.String()
}

// TestParityDefinition compares the tool's listing JSON byte for byte.
func TestParityDefinition(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "dist", "tools", "compress.js")); err != nil {
		t.Skip("dist/tools/compress.js missing; run pnpm build")
	}
	want := toolDefinitionJSON(t, root, "dist/tools/compress.js", "compressTool")
	got, err := json.Marshal(Tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != want {
		t.Fatalf("tool definition differs\n TS: %s\n Go: %s", want, got)
	}
}
