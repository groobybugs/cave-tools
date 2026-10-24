package compression

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// envContext builds a call context carrying env, like a daemon client call.
func envContext(env map[string]string, clientID string) context.Context {
	var captured context.Context
	rt.RunWithContext(context.Background(), &rt.CallContext{
		Env:      env,
		ClientID: clientID,
	}, func(ctx context.Context) any {
		captured = ctx
		return nil
	})
	return captured
}

// clientContext builds a daemon-client call context, like runWithContext with
// clientPid in the TS suite.
func clientContext(pid int) context.Context {
	var captured context.Context
	rt.RunWithContext(context.Background(), &rt.CallContext{
		ClientPID: &pid,
	}, func(ctx context.Context) any {
		captured = ctx
		return nil
	})
	return captured
}

// TestSubagentContext ports the subagent env-detection assertions in test.mjs
// ("filesTracked + subagent context tests").
func TestSubagentContext(t *testing.T) {
	ctx := context.Background()
	check := func(name string, env map[string]string, want bool) {
		t.Helper()
		ResetSubagentContextCache()
		if got := IsSubagentContext(envContext(env, "test")); got != want {
			t.Fatalf("%s: IsSubagentContext = %v, want %v", name, got, want)
		}
	}

	check("no signals", map[string]string{}, false)
	check("FORCE_FRESH=1", map[string]string{"CAVE_TOOLS_FORCE_FRESH": "1"}, true)
	check("FORCE_FRESH=true", map[string]string{"CAVE_TOOLS_FORCE_FRESH": "true"}, true)
	check("FORCE_FRESH=yes", map[string]string{"CAVE_TOOLS_FORCE_FRESH": "yes"}, false)
	check("CLINE_TASK_ID", map[string]string{"CLINE_TASK_ID": "abc"}, true)
	check("CLAUDE_CODE_SUBAGENT", map[string]string{"CLAUDE_CODE_SUBAGENT": "1"}, true)
	check("ANTHROPIC_SUBAGENT", map[string]string{"ANTHROPIC_SUBAGENT": "yes"}, true)
	check("CURSOR_TASK_ID", map[string]string{"CURSOR_TASK_ID": "cursor-task"}, true)
	check("TASK_ID-style", map[string]string{"FOO_TASK_ID": "t"}, true)
	check("unknown CLAUDE_CODE_*", map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, false)
	check("unknown *SUBAGENT*", map[string]string{"MY_SUBAGENT_ID": "x"}, false)
	_ = ctx
}

// TestFileCacheDedup ports the cache/filesTracked/stub assertions in test.mjs.
func TestFileCacheDedup(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ResetSubagentContextCache()
	ctx := context.Background()
	ResetCache(ctx)

	dir := t.TempDir()
	sample := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(sample, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	UpdateFileCache(ctx, sample, "sess", FileCacheUpdate{})
	if !IsFileUnchanged(ctx, sample, "sess", DefaultDeliveryKey) {
		t.Fatal("same session should stub")
	}
	if IsFileUnchanged(envContext(map[string]string{"CAVE_TOOLS_FORCE_FRESH": "1"}, "test"), sample, "sess", DefaultDeliveryKey) {
		t.Fatal("subagent context must never stub")
	}

	ResetCache(ctx)
	files := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, "f"+string(rune('0'+i))+".txt")
		if err := os.WriteFile(path, []byte("content-"+string(rune('0'+i))), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}
	for _, session := range []string{"s1", "s2"} {
		for _, path := range files {
			UpdateFileCache(ctx, path, session, FileCacheUpdate{})
		}
	}
	if got := GetCacheStats(ctx).FilesTracked; got != 6 {
		t.Fatalf("getCacheStats filesTracked = %d, want 6", got)
	}
	if got := BuildStats(ctx, nil).Cache.FilesTracked; got != 6 {
		t.Fatalf("buildStats filesTracked = %d, want 6", got)
	}

	// Dedup is representation-aware: exact repeats stub, a new delivery key
	// must be delivered once before it can stub.
	ResetCache(ctx)
	ranged := filepath.Join(dir, "ranges.ts")
	if err := os.WriteFile(ranged, []byte("export function alpha() {\n  return 1;\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstKey := "full:1:2"
	UpdateFileCache(ctx, ranged, "range-session", FileCacheUpdate{DeliveryKey: &firstKey})
	if !IsFileUnchanged(ctx, ranged, "range-session", firstKey) {
		t.Fatal("delivered representation should stub")
	}
	if IsFileUnchanged(ctx, ranged, "range-session", "full:3:0") {
		t.Fatal("a new delivery key must be fresh")
	}
	UpdateFileCache(ctx, ranged, "range-session", FileCacheUpdate{DeliveryKey: stringPtr("full:3:0")})
	if !IsFileUnchanged(ctx, ranged, "range-session", "full:3:0") {
		t.Fatal("second representation should stub after delivery")
	}

	// Stub text/read count + invalidation.
	stub := ReadStubFor(ranged, "range-session")
	if !strings.Contains(stub, "read #2") || !strings.Contains(stub, "session range-session") {
		t.Fatalf("unexpected stub %q", stub)
	}
	MarkFullDelivered(ranged, "range-session")
	if !IsFileUnchanged(ctx, ranged, "range-session", DefaultDeliveryKey) {
		t.Fatal("markFullDelivered should make the default key stub")
	}
	InvalidateFileCache(ctx, ranged)
	if IsFileUnchanged(ctx, ranged, "range-session", DefaultDeliveryKey) {
		t.Fatal("invalidated path must be fresh")
	}
	if got := ReadStubFor(ranged, DefaultSession); !strings.Contains(got, "session default") {
		t.Fatalf("default session tag: %q", got)
	}
}

func stringPtr(s string) *string { return &s }

// TestIsFileUnchangedConcurrentHash runs ApplyBudget while IsFileUnchanged
// re-hashes a large file. Under -race it proves the cache lock is not held
// across stat/re-hash and that entry reads/writes stay synchronized.
func TestIsFileUnchangedConcurrentHash(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ResetSubagentContextCache()
	ctx := context.Background()
	ResetCache(ctx)

	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789abcdef", 1<<18)), 0o600); err != nil {
		t.Fatal(err)
	}
	UpdateFileCache(ctx, path, "hash-session", FileCacheUpdate{})
	// Touch the mtime so the next check re-hashes the unchanged content.
	touched := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, touched, touched); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var workersReady sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		workersReady.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			workersReady.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if got := ApplyBudget(ctx, strings.Repeat("line\n", 50), "read"); got == "" {
					t.Error("ApplyBudget returned empty text")
					return
				}
			}
		}()
	}
	workersReady.Wait()
	stubbed := IsFileUnchanged(ctx, path, "hash-session", DefaultDeliveryKey)
	close(stop)
	wg.Wait()
	if !stubbed {
		t.Fatal("mtime touch with same content must still stub")
	}

	// The mtime update proves the re-hash branch ran while the workers did.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	utilsMu.Lock()
	cachedMtime := fileCache["hash-session"][path].mtime
	utilsMu.Unlock()
	if cachedMtime != mtimeMsMillis(info) {
		t.Fatalf("cached mtime = %v, want %v (re-hash path not taken)", cachedMtime, mtimeMsMillis(info))
	}
}

// TestHashContent ports the hash/getFileHash assertions from test.mjs.
func TestHashContent(t *testing.T) {
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := HashContent([]byte("hello")); got != want {
		t.Fatalf("HashContent = %s, want %s", got, want)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hash.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := GetFileHash(path)
	if hash == nil || *hash != want {
		t.Fatalf("GetFileHash = %v, want %s", hash, want)
	}
	if GetFileHash(filepath.Join(dir, "missing")) != nil {
		t.Fatal("missing file must hash to nil")
	}
}

// TestBounceTracking ports test.mjs "2c. Testing bounce tracking".
func TestBounceTracking(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ctx := context.Background()
	ResetStats(ctx)
	ResetSubagentContextCache()

	RecordRead(ctx, "src/a.rs", true, 50)
	RecordRead(ctx, "src/a.rs", false, 500)
	stats := GetBounceStats(ctx)
	if stats.TotalBounces != 1 {
		t.Fatalf("totalBounces = %d, want 1", stats.TotalBounces)
	}
	if stats.TotalWastedChars != 50 {
		t.Fatalf("totalWastedChars = %d, want 50", stats.TotalWastedChars)
	}

	RecordRead(ctx, "src/b.rs", true, 30)
	RecordRead(ctx, "src/b.rs", false, 400)
	if !ShouldForceFull(ctx, "src/c.rs") {
		t.Fatal("high bounce rate extension should force full reads")
	}
	RecordEdit(ctx, "src/edited.rs")
	if !ShouldForceFull(ctx, "src/edited.rs") {
		t.Fatal("recent edit should force full reads")
	}

	// resetStats clears the range-edit counters too (TS fix e8bb31f).
	RecordRangeEchoSaved(ctx, 100)
	ResetStats(ctx)
	rangeStats := GetRangeEditStats(ctx)
	if rangeStats != (RangeEditStats{}) {
		t.Fatalf("range edit stats after reset = %+v, want zeros", rangeStats)
	}
}

// TestBuildStatsClientBucketBounces ports scripts/test-port-bugs.mjs test 14:
// buildStats must report the passed bucket's bounces, not the ambient one.
func TestBuildStatsClientBucketBounces(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	const pid = 777
	clientCtx := clientContext(pid)
	ResetStats(clientCtx)
	RecordRead(clientCtx, "/tmp/pb14.ts", true, 100)
	RecordRead(clientCtx, "/tmp/pb14.ts", false, 500)
	// Written from the local context, whose bucket has no bounces.
	ReleaseClientStats(context.Background(), pid)

	var row *storage.SessionStatsRow
	for _, candidate := range ListSessionStats(true) {
		if candidate.Pid == pid {
			found := candidate
			row = &found
			break
		}
	}
	if row == nil {
		t.Fatal("client row written")
	}
	if row.Bounces.TotalBounces != 1 {
		t.Fatalf("bounces.totalBounces = %d, want 1", row.Bounces.TotalBounces)
	}
	if _, ok := row.Bounces.ByExtension[".ts"]; !ok {
		t.Fatalf("per-extension bounce kept, got %+v", row.Bounces.ByExtension)
	}
}

// TestResetStatsClearsRangeEdits ports scripts/test-port-bugs.mjs test 15.
func TestResetStatsClearsRangeEdits(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ctx := context.Background()
	ResetStats(ctx)
	RecordRangeEchoSaved(ctx, 1234)
	if GetRangeEditStats(ctx).Edits == 0 {
		t.Fatal("range edit should be recorded")
	}
	ResetStats(ctx)
	if got := GetRangeEditStats(ctx); got != (RangeEditStats{}) {
		t.Fatalf("range edit stats after reset = %+v, want zeros", got)
	}
}

// TestStripLeadingRtk ports test.mjs "14. Testing RTK strip".
func TestStripLeadingRtk(t *testing.T) {
	cases := []struct {
		command  string
		want     string
		stripped bool
	}{
		{"rtk git status", "git status", true},
		{"FOO=1 rtk git status", "FOO=1 git status", true},
		{"FOO=1 BAR=2 rtk git status", "FOO=1 BAR=2 git status", true},
		{`"rtk git status"`, "git status", true},
		{"'rtk git status'", "git status", true},
		{"rtk rtk git status", "rtk git status", true},
		{"git status", "", false},
		{"rtk", "", false},
		{"  rtk   git status  ", "git status", true},
		{"rtk.exe ls", "ls", true},
		{"RTK ls", "ls", true},
		{"`rtk` ls", "ls", true},
	}
	for _, testCase := range cases {
		got, stripped := StripLeadingRtk(testCase.command)
		if testCase.stripped {
			if !stripped || got != testCase.want {
				t.Fatalf("StripLeadingRtk(%q) = (%q,%v), want (%q,true)", testCase.command, got, stripped, testCase.want)
			}
		} else if stripped {
			t.Fatalf("StripLeadingRtk(%q) stripped unexpectedly", testCase.command)
		}
	}
}

// fakeRtkScript is the same fake rtk the TS suite installs on PATH.
const fakeRtkScript = `#!/usr/bin/env node
const args = process.argv.slice(2);
if (args[0] === "--version") {
  process.stdout.write("rtk-fake 0\n");
  process.exit(0);
}
if (args[0] === "rewrite") {
  const cmd = args.slice(1).join(" ");
  const sleepMs = Number(process.env.FAKE_RTK_SLEEP_MS || "0");
  if (sleepMs > 0) {
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, sleepMs);
  }
  if (process.env.FAKE_RTK_EMPTY === "1") process.exit(1);
  if (process.env.FAKE_RTK_IDENTICAL === "1") {
    process.stdout.write(cmd);
    process.exit(0);
  }
  process.stdout.write(cmd ? ("rtk " + cmd) : "");
  process.exit(0);
}
process.exit(1);
`

type rtkDelta struct {
	rewrites       int64
	alreadyWrapped int64
	passthrough    int64
}

func deltaRtk(before storage.RtkStats, after storage.RtkStats) rtkDelta {
	return rtkDelta{
		rewrites:       after.Rewrites - before.Rewrites,
		alreadyWrapped: after.AlreadyWrapped - before.AlreadyWrapped,
		passthrough:    after.Passthrough - before.Passthrough,
	}
}

func assertOnce(t *testing.T, delta rtkDelta, field string) {
	t.Helper()
	sum := delta.rewrites + delta.alreadyWrapped + delta.passthrough
	if sum != 1 {
		t.Fatalf("expected exactly one counter, got %+v", delta)
	}
	var got int64
	switch field {
	case "rewrites":
		got = delta.rewrites
	case "alreadyWrapped":
		got = delta.alreadyWrapped
	case "passthrough":
		got = delta.passthrough
	default:
		t.Fatalf("unknown field %s", field)
	}
	if got != 1 {
		t.Fatalf("expected %s=1, got %+v", field, delta)
	}
}

// TestRewriteCommandWithRtk ports the fake-rtk assertions in test.mjs.
func TestRewriteCommandWithRtk(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	if goruntime.GOOS == "windows" {
		t.Skip("fake rtk script is a POSIX shell script")
	}
	ctx := context.Background()
	ResetStats(ctx)
	ResetRTKAvailabilityCache()

	dir := t.TempDir()
	fakeRtk := filepath.Join(dir, "rtk")
	if err := os.WriteFile(fakeRtk, []byte(fakeRtkScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetRTKAvailabilityCache()

	before := GetRtkStats(ctx)
	wrapped := RewriteCommandWithRtk(ctx, "rtk echo doublewrap", false)
	if wrapped != "rtk echo doublewrap" || strings.Contains(wrapped, "rtk rtk") {
		t.Fatalf("double-wrap stripped once, got %q", wrapped)
	}
	assertOnce(t, deltaRtk(before, GetRtkStats(ctx)), "alreadyWrapped")

	before = GetRtkStats(ctx)
	rewritten := RewriteCommandWithRtk(ctx, "echo doublewrap", false)
	if rewritten != "rtk echo doublewrap" {
		t.Fatalf("plain command rewritten once, got %q", rewritten)
	}
	assertOnce(t, deltaRtk(before, GetRtkStats(ctx)), "rewrites")

	t.Setenv("FAKE_RTK_IDENTICAL", "1")
	before = GetRtkStats(ctx)
	identical := RewriteCommandWithRtk(ctx, "echo same", false)
	if identical != "echo same" {
		t.Fatalf("identical stdout, got %q", identical)
	}
	assertOnce(t, deltaRtk(before, GetRtkStats(ctx)), "alreadyWrapped")
	_ = os.Unsetenv("FAKE_RTK_IDENTICAL")

	t.Setenv("FAKE_RTK_EMPTY", "1")
	before = GetRtkStats(ctx)
	empty := RewriteCommandWithRtk(ctx, "weirdcmd --flag", false)
	if empty != "weirdcmd --flag" {
		t.Fatalf("empty stdout returns original, got %q", empty)
	}
	assertOnce(t, deltaRtk(before, GetRtkStats(ctx)), "passthrough")
	found := false
	for _, entry := range GetRtkTopPassthrough(ctx) {
		if entry.Prefix == "weirdcmd --flag" {
			found = true
		}
	}
	if !found {
		t.Fatal("passthrough prefix not tracked")
	}
	_ = os.Unsetenv("FAKE_RTK_EMPTY")

	t.Setenv("FAKE_RTK_SLEEP_MS", "3500")
	before = GetRtkStats(ctx)
	timed := RewriteCommandWithRtk(ctx, "sleepcmd now", false)
	if timed != "sleepcmd now" {
		t.Fatalf("timeout fallback returns original, got %q", timed)
	}
	assertOnce(t, deltaRtk(before, GetRtkStats(ctx)), "passthrough")
	_ = os.Unsetenv("FAKE_RTK_SLEEP_MS")
}

// fakeRtkSelfKillScript prints partial output, then kills itself with SIGKILL.
const fakeRtkSelfKillScript = `#!/bin/sh
if [ "$1" = "--version" ]; then printf 'rtk-fake 0\n'; exit 0; fi
if [ "$1" = "rewrite" ]; then printf 'rtk partial'; kill -9 $$; fi
exit 1
`

// TestRewriteCommandWithRtkSignalKill mirrors TS: a non-timeout signal kill is
// not a timeout, so the partial stdout is kept and counted as a rewrite.
func TestRewriteCommandWithRtkSignalKill(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX signal semantics")
	}
	ctx := context.Background()
	ResetStats(ctx)
	ResetRTKAvailabilityCache()

	dir := t.TempDir()
	fakeRtk := filepath.Join(dir, "rtk")
	if err := os.WriteFile(fakeRtk, []byte(fakeRtkSelfKillScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetRTKAvailabilityCache()

	before := GetRtkStats(ctx)
	got := RewriteCommandWithRtk(ctx, "echo hi", false)
	if got != "rtk partial" {
		t.Fatalf("signal-kill partial stdout = %q, want %q", got, "rtk partial")
	}
	assertOnce(t, deltaRtk(before, GetRtkStats(ctx)), "rewrites")
}

// TestCompactJSON ports test.mjs "2b. Testing JSON compaction".
func TestCompactJSON(t *testing.T) {
	prettyJSON := "{\n  \"name\": \"cave-tools\",\n  \"version\": 3,\n  \"tags\": [\"a\", \"b\"]\n}"
	compacted := CompactJSON(prettyJSON)
	if compacted == nil {
		t.Fatal("pretty JSON should compact")
	}
	if strings.Contains(*compacted, "\n") {
		t.Fatal("compacted JSON must not contain newlines")
	}
	var valueA, valueB any
	if err := jsonUnmarshal(*compacted, &valueA); err != nil {
		t.Fatal(err)
	}
	if err := jsonUnmarshal(prettyJSON, &valueB); err != nil {
		t.Fatal(err)
	}
	if marshalJSONCompact(valueA) != marshalJSONCompact(valueB) {
		t.Fatal("compacted JSON value changed")
	}

	jsonl := "{ \"a\": 1 }\n{ \"b\": 2 }\n\n{ \"c\": 3 }"
	compactedJSONL := CompactJSONL(jsonl)
	if compactedJSONL == nil || *compactedJSONL != "{\"a\":1}\n{\"b\":2}\n{\"c\":3}" {
		t.Fatalf("JSONL compacted = %v", compactedJSONL)
	}

	if CompactJSON(`{"a":1,"b":[2,3]}`) != nil {
		t.Fatal("minified JSON should be a no-op")
	}

	stringWithSpaces := "{\n  \"msg\": \"hello   world\\n\\ttab\"\n}"
	compactedString := CompactJSON(stringWithSpaces)
	if compactedString == nil || !strings.Contains(*compactedString, "hello   world") {
		t.Fatal("inner spaces must be preserved")
	}
	if !strings.Contains(*compactedString, `\n\ttab`) {
		t.Fatal("escapes must be preserved")
	}

	if got := ExtractStructuredData("not json", "ls"); got != "not json" {
		t.Fatalf("non-JSON passthrough = %q", got)
	}
}

func jsonUnmarshal(text string, target any) error {
	return json.Unmarshal([]byte(text), target)
}

// TestTruncateJSNumberSemantics pins TS's fractional number handling: line
// counts and the omitted marker stay fractional, and numbers are coerced only
// inside Array.prototype.slice.
func TestTruncateJSNumberSemantics(t *testing.T) {
	text := "a\nb\nc\nd\ne\nf"
	if got, want := TruncateLines(text, 4, 1.5, 1.25), "a\n\n... (3.25 lines truncated) ...\n\nf"; got != want {
		t.Errorf("fractional = %q, want %q", got, want)
	}
	if got := TruncateLines(text, math.Inf(1), 1, 1); got != text {
		t.Errorf("Infinity maxLines = %q, want unchanged", got)
	}
	if got, want := TruncateLines(text, math.NaN(), 1, 1), "a\n\n... (4 lines truncated) ...\n\nf"; got != want {
		t.Errorf("NaN maxLines = %q, want %q", got, want)
	}
	if got, want := TruncateLines(text, 3, -2, -1), "a\nb\nc\nd\n\n... (9 lines truncated) ...\n\nb\nc\nd\ne\nf"; got != want {
		t.Errorf("negative bounds = %q, want %q", got, want)
	}
}

// TestTruncateCharsJSNumberSemantics covers the char cap's JS arithmetic,
// including Infinity and a negative cap.
func TestTruncateCharsJSNumberSemantics(t *testing.T) {
	if got := TruncateChars("short", math.Inf(1), 1, 1); got != "short" {
		t.Errorf("Infinity cap = %q, want unchanged", got)
	}
	if got, want := TruncateChars("a\nb", -5, 0, 0), "... (2 chars truncated) ...\nb"; got != want {
		t.Errorf("negative cap = %q, want %q", got, want)
	}
}

// TestBudgetEntriesJSEntryOrder covers Object.entries(budgets): integer-like
// names enumerate first ascending, then the rest in insertion order.
func TestBudgetEntriesJSEntryOrder(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ctx := context.Background()

	before := make([]string, 0)
	for _, entry := range GetAllBudgetEntries() {
		before = append(before, entry.Name)
	}
	for _, name := range []string{"2", "10", "1", "zzz", "aaa"} {
		SetBudget(ctx, name, 3, 1, 1, numberPtr(1.5))
	}
	entries := GetAllBudgetEntries()
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	numeric := []string{"1", "2", "10"}
	rest := make([]string, 0, len(before)+2)
	for _, name := range before {
		if isJSArrayIndex(name) {
			numeric = append(numeric, name)
		} else {
			rest = append(rest, name)
		}
	}
	rest = append(rest, "zzz", "aaa")
	sort.Slice(numeric, func(i, j int) bool {
		return jsArrayIndexValue(numeric[i]) < jsArrayIndexValue(numeric[j])
	})
	want := append(numeric, rest...)
	if !slices.Equal(names, want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	if got := GetBudget("1").MaxChars; got == nil || *got != 1.5 {
		t.Fatalf("fractional maxChars lost: %v", got)
	}

	for key, want := range map[string]bool{
		"0": true, "1": true, "4294967294": true, "4294967295": false,
		"00": false, "01": false, "-1": false, "1.0": false, "": false,
	} {
		if got := isJSArrayIndex(key); got != want {
			t.Errorf("isJSArrayIndex(%q) = %v, want %v", key, got, want)
		}
	}
}

// TestResetStatsKeepsBudgetsClearsBounceOrder matches TS: resetStats clears
// counters and the per-extension Map but not the budgets object.
func TestResetStatsKeepsBudgetsClearsBounceOrder(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ctx := context.Background()
	SetBudget(ctx, "reset-probe", 7.5, 2, 2, numberPtr(9.5))
	RecordRead(ctx, "x.zzz", true, 10)
	RecordRead(ctx, "x.zzz", false, 20)
	RecordRead(ctx, "x.aaa", true, 10)
	RecordRead(ctx, "x.aaa", false, 20)
	if got := GetBounceStatsView(ctx); !slices.Equal(got.Order, []string{".zzz", ".aaa"}) {
		t.Fatalf("bounce order = %v", got.Order)
	}

	ResetStats(ctx)
	if got := GetBounceStatsView(ctx); len(got.Order) != 0 || len(got.Summary.ByExtension) != 0 {
		t.Fatalf("bounce state after reset = %+v", got)
	}
	if got := GetBudget("reset-probe"); got.MaxLines != 7.5 || got.MaxChars == nil || *got.MaxChars != 9.5 {
		t.Fatalf("resetStats must not clear budgets: %+v", got)
	}
}

// TestBudgetDefaultsAndCharCaps ports test.mjs char-cap budget assertions.
func TestBudgetDefaultsAndCharCaps(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	ctx := context.Background()
	defaults := GetAllBudgets()
	for name, want := range map[string]float64{"bash": 50, "grep": 60, "web": 120, "read": 200, "ls": 60, "find": 120} {
		if defaults[name].MaxLines != want {
			t.Fatalf("%s.maxLines = %v, want %v", name, defaults[name].MaxLines, want)
		}
	}
	if defaults["read"].MaxChars != nil {
		t.Fatal("read must not have a char cap")
	}
	if defaults["bash"].MaxChars == nil || *defaults["bash"].MaxChars != 6000 {
		t.Fatal("bash maxChars must be 6000")
	}
	if defaults["grep"].MaxChars == nil || *defaults["grep"].MaxChars != 6000 {
		t.Fatal("grep maxChars must be 6000")
	}
	if defaults["web"].MaxChars == nil || *defaults["web"].MaxChars != 8000 {
		t.Fatal("web maxChars must be 8000")
	}
	if got := GetBudget("no-such-tool"); got.MaxLines != 100 || got.HeadLines != 50 || got.TailLines != 50 || got.MaxChars != nil {
		t.Fatalf("default budget = %+v", got)
	}

	// Restore mutated budgets after this test.
	t.Cleanup(func() {
		SetBudget(ctx, "bash", 50, 15, 35, numberPtr(6000))
		SetBudget(ctx, "grep", 60, 30, 30, numberPtr(6000))
		SetBudget(ctx, "web", 120, 60, 60, numberPtr(8000))
	})

	// Long lines under the line budget: char cap trims at line boundaries.
	longLines := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		longLines = append(longLines, "L"+pad2(i)+" "+strings.Repeat("x", 396))
	}
	capped := ApplyBudget(ctx, strings.Join(longLines, "\n"), "bash")
	if len(capped) > 6000 {
		t.Fatalf("capped length %d > 6000", len(capped))
	}
	markerRE := regexp.MustCompile(`(?m)^\.\.\. \((\d+) chars truncated\) \.\.\.$`)
	marker := markerRE.FindStringSubmatchIndex(capped)
	if marker == nil {
		t.Fatal("char-cap marker missing")
	}
	markerLine := capped[marker[0]:marker[1]]
	markerValue := markerRE.FindStringSubmatch(markerLine)
	kept := make([]string, 0, len(longLines))
	for _, line := range strings.Split(capped, "\n") {
		if strings.HasPrefix(line, "...") {
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	wantSet := map[string]struct{}{}
	for _, line := range longLines {
		wantSet[line] = struct{}{}
	}
	for _, line := range kept {
		if _, ok := wantSet[line]; !ok {
			t.Fatalf("partial line kept: %q", line)
		}
	}
	headKept := len(keptLinesBefore(capped, marker[0]))
	tailKept := len(keptLinesBefore(capped[marker[1]:], -1))
	if headKept < 3 || headKept > 5 {
		t.Fatalf("head kept %d, want 3..5", headKept)
	}
	if tailKept < 9 || tailKept > 11 {
		t.Fatalf("tail kept %d, want 9..11", tailKept)
	}
	if kept[0] != longLines[0] || kept[len(kept)-1] != longLines[len(longLines)-1] {
		t.Fatal("first/last lines must be kept")
	}
	wantDropped := len(strings.Join(longLines, "\n")) - (len(strings.Join(kept, "\n")) - 1)
	if markerValue[1] != itoa(wantDropped) {
		t.Fatalf("marker dropped = %s, want %d", markerValue[1], wantDropped)
	}

	// A single giant line is cut inside the line, head + tail kept.
	giant := strings.Repeat("A", 1000) + strings.Repeat("B", 20000) + strings.Repeat("C", 1000)
	giantCapped := ApplyBudget(ctx, giant, "bash")
	if len(giantCapped) > 6000 {
		t.Fatalf("giant capped length %d > 6000", len(giantCapped))
	}
	giantParts := regexp.MustCompile(`^(A+B*)\n\.\.\. \((\d+) chars truncated\) \.\.\.\n(B*C+)$`).FindStringSubmatch(giantCapped)
	if giantParts == nil {
		t.Fatalf("giant line not cut into head + marker + tail: %q", giantCapped)
	}
	if len(giantParts[1]) >= len(giantParts[3]) {
		t.Fatal("tail share must be larger than head share")
	}
	if len(giantParts[1])+len(giantParts[3])+atoi(giantParts[2]) != len(giant) {
		t.Fatal("giant parts do not add up")
	}

	// A line over the head share but under the cap is never cut inside.
	midLines := []string{strings.Repeat("h", 3000)}
	for i := 0; i < 10; i++ {
		midLines = append(midLines, "t"+pad2(i)+" "+strings.Repeat("y", 600))
	}
	mid := strings.Join(midLines, "\n")
	if strings.Contains(TruncateChars(mid, 6000, 15, 35), strings.Repeat("h", 10)) {
		t.Fatal("partial line cut")
	}

	// Web budget is separate from bash.
	webText := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		webText = append(webText, "web row "+itoa(i))
	}
	webJoined := strings.Join(webText, "\n")
	if got := ApplyBudget(ctx, webJoined, "web"); got != webJoined {
		t.Fatal("100 lines should fit the web budget")
	}
	if !strings.Contains(ApplyBudget(ctx, webJoined, "bash"), "lines truncated") {
		t.Fatal("100 lines must exceed the bash budget")
	}

	// setBudget: max_chars set, 0 disables.
	SetBudget(ctx, "web", 120, 60, 60, numberPtr(100))
	if budget := GetBudget("web"); budget.MaxChars == nil || *budget.MaxChars != 100 {
		t.Fatalf("web.maxChars = %v, want 100", budget.MaxChars)
	}
	SetBudget(ctx, "web", 120, 60, 60, numberPtr(0))
	if budget := GetBudget("web"); budget.MaxChars != nil {
		t.Fatal("max_chars 0 must disable the cap")
	}
	SetBudget(ctx, "web", 120, 60, 60, numberPtr(8000))
	if budget := GetBudget("web"); budget.MaxChars == nil || *budget.MaxChars != 8000 {
		t.Fatal("web maxChars not restored")
	}

	// Tightening grep truncates.
	SetBudget(ctx, "grep", 5, 2, 2, nil)
	if got := ApplyBudget(ctx, "a\nb\nc\nd\ne\nf\ng\n", "grep"); !strings.Contains(got, "lines truncated") {
		t.Fatalf("tight grep budget did not truncate: %q", got)
	}
}

func keptLinesBefore(text string, end int) []string {
	part := text
	if end >= 0 {
		part = text[:end]
	}
	lines := strings.Split(part, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	digits := make([]byte, 0, 8)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// TestLineCompaction ports the collapse/truncate assertions from test.mjs.
func TestLineCompaction(t *testing.T) {
	t.Cleanup(waitPendingPersists)
	spamLines := make([]string, 0, 31)
	for i := 1; i <= 30; i++ {
		spamLines = append(spamLines, "2026-08-21T10:00:00Z INFO GET /api/users 200 12ms req="+itoa(i)+" user=42")
	}
	spamLines = append(spamLines, "2026-08-21T10:00:01Z ERROR upstream refused the connection")
	spam := strings.Join(spamLines, "\n")
	spamCollapsed := CollapseRepeatedLines(spam)
	if !strings.Contains(spamCollapsed, "similar lines omitted") {
		t.Fatal("spam run must collapse")
	}
	if !strings.Contains(spamCollapsed, "req=1") {
		t.Fatal("first occurrence must be kept verbatim")
	}
	if !strings.Contains(spamCollapsed, "upstream refused") {
		t.Fatal("distinct line must be kept")
	}
	if len(spamCollapsed) >= len(spam) {
		t.Fatal("collapsed output must be smaller")
	}
	if CollapseRepeatedLines(spamCollapsed) != spamCollapsed {
		t.Fatal("collapse must be idempotent")
	}
	if got := CollapseRepeatedLines("short\nunique\nlines\n"); got != "short\nunique\nlines\n" {
		t.Fatalf("no-run input touched: %q", got)
	}
	numeric := make([]string, 0, 50)
	for i := 1; i <= 50; i++ {
		numeric = append(numeric, itoa(i))
	}
	numericText := strings.Join(numeric, "\n")
	if CollapseRepeatedLines(numericText) != numericText {
		t.Fatal("pure-numeric runs must stay intact")
	}

	if got := CollapseBlankLines("a\n\n\n\nb"); got != "a\n\nb" {
		t.Fatalf("collapseBlankLines LF = %q", got)
	}
	if got := CollapseBlankLines("a\r\n\r\n\r\nb"); got != "a\n\nb" {
		t.Fatalf("collapseBlankLines CRLF = %q", got)
	}
	if got := StripAnsi("\x1b[31mred\x1b[0m plain"); got != "red plain" {
		t.Fatalf("stripAnsi = %q", got)
	}

	if got := TruncateLines("a\nb\nc\nd\ne\nf", 4, 2, 1); got != "a\nb\n\n... (3 lines truncated) ...\n\nf" {
		t.Fatalf("truncateLines = %q", got)
	}

	if got := ReductionPercent(100, 25); got != 25 {
		t.Fatalf("reductionPercent = %v, want 25", got)
	}
	if got := ReductionPercent(0, 0); got != 0 {
		t.Fatalf("reductionPercent empty = %v, want 0", got)
	}
	if got := EfficiencyMeter(50); got != strings.Repeat("█", 12)+strings.Repeat("░", 12) {
		t.Fatalf("efficiencyMeter = %q", got)
	}

	if !FitsBudget("hello\nworld", "read") {
		t.Fatal("small text must fit read")
	}
	if FitsBudget(strings.Repeat("x", 60000), "read") {
		t.Fatal("oversized text must not fit read")
	}
}
