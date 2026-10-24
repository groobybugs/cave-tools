package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	callruntime "github.com/groobybugs/cave-tools/go/internal/runtime"
)

// TestDispatchNilArgsDifferential pins missing/null argument behavior against
// the built TS reference. It skips when dist/ is absent.
func TestDispatchNilArgsDifferential(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "dispatch.js")); err != nil {
		t.Skip("dist/dispatch.js missing; run pnpm build")
	}
	cwd := t.TempDir()
	tsData := filepath.Join(cwd, "ts-data")
	goData := filepath.Join(cwd, "go-data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", goData)
	t.Cleanup(func() { callruntime.FlushToolErrorQueue() })

	names := []string{
		"cave__bash", "cave__write", "cave__invalidate", "cave__apply_patch",
		"cave__grep", "cave__find", "cave__ls", "cave__websearch",
		"cave__webfetch", "cave__compress", "cave__configure",
		"cave__edit", "cave__read", "cave__status",
	}
	namesJSON, _ := json.Marshal(names)
	script := `
import { pathToFileURL } from "node:url";
const { callTool } = await import(pathToFileURL(process.argv[2]).href);
const names = JSON.parse(process.argv[1]);
const out = [];
for (const name of names) {
  for (const kind of ["null", "undefined"]) {
    const args = kind === "null" ? null : undefined;
    try {
      const result = await callTool(name, args, { sessionId: "diff", cwd: process.cwd(), env: {} });
      const first = result?.content?.[0];
      out.push({ name, kind, ok: true, isError: !!result?.isError, text: first?.type === "text" ? first.text : "" });
    } catch (error) {
      out.push({ name, kind, ok: false, errorName: error.name, errorMessage: error.message });
    }
  }
}
process.stdout.write(JSON.stringify(out));
`
	cmd := exec.Command(node, "--input-type=module", "-e", script, string(namesJSON), filepath.Join(root, "dist", "dispatch.js"))
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CAVE_TOOLS_DATA_DIR="+tsData, "CAVE_TOOLS_DAEMON=0")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var want []struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		OK           bool   `json:"ok"`
		IsError      bool   `json:"isError"`
		Text         string `json:"text"`
		ErrorName    string `json:"errorName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(output, &want); err != nil {
		t.Fatalf("decode TS results: %v", err)
	}

	for _, expected := range want {
		state := argsNull
		if expected.Kind == "undefined" {
			state = argsUndefined
		}
		result, callErr := callTool(context.Background(), expected.Name, nil, &callruntime.CallContext{
			SessionID: "diff",
			Cwd:       cwd,
			Env:       map[string]string{},
		}, state)
		if !expected.OK {
			if callErr == nil {
				t.Fatalf("%s %s: Go returned a result, TS threw %s: %s", expected.Name, expected.Kind, expected.ErrorName, expected.ErrorMessage)
			}
			if callErr.Error() != expected.ErrorMessage {
				t.Fatalf("%s %s: Go error %q, want %q", expected.Name, expected.Kind, callErr.Error(), expected.ErrorMessage)
			}
			continue
		}
		if callErr != nil {
			t.Fatalf("%s %s: Go error %v, TS returned a result", expected.Name, expected.Kind, callErr)
		}
		if result.IsError != expected.IsError {
			t.Fatalf("%s %s: Go isError=%v, want %v", expected.Name, expected.Kind, result.IsError, expected.IsError)
		}
		if expected.Name == "cave__status" {
			continue
		}
		if got := resultText(t, result); got != expected.Text {
			t.Fatalf("%s %s: Go text %q, want %q", expected.Name, expected.Kind, got, expected.Text)
		}
	}
}

// TestDispatchLocalDifferential compares deterministic local calls with TS:
// relative/absolute paths, repeated reads and an unknown tool.
func TestDispatchLocalDifferential(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "dispatch.js")); err != nil {
		t.Skip("dist/dispatch.js missing; run pnpm build")
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "note.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tsData := filepath.Join(cwd, "ts-data")
	goData := filepath.Join(cwd, "go-data")
	t.Setenv("CAVE_TOOLS_DATA_DIR", goData)
	t.Cleanup(func() { callruntime.FlushToolErrorQueue() })

	type call struct {
		Name     string         `json:"name"`
		Args     map[string]any `json:"args,omitempty"`
		NullArgs bool           `json:"nullArgs,omitempty"`
	}
	calls := []call{
		{Name: "cave__read", Args: map[string]any{"file_path": "note.txt"}},
		{Name: "cave__read", Args: map[string]any{"file_path": "note.txt"}},
		{Name: "cave__read", Args: map[string]any{"file_path": filepath.Join(cwd, "note.txt")}},
		{Name: "cave__ls", Args: map[string]any{"path": "."}},
		{Name: "cave__nope", Args: map[string]any{"path": "x"}},
	}
	callsJSON, _ := json.Marshal(calls)
	script := `
import { pathToFileURL } from "node:url";
const { callTool } = await import(pathToFileURL(process.argv[2]).href);
const calls = JSON.parse(process.argv[1]);
const out = [];
for (const item of calls) {
  try {
    const result = await callTool(item.name, item.nullArgs ? null : item.args, { sessionId: "diff", cwd: process.cwd(), env: {} });
    const first = result?.content?.[0];
    out.push({ ok: true, isError: !!result?.isError, text: first?.type === "text" ? first.text : "" });
  } catch (error) {
    out.push({ ok: false, errorMessage: error.message });
  }
}
process.stdout.write(JSON.stringify(out));
`
	cmd := exec.Command(node, "--input-type=module", "-e", script, string(callsJSON), filepath.Join(root, "dist", "dispatch.js"))
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CAVE_TOOLS_DATA_DIR="+tsData, "CAVE_TOOLS_DAEMON=0")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var want []struct {
		OK           bool   `json:"ok"`
		IsError      bool   `json:"isError"`
		Text         string `json:"text"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(output, &want); err != nil {
		t.Fatalf("decode TS results: %v", err)
	}

	for i, item := range calls {
		args := item.Args
		state := argsPresent
		if item.NullArgs {
			args = nil
			state = argsNull
		}
		result, callErr := callTool(context.Background(), item.Name, args, &callruntime.CallContext{
			SessionID: "diff",
			Cwd:       cwd,
			Env:       map[string]string{},
		}, state)
		expected := want[i]
		if !expected.OK {
			if callErr == nil || callErr.Error() != expected.ErrorMessage {
				t.Fatalf("call %d: Go error %v, want %q", i, callErr, expected.ErrorMessage)
			}
			continue
		}
		if callErr != nil {
			t.Fatalf("call %d: Go error %v", i, callErr)
		}
		if result.IsError != expected.IsError || resultText(t, result) != expected.Text {
			t.Fatalf("call %d: Go %q/%v, want %q/%v", i, resultText(t, result), result.IsError, expected.Text, expected.IsError)
		}
	}

	if !reflect.DeepEqual(ToolNames, []string{
		"cave__read", "cave__bash", "cave__bash_start", "cave__bash_status",
		"cave__bash_stop", "cave__grep", "cave__find", "cave__ls",
		"cave__write", "cave__invalidate", "cave__edit", "cave__apply_patch",
		"cave__websearch", "cave__webfetch", "cave__compress", "cave__status",
		"cave__configure",
	}) {
		t.Fatal("ToolNames changed")
	}
}
