// Archive store: content-addressed output files plus their archive_meta rows.
//
// Port of src/compression/archive.ts. The archive store shares its SQLite
// file, data dir and archive layout with the TypeScript implementation.
package compression

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/storage"
)

const (
	// port of src/compression/archive.ts:ARCHIVE_THRESHOLD
	archiveThreshold = 50_000
	// port of src/compression/archive.ts:ARCHIVE_MAX_AGE_MS
	archiveMaxAgeMS = 24 * 60 * 60 * 1000
)

// HANDLE_MIN_DROPPED_CHARS is the minimum drop that justifies archiving: only
// archive dropped output when the drop outweighs the handle line itself.
// port of src/compression/archive.ts:HANDLE_MIN_DROPPED_CHARS
const HANDLE_MIN_DROPPED_CHARS = 200

// ArchiveEntry is one archive_meta row.
// port of src/compression/archive.ts:ArchiveEntry
type ArchiveEntry = storage.ArchiveEntry

// port of src/compression/archive.ts:archiveId
func archiveID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// port of src/compression/archive.ts:entryDir
func entryDir(id string) string {
	prefix := id
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.Join(storage.ArchiveDir(), prefix)
}

// port of src/compression/archive.ts:contentPath
func contentPath(id string) string {
	absolute, err := filepath.Abs(filepath.Join(entryDir(id), id+".txt"))
	if err != nil {
		return filepath.Join(entryDir(id), id+".txt")
	}
	return absolute
}

// port of src/compression/archive.ts:metaPath
func metaPath(id string) string {
	return filepath.Join(entryDir(id), id+".meta.json")
}

// StoreArchive writes output to the content-addressed archive store and
// records it. Retention is CleanupArchives().
// port of src/compression/archive.ts:storeArchive
func StoreArchive(output, command string) (id string, path string, err error) {
	id = archiveID(output)
	archivedContentPath := contentPath(id)
	// Archives can hold private file contents (grep) or command output: keep
	// them owner-only regardless of umask.
	if err := os.MkdirAll(entryDir(id), 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(storage.ArchiveDir(), 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(entryDir(id), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(archivedContentPath, []byte(output), 0o600); err != nil {
		return "", "", err
	}
	if err := os.Chmod(archivedContentPath, 0o600); err != nil {
		return "", "", err
	}
	if err := storage.RecordArchive(storage.ArchiveEntry{
		ID:          id,
		Command:     command,
		SizeChars:   int64(utf16Length(output)),
		CreatedAt:   time.Now().UnixMilli(),
		ContentPath: archivedContentPath,
	}); err != nil {
		return "", "", err
	}
	return id, archivedContentPath, nil
}

// ArchiveHandle archives full when shown dropped more than
// HANDLE_MIN_DROPPED_CHARS of it and returns a one-line handle (with leading
// blank line) pointing at the archive; otherwise "". Callers pass full already
// redacted when redaction is on, so secrets never reach disk. Archive failures
// never fail the tool call.
// port of src/compression/archive.ts:archiveHandle
func ArchiveHandle(full, shown, command string) string {
	if utf16Length(full)-utf16Length(shown) <= HANDLE_MIN_DROPPED_CHARS {
		return ""
	}
	_, path, err := StoreArchive(full, command)
	if err != nil {
		return ""
	}
	lines := strings.Count(full, "\n") + 1
	return "\n\n[cave: full output (" + strconv.Itoa(lines) + " lines) at " + path +
		" — cave__read offset/limit or cave__grep]"
}

// ArchiveSummary is archiveIfLarge's result object.
// port of src/compression/archive.ts:archiveIfLarge (return object)
type ArchiveSummary struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// ArchiveIfLarge archives output at or above archiveThreshold chars and returns
// a head/tail summary; nil when the output is small.
// port of src/compression/archive.ts:archiveIfLarge
func ArchiveIfLarge(output, command string) (*ArchiveSummary, error) {
	if utf16Length(output) < archiveThreshold {
		return nil, nil
	}

	id, _, err := StoreArchive(output, command)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(output, "\n")
	head := strings.Join(lines[:minInt(len(lines), 20)], "\n")
	tailStart := len(lines) - 10
	if tailStart < 0 {
		tailStart = 0
	}
	tail := strings.Join(lines[tailStart:], "\n")
	summary := head + "\n\n... (" + strconv.Itoa(len(lines)-30) + " lines archived) ...\n\n" + tail

	return &ArchiveSummary{ID: id, Summary: summary}, nil
}

// ExpandArchive returns an archive's content, or ok=false when it is missing.
// port of src/compression/archive.ts:expandArchive
func ExpandArchive(id string) (content string, ok bool) {
	data, err := os.ReadFile(contentPath(id))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// ListArchives returns archive rows newest first; a storage failure yields an
// empty list like TS's try/catch.
// port of src/compression/archive.ts:listArchives
func ListArchives() []ArchiveEntry {
	archives, err := storage.ListArchives()
	if err != nil {
		return []ArchiveEntry{}
	}
	return archives
}

// CleanupArchives removes archives older than maxAgeMS (default 24h) and
// returns how many rows it removed.
// port of src/compression/archive.ts:cleanupArchives
func CleanupArchives(maxAgeMS ...int64) int {
	maxAge := int64(archiveMaxAgeMS)
	if len(maxAgeMS) > 0 {
		maxAge = maxAgeMS[0]
	}
	cutoff := time.Now().UnixMilli() - maxAge
	removed := 0
	for _, archive := range ListArchives() {
		if archive.CreatedAt >= cutoff {
			continue
		}
		path := archive.ContentPath
		if path == "" {
			path = contentPath(archive.ID)
		}
		if err := removeForce(path); err != nil {
			_ = storage.DeleteArchive(archive.ID)
			continue
		}
		if err := removeForce(metaPath(archive.ID)); err != nil {
			_ = storage.DeleteArchive(archive.ID)
			continue
		}
		if err := storage.DeleteArchive(archive.ID); err != nil {
			continue
		}
		removed++
	}
	return removed
}

// removeForce mirrors fs.rm(path, { force: true }) for our uses: a missing file
// is success, any other error is returned.
func removeForce(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// GetArchiveStats returns the archive_meta aggregate, or zeros when the db is
// unavailable.
// port of src/compression/archive.ts:getArchiveStats
func GetArchiveStats() storage.ArchiveTotals {
	totals, err := storage.ArchiveStats()
	if err != nil {
		return storage.ArchiveTotals{}
	}
	return totals
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
