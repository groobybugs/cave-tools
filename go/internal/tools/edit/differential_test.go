package edit

// Differential harness: runs the TypeScript handlers from dist/ and the Go
// handlers on the same arguments and the same fixture layout, then compares the
// full ToolResult JSON plus the advertised inputSchema JSON.
//
// Skips with a clear message when dist/ or node is missing, like the storage
// interop test, so `go test ./...` stays hermetic on a fresh checkout.

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools"
	"github.com/groobybugs/cave-tools/go/internal/tools/applypatch"
	"github.com/groobybugs/cave-tools/go/internal/tools/write"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

type diffCase struct {
	Name    string            `json:"name"`
	Tool    string            `json:"tool"`
	Args    map[string]any    `json:"args"`
	Fixture []diffFixture     `json:"fixture,omitempty"`
	Env     map[string]string `json:"-"`
}

type diffFixture struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type tsSpec struct {
	Dist  string     `json:"dist"`
	Cases []diffCase `json:"cases"`
}

type tsOutput struct {
	Schemas map[string]string `json:"schemas"`
	Results []json.RawMessage `json:"results"`
}

const tsRunner = `
import { readFileSync } from "node:fs";
const spec = JSON.parse(readFileSync(process.argv[2], "utf8"));
const editTool = (await import(spec.dist + "/tools/edit.js")).editTool;
const writeTool = (await import(spec.dist + "/tools/write.js")).writeTool;
const applyPatchTool = (await import(spec.dist + "/tools/apply-patch.js")).applyPatchTool;
const tools = { edit: editTool, write: writeTool, apply_patch: applyPatchTool };
const results = [];
for (const item of spec.cases) {
  const result = await tools[item.tool].handler(item.args);
  results.push(result);
}
process.stdout.write(JSON.stringify({
  schemas: {
    edit: JSON.stringify(editTool.inputSchema),
    write: JSON.stringify(writeTool.inputSchema),
    apply_patch: JSON.stringify(applyPatchTool.inputSchema),
  },
  results,
}));
`

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

// differentialCases is the shared sequence. Fixtures are re-created per case so
// the TS and Go runs see identical starting states.
func differentialCases() []diffCase {
	return []diffCase{
		{Name: "write create", Tool: "write", Args: map[string]any{"file_path": "$DIR/w1.txt", "content": "hello"}},
		{Name: "write overwrite", Tool: "write", Args: map[string]any{"file_path": "$DIR/w1.txt", "content": "replaced"},
			Fixture: []diffFixture{{Path: "w1.txt", Content: "hello"}}},
		{Name: "write truncate", Tool: "write", Args: map[string]any{"file_path": "$DIR/w2.txt", "truncate": true},
			Fixture: []diffFixture{{Path: "w2.txt", Content: "will be emptied"}}},
		{Name: "write empty content", Tool: "write", Args: map[string]any{"file_path": "$DIR/w3.txt", "content": ""},
			Fixture: []diffFixture{{Path: "w3.txt", Content: "to be cleared"}}},
		{Name: "write missing content", Tool: "write", Args: map[string]any{"file_path": "$DIR/w4.txt"}},
		{Name: "write missing path", Tool: "write", Args: map[string]any{"content": "x"}},
		{Name: "write nested", Tool: "write", Args: map[string]any{"file_path": "$DIR/nested/deep.txt", "content": "nested"}},
		{Name: "write bom", Tool: "write", Args: map[string]any{"file_path": "$DIR/bom.txt", "content": "after"},
			Fixture: []diffFixture{{Path: "bom.txt", Content: "\uFEFFbefore"}}},

		{Name: "edit unique", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e1.txt", "old_string": "alpha", "new_string": "omega"},
			Fixture: []diffFixture{{Path: "e1.txt", Content: "alpha beta beta\n"}}},
		{Name: "edit non-unique", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e2.txt", "old_string": "beta", "new_string": "gamma"},
			Fixture: []diffFixture{{Path: "e2.txt", Content: "alpha beta beta\n"}}},
		{Name: "edit replace_all", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e3.txt", "old_string": "beta", "new_string": "gamma", "replace_all": true},
			Fixture: []diffFixture{{Path: "e3.txt", Content: "alpha beta beta\n"}}},
		{Name: "edit identical", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e4.txt", "old_string": "alpha", "new_string": "alpha"},
			Fixture: []diffFixture{{Path: "e4.txt", Content: "alpha\n"}}},
		{Name: "edit empty old_string", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e5.txt", "old_string": "", "new_string": "x"},
			Fixture: []diffFixture{{Path: "e5.txt", Content: "alpha\n"}}},
		{Name: "edit already applied", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e6.txt", "old_string": "old content", "new_string": "new content here"},
			Fixture: []diffFixture{{Path: "e6.txt", Content: "new content here\n"}}},
		{Name: "edit unicode punctuation", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e7.txt", "old_string": `it's a "test" - done`, "new_string": `it's a "test" - changed`},
			Fixture: []diffFixture{{Path: "e7.txt", Content: "it’s a “test” — done\n"}}},
		{Name: "edit trailing whitespace", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e8.txt", "old_string": "alpha\nbeta", "new_string": "alpha\nGAMMA"},
			Fixture: []diffFixture{{Path: "e8.txt", Content: "alpha   \nbeta\t\n"}}},
		{Name: "edit crlf", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e9.txt", "old_string": "line1\nline2", "new_string": "changed1\nchanged2"},
			Fixture: []diffFixture{{Path: "e9.txt", Content: "line1\r\nline2\r\nline3\r\n"}}},
		{Name: "edit hint", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e10.txt", "old_string": "function notHere() {", "new_string": "x"},
			Fixture: []diffFixture{{Path: "e10.txt", Content: "function myFunction() {\n  return 42;\n}\n"}}},
		{Name: "edit missing file", Tool: "edit", Args: map[string]any{"file_path": "$DIR/nope.txt", "old_string": "a", "new_string": "b"}},
		{Name: "edit missing file_path", Tool: "edit", Args: map[string]any{"old_string": "a", "new_string": "b"}},
		{Name: "edit bad type", Tool: "edit", Args: map[string]any{"file_path": "$DIR/e11.txt", "old_string": 5, "new_string": "x"},
			Fixture: []diffFixture{{Path: "e11.txt", Content: "alpha\n"}}},

		{Name: "edit range replace", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r1.txt", "start_line": 2, "end_line": 3, "content": "TWO\nTHREE"},
			Fixture: []diffFixture{{Path: "r1.txt", Content: "one\ntwo\nthree\nfour\n"}}},
		{Name: "edit range insert", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r2.txt", "start_line": 2, "end_line": 1, "content": "x"},
			Fixture: []diffFixture{{Path: "r2.txt", Content: "a\nb\n"}}},
		{Name: "edit range delete", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r3.txt", "start_line": 2, "end_line": 2, "delete": true},
			Fixture: []diffFixture{{Path: "r3.txt", Content: "1\n2\n3\n"}}},
		{Name: "edit range out of bounds", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r4.txt", "start_line": 99, "end_line": 99, "content": "x"},
			Fixture: []diffFixture{{Path: "r4.txt", Content: "1\n2\n"}}},
		{Name: "edit range bad checksum", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r5.txt", "start_line": 2, "end_line": 2, "content": "B", "expected_range_checksum": "ffffffff"},
			Fixture: []diffFixture{{Path: "r5.txt", Content: "a\nb\nc\n"}}},
		{Name: "edit hash mismatch", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r6.txt", "start_line": 1, "end_line": 1, "content": "bye", "expected_hash": "deadbeefdeadbeef"},
			Fixture: []diffFixture{{Path: "r6.txt", Content: "hello\n"}}},
		{Name: "edit move", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r7.txt", "start_line": 2, "end_line": 3, "insert_before": 5},
			Fixture: []diffFixture{{Path: "r7.txt", Content: "a\nb\nc\nd\n"}}},
		{Name: "edit bom range", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r8.txt", "start_line": 1, "end_line": 1, "content": "ALPHA"},
			Fixture: []diffFixture{{Path: "r8.txt", Content: "\uFEFFalpha\nbeta\n"}}},
		{Name: "edit mixed eol range", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r9.txt", "start_line": 2, "end_line": 2, "content": "TWO"},
			Fixture: []diffFixture{{Path: "r9.txt", Content: "1\r\n2\n3\r\n4\n"}}},
		{Name: "edit mixed modes", Tool: "edit", Args: map[string]any{"file_path": "$DIR/r10.txt", "old_string": "one", "new_string": "ONE", "start_line": 1, "end_line": 1, "content": "ONE"},
			Fixture: []diffFixture{{Path: "r10.txt", Content: "one\n"}}},

		{Name: "edit batch strings", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b1.txt", "edits": []any{
			map[string]any{"old_string": "alpha", "new_string": "one"},
			map[string]any{"old_string": "gamma", "new_string": "three"},
		}}, Fixture: []diffFixture{{Path: "b1.txt", Content: "alpha beta gamma delta\n"}}},
		{Name: "edit batch overlap", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b2.txt", "edits": []any{
			map[string]any{"old_string": "foo bar", "new_string": "x"},
			map[string]any{"old_string": "bar baz", "new_string": "y"},
		}}, Fixture: []diffFixture{{Path: "b2.txt", Content: "foo bar baz\n"}}},
		{Name: "edit batch multi-file", Tool: "edit", Args: map[string]any{"edits": []any{
			map[string]any{"file_path": "$DIR/b3a.txt", "start_line": 1, "end_line": 1, "content": "X"},
			map[string]any{"file_path": "$DIR/b3b.txt", "old_string": "y", "new_string": "Y"},
		}}, Fixture: []diffFixture{{Path: "b3a.txt", Content: "x\n"}, {Path: "b3b.txt", Content: "y\n"}}},
		{Name: "edit batch ranges", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b4.txt", "edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "content": "A"},
			map[string]any{"start_line": 4, "end_line": 5, "content": "D\nE"},
		}}, Fixture: []diffFixture{{Path: "b4.txt", Content: "1\n2\n3\n4\n5\n"}}},
		{Name: "edit batch move+range", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b5.txt", "edits": []any{
			map[string]any{"start_line": 1, "end_line": 2, "insert_before": 5},
			map[string]any{"start_line": 3, "end_line": 3, "content": "X"},
		}}, Fixture: []diffFixture{{Path: "b5.txt", Content: "1\n2\n3\n4\n"}}},
		{Name: "edit batch same-point inserts", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b6.txt", "edits": []any{
			map[string]any{"start_line": 2, "end_line": 1, "content": "A"},
			map[string]any{"start_line": 2, "end_line": 1, "content": "B"},
		}}, Fixture: []diffFixture{{Path: "b6.txt", Content: "1\n2\n3\n"}}},
		{Name: "edit batch empty", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b7.txt", "edits": []any{}},
			Fixture: []diffFixture{{Path: "b7.txt", Content: "x\n"}}},
		{Name: "edit batch alias clash", Tool: "edit", Args: map[string]any{"edits": []any{
			map[string]any{"file_path": "$DIR/sub/../b8.txt", "old_string": "one", "new_string": "ONE"},
			map[string]any{"file_path": "$DIR/b8.txt", "old_string": "two", "new_string": "TWO"},
		}}, Fixture: []diffFixture{{Path: "b8.txt", Content: "one\ntwo\n"}}},
		{Name: "edit batch missing new_string", Tool: "edit", Args: map[string]any{"file_path": "$DIR/b9.txt", "edits": []any{
			map[string]any{"old_string": "alpha"},
		}}, Fixture: []diffFixture{{Path: "b9.txt", Content: "alpha\n"}}},

		{Name: "edit batch default plus override", Tool: "edit", Args: map[string]any{"file_path": "$DIR/d1.txt", "edits": []any{
			map[string]any{"old_string": "alpha", "new_string": "ALPHA"},
			map[string]any{"file_path": "$DIR/d2.txt", "old_string": "beta", "new_string": "BETA"},
		}}, Fixture: []diffFixture{{Path: "d1.txt", Content: "alpha\n"}, {Path: "d2.txt", Content: "beta\n"}}},
		{Name: "edit batch delete with content", Tool: "edit", Args: map[string]any{"file_path": "$DIR/d3.txt", "edits": []any{
			map[string]any{"start_line": 1, "end_line": 1, "delete": true, "content": "ignored"},
		}}, Fixture: []diffFixture{{Path: "d3.txt", Content: "alpha\nbeta\n"}}},
		{Name: "edit range delete with content", Tool: "edit", Args: map[string]any{"file_path": "$DIR/d4.txt", "start_line": 1, "end_line": 1, "delete": true, "content": "ignored"},
			Fixture: []diffFixture{{Path: "d4.txt", Content: "alpha\nbeta\n"}}},
		{Name: "edit old_string null", Tool: "edit", Args: map[string]any{"file_path": "$DIR/d5.txt", "old_string": nil, "new_string": "x"},
			Fixture: []diffFixture{{Path: "d5.txt", Content: "alpha\n"}}},
		{Name: "edit range checksum ok", Tool: "edit", Args: map[string]any{"file_path": "$DIR/d6.txt", "start_line": 2, "end_line": 2, "content": "B", "expected_range_checksum": tools.RangeChecksum([]string{"a", "b", "c", ""}, 2, 2)},
			Fixture: []diffFixture{{Path: "d6.txt", Content: "a\nb\nc\n"}}},
		{Name: "write numeric path", Tool: "write", Args: map[string]any{"file_path": 5, "content": "x"}},

		{Name: "patch add", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Add File: $DIR/p1.txt\n+one\n+two\n*** End Patch"}},
		{Name: "patch update", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p2.txt\n@@\n-one\n+ONE\n two\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p2.txt", Content: "one\ntwo\n"}}},
		{Name: "patch delete", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Delete File: $DIR/p3.txt\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p3.txt", Content: "gone\n"}}},
		{Name: "patch move", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p4.txt\n*** Move to: $DIR/moved/p4.txt\n@@\n-old\n+new\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p4.txt", Content: "old\n"}}},
		{Name: "patch multi hunk", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p5.txt\n@@\n-line2\n+changed2\n@@\n-line4\n+changed4\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p5.txt", Content: "line1\nline2\nline3\nline4\n"}}},
		{Name: "patch context", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p6.txt\n@@ fn b\n-x=10\n+x=11\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p6.txt", Content: "fn a\nx=10\ny=2\nfn b\nx=10\ny=20\n"}}},
		{Name: "patch eof anchor", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p7.txt\n@@\n-last\n+end\n*** End of File\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p7.txt", Content: "alpha\nlast\n"}}},
		{Name: "patch heredoc", Tool: "apply_patch", Args: map[string]any{"patchText": "cat <<'EOF'\n*** Begin Patch\n*** Add File: $DIR/p8.txt\n+heredoc\n*** End Patch\nEOF"}},
		{Name: "patch unicode", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p9.txt\n@@\n-He said \"hello\"\n+He said \"hi\"\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p9.txt", Content: "He said \u201Chello\u201D\nsome\u2014dash\nend\n"}}},
		{Name: "patch bom", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p10.cs\n@@\n class Test {}\n+class Next {}\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p10.cs", Content: "\uFEFFusing System;\n\nclass Test {}\n"}}},
		{Name: "patch missing update", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/nope.txt\n@@\n-a\n+b\n*** End Patch"}},
		{Name: "patch empty", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** End Patch"}},
		{Name: "patch invalid", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Frobnicate File: foo\n*** End Patch"}},
		{Name: "patch missing text", Tool: "apply_patch", Args: map[string]any{}},
		{Name: "patch move+add", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Update File: $DIR/p11.txt\n*** Move to: $DIR/p11dst.txt\n@@\n-src\n+from-src\n*** Add File: $DIR/p11dst.txt\n+from-add\n*** End Patch"},
			Fixture: []diffFixture{{Path: "p11.txt", Content: "src\n"}}},
		{Name: "patch delete directory", Tool: "apply_patch", Args: map[string]any{"patchText": "*** Begin Patch\n*** Delete File: $DIR/adir\n*** End Patch"},
			Fixture: []diffFixture{{Path: "adir/keep.txt", Content: "x\n"}}},
	}
}

func substitute(value any, dir string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "$DIR", dir)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = substitute(item, dir)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = substitute(item, dir)
		}
		return out
	default:
		return value
	}
}

func setupFixtures(t *testing.T, dir string, fixtures []diffFixture) {
	t.Helper()
	for _, fixture := range fixtures {
		path := filepath.Join(dir, fixture.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatalf("fixture mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(fixture.Content), 0o644); err != nil {
			t.Fatalf("fixture write: %v", err)
		}
	}
}

func runGoCase(t *testing.T, dir string, item diffCase) types.ToolResult {
	t.Helper()
	ctx := runtime.RunWithContext(context.Background(), &runtime.CallContext{
		Cwd:      dir,
		Env:      map[string]string{"CAVE_TOOLS_MODE": "enforce"},
		ClientID: "local",
	}, func(ctx context.Context) context.Context { return ctx })
	args := substitute(item.Args, dir).(map[string]any)
	var (
		result types.ToolResult
		err    error
	)
	switch item.Tool {
	case "edit":
		result, err = handle(ctx, args)
	case "write":
		result, err = write.Tool.Handler(ctx, args)
	case "apply_patch":
		result, err = applypatch.Tool.Handler(ctx, args)
	default:
		t.Fatalf("unknown tool %q", item.Tool)
	}
	if err != nil {
		t.Fatalf("go handler error: %v", err)
	}
	return result
}

func TestDifferentialAgainstTypeScript(t *testing.T) {
	root := repoRoot(t)
	dist := filepath.Join(root, "dist")
	if _, err := os.Stat(filepath.Join(dist, "tools", "edit.js")); err != nil {
		t.Skip("dist/ missing: run pnpm build to enable the differential harness")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: run pnpm build to enable the differential harness")
	}

	cases := differentialCases()
	tsDir := t.TempDir()
	goDir := t.TempDir()

	specPath := filepath.Join(t.TempDir(), "spec.json")

	// One TS process runs the whole sequence so stateful fixtures behave like
	// the Go run. Fixtures are created just before each case in both runs.
	scriptPath := filepath.Join(t.TempDir(), "runner.mjs")
	runnerScript := strings.Replace(tsRunner, "for (const item of spec.cases) {",
		"const { mkdirSync, writeFileSync } = await import(\"node:fs\");\n"+
			"const path = await import(\"node:path\");\n"+
			"for (const item of spec.cases) {\n"+
			"  for (const fixture of item.fixture || []) {\n"+
			"    const target = path.join(process.env.CAVE_DIFF_DIR, fixture.path);\n"+
			"    mkdirSync(path.dirname(target), { recursive: true });\n"+
			"    writeFileSync(target, fixture.content, \"utf-8\");\n"+
			"  }", 1)
	if err := os.WriteFile(scriptPath, []byte(runnerScript), 0o644); err != nil {
		t.Fatalf("write runner: %v", err)
	}

	// The spec's args already carry the TS dir; fixtures need the same dir.
	tsSpecWithFixtures := tsSpec{Dist: dist, Cases: make([]diffCase, len(cases))}
	for i, item := range cases {
		tsSpecWithFixtures.Cases[i] = diffCase{
			Name:    item.Name,
			Tool:    item.Tool,
			Args:    substitute(item.Args, tsDir).(map[string]any),
			Fixture: item.Fixture,
		}
	}
	specBytes, marshalErr := json.Marshal(tsSpecWithFixtures)
	if marshalErr != nil {
		t.Fatalf("marshal spec: %v", marshalErr)
	}
	if err := os.WriteFile(specPath, specBytes, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	dataDir := t.TempDir()
	cmd := exec.Command(nodePath, scriptPath, specPath)
	cmd.Dir = tsDir
	cmd.Env = append(os.Environ(),
		"CAVE_TOOLS_DATA_DIR="+dataDir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(dataDir, "claude"),
		"CAVE_TOOLS_MODE=enforce",
		"CAVE_DIFF_DIR="+tsDir,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		t.Fatalf("ts runner failed: %v\n%s", runErr, stderr.String())
	}

	var ts tsOutput
	if err := json.Unmarshal(stdout.Bytes(), &ts); err != nil {
		t.Fatalf("decode ts output: %v\n%s", err, stdout.String())
	}
	if len(ts.Results) != len(cases) {
		t.Fatalf("ts returned %d results, want %d", len(ts.Results), len(cases))
	}

	// Schema parity: the exact JSON the TS tool object declares.
	goSchemas := map[string]json.RawMessage{
		"edit":        Tool.InputSchema,
		"write":       write.Tool.InputSchema,
		"apply_patch": applypatch.Tool.InputSchema,
	}
	for name, raw := range goSchemas {
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			t.Fatalf("%s schema is not valid JSON: %v", name, err)
		}
		if got, want := compact.String(), ts.Schemas[name]; got != want {
			t.Fatalf("%s inputSchema differs from TS\n go: %s\n ts: %s", name, got, want)
		}
	}

	for i, item := range cases {
		dir := goDir
		setupFixtures(t, dir, item.Fixture)
		goResult := runGoCase(t, dir, item)

		var tsResult any
		if err := json.Unmarshal(ts.Results[i], &tsResult); err != nil {
			t.Fatalf("%s: decode ts result: %v", item.Name, err)
		}
		goBytes, err := json.Marshal(goResult)
		if err != nil {
			t.Fatalf("%s: marshal go result: %v", item.Name, err)
		}
		var goAny any
		if err := json.Unmarshal(goBytes, &goAny); err != nil {
			t.Fatalf("%s: decode go result: %v", item.Name, err)
		}
		normalizedTS := normalizePaths(tsResult, tsDir, "$DIR")
		normalizedGo := normalizePaths(goAny, goDir, "$DIR")
		if !reflect.DeepEqual(normalizedGo, normalizedTS) {
			goJSON, _ := json.Marshal(normalizedGo)
			tsJSON, _ := json.Marshal(normalizedTS)
			t.Fatalf("%s: ToolResult mismatch\n go: %s\n ts: %s", item.Name, goJSON, tsJSON)
		}
	}
	// The final trees must match too: same files, same bytes on disk.
	tsTree := snapshotTree(t, tsDir)
	goTree := snapshotTree(t, goDir)
	if !reflect.DeepEqual(goTree, tsTree) {
		for path, tsContent := range tsTree {
			if goContent, ok := goTree[path]; !ok {
				t.Fatalf("tree mismatch: go is missing %s", path)
			} else if goContent != tsContent {
				t.Fatalf("tree mismatch at %s\n go: %q\n ts: %q", path, goContent, tsContent)
			}
		}
		for path := range goTree {
			if _, ok := tsTree[path]; !ok {
				t.Fatalf("tree mismatch: go has extra %s", path)
			}
		}
	}
	t.Logf("differential: %d cases, 3 schemas, 0 result mismatches, %d files identical", len(cases), len(tsTree))
}

func TestDifferentialCaseCount(t *testing.T) {
	if len(differentialCases()) < 50 {
		t.Fatalf("differential suite shrank to %d cases", len(differentialCases()))
	}
}

// snapshotTree maps relative path -> file bytes (dirs get a trailing slash).
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func normalizePaths(value any, dir, placeholder string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, dir, placeholder)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalizePaths(item, dir, placeholder)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = normalizePaths(item, dir, placeholder)
		}
		return out
	default:
		return value
	}
}
