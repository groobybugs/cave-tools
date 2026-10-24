package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/types"
)

// toolErrorsTestEnv isolates one test: fresh data dir, cleared sink state and
// no asynchronous flushes (Go has no setImmediate turn to wait on).
func toolErrorsTestEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	autoFlushDisabledForTests = true
	SetKnownToolNames([]string{
		"cave__read", "cave__bash", "cave__bash_start", "cave__bash_status",
		"cave__bash_stop", "cave__grep", "cave__find", "cave__ls", "cave__write",
		"cave__invalidate", "cave__edit", "cave__apply_patch", "cave__websearch",
		"cave__webfetch", "cave__compress", "cave__status", "cave__configure",
	})
	ResetToolErrorStateForTests()
	t.Cleanup(func() {
		ResetToolErrorStateForTests()
		autoFlushDisabledForTests = false
	})
	return dir
}

func TestToolErrorConstants(t *testing.T) {
	if ToolErrorQueueCap != 256 {
		t.Errorf("ToolErrorQueueCap = %d, want 256", ToolErrorQueueCap)
	}
	if ToolErrorMaxRows != 5000 {
		t.Errorf("ToolErrorMaxRows = %d, want 5000", ToolErrorMaxRows)
	}
	if ToolErrorRetentionDays != 7 {
		t.Errorf("ToolErrorRetentionDays = %d, want 7", ToolErrorRetentionDays)
	}
}

func TestClassifySoftReasonCode(t *testing.T) {
	cases := []struct {
		name string
		tool string
		text string
		want ToolErrorReasonCode
	}{
		{"path not found", "cave__ls", "Path not found or not a directory: /x", ReasonNotFound},
		{"required", "cave__websearch", "Error: query is required", ReasonInvalidArgs},
		{"out of range", "cave__read", "Offset 5 is out of range", ReasonInvalidArgs},
		{"bash exit", "cave__bash", "out\n\n[exit: 1]", ReasonExecFailed},
		{"webfetch status", "cave__webfetch", "Error: Request failed with status 500", ReasonUpstream},
		{"not utf8", "cave__read", "Error: File is not valid UTF-8: f", ReasonUnreadable},
		{"stale hash", "cave__edit", "Error: expected_hash mismatch — file changed since read. Re-run cave__read.", ReasonStaleHash},
		{"stale range", "cave__edit", "Error: expected_range_checksum mismatch for lines 3-5. Re-read with cave__read.", ReasonStaleRange},
		{"batch checksum", "cave__edit", "Error: edits[2]: checksum failed", ReasonStaleRange},
		{"batch hash", "cave__edit", "Error: edits[0]: hash failed", ReasonStaleHash},
		{"not unique", "cave__edit", "Error: old_string is not unique (2+ matches). Add more context.", ReasonNotUnique},
		{"batch not unique", "cave__edit", "Error: edits[4]: old_string is not unique after prior edits.", ReasonNotUnique},
		{"overlap", "cave__edit", "Error: edits overlap. Merge them into one edit.", ReasonOverlap},
		{"move mix", "cave__edit", "Error: cannot mix move and range edits in one batch.", ReasonOverlap},
		{"old string gone", "cave__edit", "Error: old_string not found — new_string already present, edit already applied?", ReasonNotFound},
		{"batch old string gone", "cave__edit", "Error: edits[1]: old_string not found in file.", ReasonNotFound},
		{"file path required", "cave__edit", "Error: file_path is required", ReasonInvalidArgs},
		{"empty edits", "cave__edit", "Error: edits array is empty", ReasonInvalidArgs},
		{"hex short", "cave__edit", "Error: expected_hash must be at least 8 hex characters", ReasonInvalidArgs},
		{"integers", "cave__edit", "Error: edits[1]: start_line and end_line must be integers", ReasonInvalidArgs},
		{
			"anchored wins",
			"cave__edit",
			"Error: expected_hash mismatch — file changed.\nexpected: abcd\nactual: ef12\nnote: file not found fallback",
			ReasonStaleHash,
		},
		{"empty", "cave__ls", "", ReasonUnknown},
		{"novel", "cave__ls", "Error: something totally novel", ReasonUnknown},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifySoftReasonCode(test.tool, test.text); got != test.want {
				t.Errorf("ClassifySoftReasonCode(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

func TestParseBatchIndex(t *testing.T) {
	cases := []struct {
		text string
		want *int64
	}{
		{"Error: edits[12]: checksum failed", int64Ptr(12)},
		{"edits[0]: hash failed", int64Ptr(0)},
		{"note: see edits[3] later", nil},
		{"", nil},
	}
	for _, test := range cases {
		got := ParseBatchIndex(test.text)
		if (got == nil) != (test.want == nil) || (got != nil && *got != *test.want) {
			t.Errorf("ParseBatchIndex(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestClassifyThrownReasonCode(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    ToolErrorReasonCode
	}{
		{"TypeError", "Cannot read properties of undefined (reading 'edits')", ReasonInvalidArgs},
		{"Error", "Unknown tool: cave__x", ReasonInternal},
		{"CloudflareChallengeError", "challenge", ReasonUpstream},
		{"Error", "File changed after it was read: f", ReasonStaleWrite},
		{"Error", "weird boom", ReasonInternal},
	}
	for _, test := range cases {
		if got := ClassifyThrownReasonCode("cave__edit", test.name, test.message); got != test.want {
			t.Errorf("ClassifyThrownReasonCode(%q, %q) = %q, want %q", test.name, test.message, got, test.want)
		}
	}
	if got := NormalizeThrownKind("TypeError"); got != "TypeError" {
		t.Errorf("NormalizeThrownKind(TypeError) = %q", got)
	}
	if got := NormalizeThrownKind("WeirdCustom"); got != "Error" {
		t.Errorf("NormalizeThrownKind(WeirdCustom) = %q", got)
	}
}

func TestSanitizers(t *testing.T) {
	SetKnownToolNames([]string{"cave__ls", "cave__read"})
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"known", SanitizeToolName("cave__ls"), "cave__ls"},
		{"unknown", SanitizeToolName("cave__evil"), "unknown_tool"},
		{"not string", SanitizeToolName(123), "unknown_tool"},
		{"mode", SanitizeMode("enforce"), "enforce"},
		{"bad mode", SanitizeMode("weird"), "default"},
		{"nil mode", SanitizeMode(nil), "default"},
	}
	for _, test := range cases {
		if test.got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, test.got, test.want)
		}
	}
}

func TestSummarizeArgs(t *testing.T) {
	summary := SummarizeArgs(
		map[string]any{"file_path": "f", "limit": 3, "bogus": 1, "__sessionId": "s"},
		[]string{"file_path", "limit"},
	)
	if strings.Join(summary.Keys, ",") != "file_path,limit" {
		t.Errorf("keys = %v", summary.Keys)
	}
	if len(summary.Types) != 2 || summary.Types["file_path"] != "string" || summary.Types["limit"] != "number" {
		t.Errorf("types = %v", summary.Types)
	}
	if summary.Count != 3 {
		t.Errorf("count = %d, want 3", summary.Count)
	}
	if summary.UnknownCount != 1 {
		t.Errorf("unknownCount = %d, want 1", summary.UnknownCount)
	}
}

func TestDeriveOperation(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want *string
	}{
		{"cave__edit", map[string]any{"edits": []any{}}, stringPtr("batch")},
		{"cave__edit", map[string]any{"delete": true}, stringPtr("delete")},
		{"cave__edit", map[string]any{"insert_before": 3}, stringPtr("move")},
		{"cave__edit", map[string]any{"start_line": 1}, stringPtr("range")},
		{"cave__edit", map[string]any{"old_string": "x"}, stringPtr("string")},
		{"cave__read", map[string]any{"offset": 2}, stringPtr("paged")},
		{"cave__read", map[string]any{}, stringPtr("full")},
		{"cave__ls", map[string]any{"path": "x"}, nil},
	}
	for _, test := range cases {
		got := DeriveOperation(test.tool, test.args)
		if (got == nil) != (test.want == nil) || (got != nil && *got != *test.want) {
			t.Errorf("DeriveOperation(%s, %v) = %v, want %v", test.tool, test.args, got, test.want)
		}
	}
}

func TestHashes(t *testing.T) {
	if got := SessionHashFor("test-session"); len(got) != 16 || strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("session hash = %q", got)
	}
	hash := FileHashFor(map[string]any{"file_path": "/nope/secret/f.txt"})
	if hash == nil || len(*hash) != 16 {
		t.Fatalf("file hash = %v", hash)
	}
	if strings.Contains(*hash, "secret") {
		t.Errorf("file hash leaks path: %q", *hash)
	}
	if got := FileHashFor(map[string]any{"path": ""}); got != nil {
		t.Errorf("empty path hash = %v, want nil", got)
	}
}

func TestSoftCapture(t *testing.T) {
	toolErrorsTestEnv(t)
	SetKnownToolNames([]string{"cave__ls"})

	result := types.ToolResult{
		Content: []types.Content{types.Text("Path not found or not a directory: /x")},
		IsError: true,
	}
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        "cave__ls",
		Args:        map[string]any{"path": "/x"},
		SchemaProps: []string{"path"},
		SessionID:   "test-session",
		StartedAt:   time.Now().UnixMilli(),
		Result:      result,
	})
	if depth := ToolErrorQueueDepthForTests(); depth != 1 {
		t.Fatalf("queue depth = %d, want 1", depth)
	}
	if count := CountToolErrors().Total; count != 0 {
		t.Fatalf("count before flush = %d, want 0", count)
	}
	FlushToolErrorQueue()
	rows := ListToolErrors(10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Outcome != ToolErrorSoft || row.Tool != "cave__ls" || row.ReasonCode != ReasonNotFound {
		t.Errorf("row = %+v", row)
	}
	if strings.Join(row.ArgKeys, ",") != "path" || row.ArgTypes["path"] != "string" || row.UnknownArgCount != 0 {
		t.Errorf("args = %+v", row)
	}
	if row.ElapsedMs < 0 {
		t.Errorf("elapsed = %d", row.ElapsedMs)
	}
	if len(row.SessionHash) != 16 {
		t.Errorf("session hash = %q", row.SessionHash)
	}
	if !result.IsError {
		t.Errorf("result mutated")
	}
}

func TestThrownCapture(t *testing.T) {
	toolErrorsTestEnv(t)
	SetKnownToolNames([]string{"cave__nope"})
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:      "cave__nope",
		StartedAt: time.Now().UnixMilli(),
		Thrown:    &ThrownError{Name: "Error", Message: "Unknown tool: cave__nope"},
	})
	FlushToolErrorQueue()
	rows := ListToolErrors(10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Outcome != ToolErrorThrown || row.ReasonCode != ReasonInternal {
		t.Errorf("row = %+v", row)
	}
	if row.ErrorKind == nil || *row.ErrorKind != "Error" {
		t.Errorf("kind = %v", row.ErrorKind)
	}
}

func TestThrownNullAndUndefinedRecorded(t *testing.T) {
	toolErrorsTestEnv(t)
	base := ObserveToolCallOptions{
		Tool:        "cave__ls",
		Args:        map[string]any{"path": "/x"},
		SchemaProps: []string{"path"},
		SessionID:   "s",
		StartedAt:   time.Now().UnixMilli(),
	}
	nullThrow := base
	nullThrow.Thrown = &ThrownError{Message: "null"}
	ObserveToolCall(context.Background(), nullThrow)
	undefinedThrow := base
	undefinedThrow.Args = map[string]any{"path": "/y"}
	undefinedThrow.Thrown = &ThrownError{Message: "undefined"}
	ObserveToolCall(context.Background(), undefinedThrow)
	success := base
	success.Args = map[string]any{"path": "/z"}
	success.Result = types.ToolResult{Content: []types.Content{types.Text("ok")}}
	ObserveToolCall(context.Background(), success)
	FlushToolErrorQueue()
	rows := ListToolErrors(10)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Outcome != ToolErrorThrown || row.ErrorKind == nil || *row.ErrorKind != "Error" {
			t.Errorf("row = %+v", row)
		}
	}
}

func TestSecretsNeverPersist(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	SetKnownToolNames([]string{"cave__ls", "cave__read"})
	secret := "sk-ant-fake0123456789abcdef"
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        "cave__read",
		Args:        map[string]any{"file_path": "/nope/" + secret + "/f.txt"},
		SchemaProps: []string{"file_path"},
		SessionID:   "s",
		StartedAt:   time.Now().UnixMilli(),
		Result: types.ToolResult{
			Content: []types.Content{types.Text("File not found")},
			IsError: true,
		},
	})
	FlushToolErrorQueue()
	rows := ListToolErrors(10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	dump, _ := json.Marshal(rows)
	if strings.Contains(string(dump), secret) {
		t.Errorf("record leaks secret: %s", dump)
	}
	dbBytes, err := os.ReadFile(filepath.Join(dir, "cave-tools.db"))
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(dbBytes), secret) {
		t.Errorf("db bytes leak secret")
	}
	if rows[0].FileHash == nil || len(*rows[0].FileHash) != 16 {
		t.Errorf("file hash = %v", rows[0].FileHash)
	}
}

func TestCallerMetadataSanitized(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	SetKnownToolNames([]string{"cave__ls"})
	secret := "sk-ant-fake9999888877776666"
	t.Setenv("CAVE_TOOLS_MODE", secret)
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:      "cave__" + secret,
		StartedAt: time.Now().UnixMilli(),
		Thrown:    &ThrownError{Name: secret, Message: secret},
	})
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        "cave__ls",
		Args:        map[string]any{secret: "v", "path": "x"},
		SchemaProps: []string{"path"},
		SessionID:   "s",
		StartedAt:   time.Now().UnixMilli(),
		Thrown:      &ThrownError{Name: secret, Message: secret},
	})
	FlushToolErrorQueue()
	rows := ListToolErrors(20)
	if len(rows) < 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	dump, _ := json.Marshal(rows)
	if strings.Contains(string(dump), secret) {
		t.Errorf("records leak secret: %s", dump)
	}
	dbBytes, _ := os.ReadFile(filepath.Join(dir, "cave-tools.db"))
	if strings.Contains(string(dbBytes), secret) {
		t.Errorf("db bytes leak secret")
	}
	for _, row := range rows {
		if row.Tool != "cave__ls" && row.Tool != "unknown_tool" {
			t.Errorf("tool = %q", row.Tool)
		}
		if row.Mode != "default" && row.Mode != "off" && row.Mode != "hint" && row.Mode != "enforce" && row.Mode != "strict" {
			t.Errorf("mode = %q", row.Mode)
		}
		if row.ErrorKind != nil && *row.ErrorKind != "Error" {
			t.Errorf("kind = %v", row.ErrorKind)
		}
	}
}

func TestDisableMatrix(t *testing.T) {
	mk := func(env map[string]string) ObserveToolCallOptions {
		return ObserveToolCallOptions{
			Tool:        "cave__ls",
			Args:        map[string]any{"path": "/m"},
			SchemaProps: []string{"path"},
			SessionID:   "s",
			StartedAt:   time.Now().UnixMilli(),
			Env:         env,
			Result: types.ToolResult{
				Content: []types.Content{types.Text("Path not found")},
				IsError: true,
			},
		}
	}

	toolErrorsTestEnv(t)
	t.Setenv("CAVE_TOOLS_ERROR_LOG", "")
	ObserveToolCall(context.Background(), mk(nil))
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 1 {
		t.Errorf("proc on + caller unset: total = %d, want 1", got)
	}

	toolErrorsTestEnv(t)
	ObserveToolCall(context.Background(), mk(map[string]string{"CAVE_TOOLS_ERROR_LOG": "0"}))
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("caller disable: total = %d, want 0", got)
	}
	if depth := ToolErrorQueueDepthForTests(); depth != 0 {
		t.Errorf("disabled call queued: depth = %d", depth)
	}

	toolErrorsTestEnv(t)
	t.Setenv("CAVE_TOOLS_ERROR_LOG", "0")
	ObserveToolCall(context.Background(), mk(nil))
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("global disable: total = %d, want 0", got)
	}

	toolErrorsTestEnv(t)
	t.Setenv("CAVE_TOOLS_ERROR_LOG", "0")
	ObserveToolCall(context.Background(), mk(map[string]string{"CAVE_TOOLS_ERROR_LOG": "1"}))
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("global disable wins: total = %d, want 0", got)
	}
}

func TestRetentionPruning(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	observeSoft(t, "cave__ls", "/nonexistent-a")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 1 {
		t.Fatalf("total = %d", got)
	}
	db := openTestDB(t, dir)
	if _, err := db.Exec("UPDATE tool_errors SET created_at = ?", time.Now().UnixMilli()-8*24*60*60*1000); err != nil {
		t.Fatalf("update: %v", err)
	}
	_ = db.Close()
	observeSoft(t, "cave__ls", "/nonexistent-b")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 1 {
		t.Errorf("old row pruned, new row kept: total = %d", got)
	}
}

func TestRowCap(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	observeSoft(t, "cave__ls", "/nonexistent-cap")
	FlushToolErrorQueue()
	db := openTestDB(t, dir)
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare("INSERT OR IGNORE INTO tool_errors (id, created_at, tool, outcome, reason_code, elapsed_ms, mode, agent, session_hash, arg_keys, arg_types) VALUES (?, ?, 'cave__ls', 'soft', 'not_found', 1, 'default', 'unknown', 'aaaa', '[]', '{}')")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := 0; i < 5005; i++ {
		if _, err := stmt.Exec("bulk-"+strconv.Itoa(i), now-int64(i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_ = db.Close()
	observeSoft(t, "cave__ls", "/nonexistent-cap2")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got > 5000 {
		t.Errorf("total %d exceeds cap", got)
	}
}

func TestQueueCap(t *testing.T) {
	toolErrorsTestEnv(t)
	for i := 0; i < 300; i++ {
		ObserveToolCall(context.Background(), ObserveToolCallOptions{
			Tool:        "cave__ls",
			Args:        map[string]any{"path": "/n" + strconv.Itoa(i)},
			SchemaProps: []string{"path"},
			SessionID:   "s",
			StartedAt:   time.Now().UnixMilli(),
			Result: types.ToolResult{
				Content: []types.Content{types.Text("Path not found")},
				IsError: true,
			},
		})
	}
	if depth := ToolErrorQueueDepthForTests(); depth != 256 {
		t.Fatalf("queue depth = %d, want 256", depth)
	}
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 256 {
		t.Errorf("persisted = %d, want 256", got)
	}
}

func TestNoCrossDirContamination(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	autoFlushDisabledForTests = true
	SetKnownToolNames([]string{
		"cave__read", "cave__bash", "cave__bash_start", "cave__bash_status",
		"cave__bash_stop", "cave__grep", "cave__find", "cave__ls", "cave__write",
		"cave__invalidate", "cave__edit", "cave__apply_patch", "cave__websearch",
		"cave__webfetch", "cave__compress", "cave__status", "cave__configure",
	})
	ResetToolErrorStateForTests()
	t.Cleanup(func() {
		ResetToolErrorStateForTests()
		autoFlushDisabledForTests = false
	})
	t.Setenv("CAVE_TOOLS_DATA_DIR", dirA)
	observeSoft(t, "cave__ls", "/nonexistent-xa")
	t.Setenv("CAVE_TOOLS_DATA_DIR", dirB)
	observeSoft(t, "cave__ls", "/nonexistent-xb")
	FlushToolErrorQueue()
	for _, dir := range []string{dirA, dirB} {
		db := openTestDB(t, dir)
		var count int64
		if err := db.QueryRow("SELECT COUNT(*) FROM tool_errors").Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		_ = db.Close()
		if count != 1 {
			t.Errorf("%s count = %d, want 1", dir, count)
		}
	}
}

func TestUnwritableStoreIsolation(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_TOOLS_DATA_DIR", file)
	autoFlushDisabledForTests = true
	SetKnownToolNames([]string{
		"cave__read", "cave__bash", "cave__bash_start", "cave__bash_status",
		"cave__bash_stop", "cave__grep", "cave__find", "cave__ls", "cave__write",
		"cave__invalidate", "cave__edit", "cave__apply_patch", "cave__websearch",
		"cave__webfetch", "cave__compress", "cave__status", "cave__configure",
	})
	ResetToolErrorStateForTests()
	t.Cleanup(func() {
		ResetToolErrorStateForTests()
		autoFlushDisabledForTests = false
	})
	observeSoft(t, "cave__ls", "/nonexistent-unwritable")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("total = %d, want 0", got)
	}
}

func TestLockedStoreDefers(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	observeSoft(t, "cave__ls", "/nonexistent-init")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 1 {
		t.Fatalf("initial total = %d", got)
	}

	lock := openTestDB(t, dir)
	if _, err := lock.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	start := time.Now()
	observeSoft(t, "cave__ls", "/nonexistent-busy")
	FlushToolErrorQueue()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("logging blocked the call for %v", elapsed)
	}
	if depth := ToolErrorQueueDepthForTests(); depth != 1 {
		t.Errorf("deferred queue depth = %d, want 1", depth)
	}
	if _, err := lock.Exec("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	_ = lock.Close()
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 2 {
		t.Errorf("deferred row not flushed: total = %d, want 2", got)
	}
}

func TestCorruptStoreReadsEmpty(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	if err := os.WriteFile(filepath.Join(dir, "cave-tools.db"), []byte("not a database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows := ListToolErrors(5); len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
	if got := CountToolErrors().Total; got != 0 {
		t.Errorf("total = %d, want 0", got)
	}
	if rows := ListToolErrors(5); len(rows) != 0 {
		t.Errorf("repeat rows = %d, want 0", len(rows))
	}
}

func TestAnchoredBatchIndexStored(t *testing.T) {
	toolErrorsTestEnv(t)
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        "cave__edit",
		Args:        map[string]any{"file_path": "f", "edits": []any{map[string]any{}, map[string]any{}, map[string]any{}}},
		SchemaProps: []string{"file_path", "edits"},
		SessionID:   "s",
		StartedAt:   time.Now().UnixMilli(),
		Result: types.ToolResult{
			Content: []types.Content{types.Text("Error: edits[2]: old_string is not unique (2+ matches).")},
			IsError: true,
		},
	})
	FlushToolErrorQueue()
	rows := ListToolErrors(10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].ReasonCode != ReasonNotUnique {
		t.Errorf("reason = %q", rows[0].ReasonCode)
	}
	if rows[0].BatchIndex == nil || *rows[0].BatchIndex != 2 {
		t.Errorf("batch = %v", rows[0].BatchIndex)
	}
	if rows[0].Operation == nil || *rows[0].Operation != "batch" {
		t.Errorf("operation = %v", rows[0].Operation)
	}
}

func TestSubagentAgentHint(t *testing.T) {
	toolErrorsTestEnv(t)
	SetSubagentDetector(func(context.Context) bool { return true })
	t.Cleanup(func() { SetSubagentDetector(nil) })
	observeSoft(t, "cave__ls", "/nonexistent-agent")
	FlushToolErrorQueue()
	rows := ListToolErrors(10)
	if len(rows) != 1 || rows[0].Agent != "subagent" {
		t.Fatalf("rows = %+v", rows)
	}
}

// helpers

func observeSoft(t *testing.T, tool string, path string) {
	t.Helper()
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        tool,
		Args:        map[string]any{"path": path},
		SchemaProps: []string{"path"},
		SessionID:   "test-session",
		StartedAt:   time.Now().UnixMilli(),
		Result: types.ToolResult{
			Content: []types.Content{types.Text("Path not found")},
			IsError: true,
		},
	})
}

func openTestDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "cave-tools.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func int64Ptr(value int64) *int64 { return &value }

func TestLockedStoreInitialization(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	lock := openTestDB(t, dir)
	if _, err := lock.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	start := time.Now()
	observeSoft(t, "cave__ls", "/nonexistent-busy-init")
	FlushToolErrorQueue()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("logging blocked the call for %v", elapsed)
	}
	if depth := ToolErrorQueueDepthForTests(); depth != 1 {
		t.Errorf("deferred queue depth = %d, want 1", depth)
	}
	if _, err := lock.Exec("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	_ = lock.Close()
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 1 {
		t.Errorf("deferred row not flushed after unlock: total = %d, want 1", got)
	}
}
