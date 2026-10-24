package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/types"
)

// TestToolErrorsInteropWithTS proves both implementations read each other's
// rows from the shared SQLite file (same schema, same data dir).
func TestToolErrorsInteropWithTS(t *testing.T) {
	root := toolErrorsRepoRoot(t)
	dist := filepath.Join(root, "dist", "runtime", "tool-errors.js")
	if _, err := os.Stat(dist); err != nil {
		t.Skip("dist/runtime/tool-errors.js is missing: run pnpm build first")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}

	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	autoFlushDisabledForTests = true
	SetKnownToolNames([]string{"cave__ls", "cave__read"})
	ResetToolErrorStateForTests()
	t.Cleanup(func() {
		ResetToolErrorStateForTests()
		autoFlushDisabledForTests = false
	})

	// TS writes, Go reads.
	script := filepath.Join(t.TempDir(), "write.mjs")
	source := "import { observeToolCall, flushToolErrorQueue, listToolErrors } from " + jsonString(pathToFileURL(dist)) + ";\n" +
		"import " + jsonString(pathToFileURL(filepath.Join(root, "dist", "dispatch.js"))) + ";\n" +
		"observeToolCall({ tool: \"cave__ls\", args: { path: \"/x\" }, schemaProps: [\"path\"], sessionId: \"s\", startedAt: Date.now()," +
		" result: { content: [{ type: \"text\", text: \"Path not found\" }], isError: true } });\n" +
		"flushToolErrorQueue();\n" +
		"process.stdout.write(JSON.stringify(listToolErrors(10)));\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CAVE_TOOLS_DATA_DIR="+dir, "CLAUDE_CONFIG_DIR="+filepath.Join(dir, "claude"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ts write: %v (stderr %s)", err, stderr.String())
	}
	rows := ListToolErrors(10)
	if len(rows) != 1 {
		t.Fatalf("go read after TS write: rows = %d, want 1 (TS %s)", len(rows), stdout.String())
	}
	if rows[0].Tool != "cave__ls" || rows[0].Outcome != ToolErrorSoft || rows[0].ReasonCode != ReasonNotFound {
		t.Errorf("row = %+v", rows[0])
	}
	if len(rows[0].ID) != 36 {
		t.Errorf("id = %q", rows[0].ID)
	}

	// Go writes (same dir), TS reads.
	ResetToolErrorStateForTests()
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        "cave__read",
		Args:        map[string]any{"file_path": "/missing.txt"},
		SchemaProps: []string{"file_path"},
		SessionID:   "s",
		StartedAt:   time.Now().UnixMilli(),
		Result: types.ToolResult{
			Content: []types.Content{types.Text("File not found")},
			IsError: true,
		},
	})
	FlushToolErrorQueue()

	script = filepath.Join(t.TempDir(), "read.mjs")
	source = "import { listToolErrors, countToolErrors } from " + jsonString(pathToFileURL(dist)) + ";\n" +
		"process.stdout.write(JSON.stringify({ rows: listToolErrors(10), counts: countToolErrors() }));\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(node, script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CAVE_TOOLS_DATA_DIR="+dir, "CLAUDE_CONFIG_DIR="+filepath.Join(dir, "claude"))
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ts read: %v (stderr %s)", err, stderr.String())
	}
	var payload struct {
		Rows []struct {
			Tool       string `json:"tool"`
			Outcome    string `json:"outcome"`
			ReasonCode string `json:"reasonCode"`
		} `json:"rows"`
		Counts struct {
			Total int64 `json:"total"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("ts output %q: %v", stdout.String(), err)
	}
	if payload.Counts.Total != 2 || len(payload.Rows) != 2 {
		t.Fatalf("TS read after Go write: %+v", payload)
	}
	found := false
	for _, row := range payload.Rows {
		if row.Tool == "cave__read" && row.Outcome == "soft" && row.ReasonCode == "not_found" {
			found = true
		}
	}
	if !found {
		t.Errorf("Go row not visible to TS: %+v", payload.Rows)
	}
}

func toolErrorsRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func pathToFileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}
