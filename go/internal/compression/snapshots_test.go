package compression

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// TestSnapshotLRU ports the snapshot assertions from test.mjs (the re-read
// diff block).
func TestSnapshotLRU(t *testing.T) {
	if MaxSnapshotFileBytes != 256*1024 {
		t.Fatalf("MaxSnapshotFileBytes = %d, want %d", MaxSnapshotFileBytes, 256*1024)
	}
	if MaxSnapshotTotalBytes != 8*1024*1024 {
		t.Fatalf("MaxSnapshotTotalBytes = %d, want %d", MaxSnapshotTotalBytes, 8*1024*1024)
	}

	ResetSnapshots()
	t.Cleanup(ResetSnapshots)

	chunk := strings.Repeat("y", 200*1024)
	count := int(math.Ceil(float64(MaxSnapshotTotalBytes)/float64(len(chunk)))) + 5
	for i := 0; i < count; i++ {
		RememberSnapshot("cap", "/f"+strconv.Itoa(i), "h"+strconv.Itoa(i), chunk)
	}

	files, bytes := GetSnapshotStats()
	if bytes > MaxSnapshotTotalBytes {
		t.Errorf("snapshot bytes %d exceed cap %d", bytes, MaxSnapshotTotalBytes)
	}
	if files >= count {
		t.Errorf("snapshot files %d, want < %d", files, count)
	}
	// Byte-exact TS state for the same fixture.
	if files != 40 || bytes != 8_192_000 {
		t.Errorf("GetSnapshotStats() = (%d, %d), want (40, 8192000)", files, bytes)
	}
	if got := GetSnapshot("cap", "/f0"); got != nil {
		t.Errorf("oldest snapshot was not evicted: %+v", got)
	}
	last := GetSnapshot("cap", "/f"+strconv.Itoa(count-1))
	if last == nil {
		t.Fatal("newest snapshot was evicted")
	}
	if last.Hash != "h45" || last.Content != chunk || last.Bytes != 204800 {
		t.Errorf("newest snapshot = %+v", last)
	}

	RememberSnapshot("cap", "/huge", "h", strings.Repeat("z", MaxSnapshotFileBytes+1))
	if got := GetSnapshot("cap", "/huge"); got != nil {
		t.Errorf("per-file cap not enforced: %+v", got)
	}

	ResetSnapshots()
	if files, bytes := GetSnapshotStats(); files != 0 || bytes != 0 {
		t.Errorf("after reset GetSnapshotStats() = (%d, %d), want (0, 0)", files, bytes)
	}

	RememberSnapshot("s", "/p", "H", "hello")
	got := GetSnapshot("s", "/p")
	if got == nil || got.Hash != "H" || got.Content != "hello" || got.Bytes != 5 {
		t.Errorf("GetSnapshot() = %+v", got)
	}
	if missing := GetSnapshot("s", "/nope"); missing != nil {
		t.Errorf("missing snapshot = %+v, want nil", missing)
	}
}
