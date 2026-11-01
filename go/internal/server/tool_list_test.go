package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// TestToolListOrder pins the exact TS loader/allTools order.
func TestToolListOrder(t *testing.T) {
	want := []string{
		"cave__read", "cave__bash", "cave__bash_start", "cave__bash_status",
		"cave__bash_stop", "cave__grep", "cave__find", "cave__ls",
		"cave__write", "cave__invalidate", "cave__edit", "cave__apply_patch",
		"cave__websearch", "cave__webfetch", "cave__compress", "cave__status",
		"cave__configure",
	}
	if !reflect.DeepEqual(ToolNames, want) {
		t.Fatalf("ToolNames = %v, want %v", ToolNames, want)
	}
	if len(AllTools) != len(want) {
		t.Fatalf("len(AllTools) = %d, want %d", len(AllTools), len(want))
	}
	for i, name := range want {
		if AllTools[i].Name != name {
			t.Fatalf("AllTools[%d].Name = %q, want %q", i, AllTools[i].Name, name)
		}
		if !IsToolName(name) {
			t.Fatalf("IsToolName(%q) = false", name)
		}
	}
	if IsToolName("toString") || IsToolName("__proto__") || IsToolName("cave__nope") {
		t.Fatal("non-own names must not resolve")
	}
}

// TestListedToolsProfiles ports the TS profile assertions.
func TestListedToolsProfiles(t *testing.T) {
	hidden := []string{"cave__configure", "cave__compress", "cave__status"}
	if got := len(ListedTools("full")); got != 17 {
		t.Fatalf("full profile lists %d tools, want 17", got)
	}
	for _, profile := range []string{"", "standard", "bogus"} {
		listed := ListedTools(profile)
		if len(listed) != 14 {
			t.Fatalf("profile %q lists %d tools, want 14", profile, len(listed))
		}
		for _, name := range hidden {
			for _, tool := range listed {
				if tool.Name == name {
					t.Fatalf("profile %q lists hidden %s", profile, name)
				}
			}
		}
	}
	if got := len(ListedTools(" FULL ")); got != 17 {
		t.Fatalf("padded FULL lists %d tools, want 17", got)
	}
	t.Setenv("CAVE_TOOLS_PROFILE", "full")
	if got := len(ListedTools()); got != 17 {
		t.Fatalf("env profile full lists %d tools, want 17", got)
	}
}

// TestToolDefinitionDifferential compares every name, description, annotation
// and input schema with the built TS reference. It skips when dist/ is absent.
func TestToolDefinitionDifferential(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "tool-list.js")); err != nil {
		t.Skip("dist/tool-list.js missing; run pnpm build")
	}
	script := `import { allTools } from "./dist/tool-list.js"; process.stdout.write(JSON.stringify(allTools));`
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Dir = root
	cmd.Env = testenv.Environ()
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var wantJSON any
	if err := json.Unmarshal(output, &wantJSON); err != nil {
		t.Fatalf("decode TS tool list: %v", err)
	}
	gotBytes, err := json.Marshal(AllTools)
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON any
	if err := json.Unmarshal(gotBytes, &gotJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("Go tool list differs from TS\n got: %s\nwant: %s", gotBytes, output)
	}

	var tsNames []string
	for _, raw := range wantJSON.([]any) {
		tsNames = append(tsNames, raw.(map[string]any)["name"].(string))
	}
	if !reflect.DeepEqual(tsNames, ToolNames) {
		t.Fatalf("TS tool order %v != ToolNames %v", tsNames, ToolNames)
	}
}

// TestToolSchemasRootIsPlainObject keeps every advertised inputSchema root a
// plain object. Strict providers (xAI/grok) refuse to sample when the root is
// a union: "tool parameter root must be an object type (root schema is an
// anyOf/oneOf union with a non-object branch)". Unions may live in properties.
func TestToolSchemasRootIsPlainObject(t *testing.T) {
	for _, tool := range AllTools {
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s inputSchema is not a JSON object: %v", tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s inputSchema root type = %v, want object", tool.Name, schema["type"])
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf", "not", "enum", "if", "then", "else"} {
			if _, ok := schema[key]; ok {
				t.Errorf("%s inputSchema root has %q; strict providers reject root combinators", tool.Name, key)
			}
		}
	}
}
