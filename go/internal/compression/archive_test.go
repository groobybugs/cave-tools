package compression

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// TestMain isolates the storage singleton in a temp data dir and clears any
// subagent signal the developer's shell would otherwise inject into the
// cache/read-freshness tests.
func TestMain(m *testing.M) {
	testenv.ScrubProcess()
	dir, err := os.MkdirTemp("", "cave-tools-go-archive-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	_ = os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestArchiveIDIsSHA256(t *testing.T) {
	id := archiveID("hello")
	sum := sha256.Sum256([]byte("hello"))
	if want := hex.EncodeToString(sum[:]); id != want {
		t.Fatalf("archiveID = %q, want %q", id, want)
	}
	if entryDir(id) != filepath.Join(storage.ArchiveDir(), id[:2]) {
		t.Fatalf("entryDir = %q", entryDir(id))
	}
	if metaPath(id) != filepath.Join(entryDir(id), id+".meta.json") {
		t.Fatalf("metaPath = %q", metaPath(id))
	}
	wantPath, err := filepath.Abs(filepath.Join(entryDir(id), id+".txt"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if contentPath(id) != wantPath {
		t.Fatalf("contentPath = %q, want %q", contentPath(id), wantPath)
	}
}

func TestStoreArchive(t *testing.T) {
	CleanupArchives(-1)
	output := "line\nline\nline\n"
	command := "seq 3"
	id, path, err := StoreArchive(output, command)
	if err != nil {
		t.Fatalf("StoreArchive: %v", err)
	}
	if want := archiveID(output); id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}
	if path != contentPath(id) {
		t.Fatalf("path = %q, want %q", path, contentPath(id))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if string(data) != output {
		t.Fatalf("content = %q, want %q", data, output)
	}
	for _, dir := range []string{storage.ArchiveDir(), entryDir(id)} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", dir, perm)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat content: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("content mode = %04o, want 0600", perm)
	}
	entries := ListArchives()
	if len(entries) != 1 {
		t.Fatalf("ListArchives = %+v, want one entry", entries)
	}
	if entries[0].ID != id || entries[0].Command != command || entries[0].SizeChars != int64(len(output)) {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].ContentPath != path {
		t.Fatalf("contentPath = %q, want %q", entries[0].ContentPath, path)
	}
}

// Mirrors test.mjs archive tests: small output is not archived, big output is,
// and the archive round-trips.
func TestArchiveIfLarge(t *testing.T) {
	CleanupArchives(-1)
	small := "small"
	got, err := ArchiveIfLarge(small, "echo small")
	if err != nil {
		t.Fatalf("ArchiveIfLarge(small): %v", err)
	}
	if got != nil {
		t.Fatalf("small output archived: %+v", got)
	}

	big := strings.Repeat("line\n", 10_000)
	archived, err := ArchiveIfLarge(big, "seq 10000")
	if err != nil {
		t.Fatalf("ArchiveIfLarge(big): %v", err)
	}
	if archived == nil {
		t.Fatal("big output not archived")
	}
	if !strings.Contains(archived.Summary, "lines archived") {
		t.Fatalf("summary = %q, want 'lines archived'", archived.Summary)
	}
	if archived.ID != archiveID(big) {
		t.Fatalf("id = %q, want %q", archived.ID, archiveID(big))
	}
	expanded, ok := ExpandArchive(archived.ID)
	if !ok || expanded != big {
		t.Fatalf("archive round-trip failed (ok=%v, len=%d)", ok, len(expanded))
	}
	CleanupArchives(-1)
	if _, ok := ExpandArchive(archived.ID); ok {
		t.Fatal("cleanup did not remove archive")
	}
	if stats := GetArchiveStats(); stats.Count != 0 || stats.TotalChars != 0 {
		t.Fatalf("stats after cleanup = %+v, want zeros", stats)
	}
}

func TestArchiveThresholdBoundary(t *testing.T) {
	below := strings.Repeat("x", archiveThreshold-1)
	if got, err := ArchiveIfLarge(below, "cmd"); err != nil || got != nil {
		t.Fatalf("below threshold = %+v, %v; want nil", got, err)
	}
	at := strings.Repeat("x", archiveThreshold)
	got, err := ArchiveIfLarge(at, "cmd")
	if err != nil || got == nil {
		t.Fatalf("at threshold = %+v, %v; want archived", got, err)
	}
	// One huge line: a line-based head/tail would keep everything, so the
	// summary cuts by chars instead of printing a negative line count.
	want := strings.Repeat("x", 1000) + "\n\n... (48500 chars archived) ...\n\n" + strings.Repeat("x", 500)
	if got.Summary != want {
		t.Fatalf("summary = %q, want %q", got.Summary, want)
	}
	if regexp.MustCompile(`-\d+ lines archived`).MatchString(got.Summary) {
		t.Fatalf("summary = %q, want no negative line count", got.Summary)
	}
}

// TestArchiveFewHugeLinesUTF16 pins the char cut to String.prototype.slice
// semantics: UTF-16 code units, where cutting a surrogate pair leaves a lone
// surrogate that Node serialises as U+FFFD.
func TestArchiveFewHugeLinesUTF16(t *testing.T) {
	// 1 + 30000 astral chars = 60001 UTF-16 units on one line.
	output := "a" + strings.Repeat("\U0001F600", 30000)
	got, err := ArchiveIfLarge(output, "emoji")
	if err != nil || got == nil {
		t.Fatalf("ArchiveIfLarge = %+v, %v; want archived", got, err)
	}
	wantHead := "a" + strings.Repeat("\U0001F600", 499) + "\uFFFD"
	wantTail := strings.Repeat("\U0001F600", 250)
	want := wantHead + "\n\n... (58501 chars archived) ...\n\n" + wantTail
	if got.Summary != want {
		t.Fatalf("summary = %q, want %q", got.Summary, want)
	}
}

// TestArchiveManyLinesKeepsLineSummary covers the unchanged line-based path.
func TestArchiveManyLinesKeepsLineSummary(t *testing.T) {
	lines := make([]string, 10000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	got, err := ArchiveIfLarge(strings.Join(lines, "\n"), "many")
	if err != nil || got == nil {
		t.Fatalf("ArchiveIfLarge = %+v, %v; want archived", got, err)
	}
	if !strings.Contains(got.Summary, "(9970 lines archived)") {
		t.Fatalf("summary = %q, want (9970 lines archived)", got.Summary)
	}
}

func TestArchiveHandle(t *testing.T) {
	full := strings.Repeat("a", 1000)
	shown := strings.Repeat("a", 10)
	handle := ArchiveHandle(full, shown, "echo big")
	pattern := regexp.MustCompile(`^\n\n\[cave: full output \(1 lines\) at (.+) — cave__read offset/limit or cave__grep\]$`)
	match := pattern.FindStringSubmatch(handle)
	if match == nil {
		t.Fatalf("handle = %q", handle)
	}
	archived, ok := ExpandArchive(archiveID(full))
	if !ok || archived != full {
		t.Fatalf("handle path does not point at archived content")
	}
	if match[1] != contentPath(archiveID(full)) {
		t.Fatalf("handle path = %q, want %q", match[1], contentPath(archiveID(full)))
	}

	// Exactly HANDLE_MIN_DROPPED_CHARS dropped: no handle.
	exact := strings.Repeat("a", 300)
	if got := ArchiveHandle(exact, strings.Repeat("a", 100), "cmd"); got != "" {
		t.Fatalf("handle at exact threshold = %q, want empty", got)
	}
	over := strings.Repeat("a", 301)
	if got := ArchiveHandle(over, strings.Repeat("a", 100), "cmd"); got == "" {
		t.Fatal("handle above threshold is empty")
	}
}

func TestArchiveUsesUTF16Lengths(t *testing.T) {
	CleanupArchives(-1)
	content := "€€" // 2 UTF-16 code units, 6 UTF-8 bytes
	id, _, err := StoreArchive(content, "echo euro")
	if err != nil {
		t.Fatalf("StoreArchive: %v", err)
	}
	entries := ListArchives()
	found := false
	for _, entry := range entries {
		if entry.ID == id {
			found = true
			if entry.SizeChars != 2 {
				t.Fatalf("sizeChars = %d, want 2 (JS string.length)", entry.SizeChars)
			}
		}
	}
	if !found {
		t.Fatalf("archive %s not listed", id)
	}
	// 49999 euro signs are 49999 JS chars: below threshold despite 149997 bytes.
	if got, err := ArchiveIfLarge(strings.Repeat("€", archiveThreshold-1), "cmd"); err != nil || got != nil {
		t.Fatalf("utf16 threshold = %+v, %v; want nil", got, err)
	}
}

func TestExpandArchiveMissing(t *testing.T) {
	if _, ok := ExpandArchive(archiveID("never stored")); ok {
		t.Fatal("ExpandArchive(missing) returned content")
	}
}

func TestCleanupArchivesKeepsFresh(t *testing.T) {
	CleanupArchives(-1)
	id, _, err := StoreArchive("fresh output", "cmd")
	if err != nil {
		t.Fatalf("StoreArchive: %v", err)
	}
	if removed := CleanupArchives(); removed != 0 {
		t.Fatalf("CleanupArchives() removed %d fresh archives", removed)
	}
	if _, ok := ExpandArchive(id); !ok {
		t.Fatal("fresh archive removed")
	}
	if removed := CleanupArchives(-1); removed < 1 {
		t.Fatalf("CleanupArchives(-1) removed %d, want at least 1", removed)
	}
	if _, ok := ExpandArchive(id); ok {
		t.Fatal("archive survived CleanupArchives(-1)")
	}
}

func TestListArchivesEmpty(t *testing.T) {
	CleanupArchives(-1)
	if got := ListArchives(); len(got) != 0 {
		t.Fatalf("ListArchives = %+v, want empty", got)
	}
}
