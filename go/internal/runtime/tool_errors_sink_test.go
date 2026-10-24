package runtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/types"
)

// observeForSinkTest enqueues one soft error without touching global names.
func observeForSinkTest(sessionID string) {
	ObserveToolCall(context.Background(), ObserveToolCallOptions{
		Tool:        "cave__ls",
		Args:        map[string]any{"path": "/nonexistent-" + sessionID},
		SchemaProps: []string{"path"},
		SessionID:   sessionID,
		StartedAt:   time.Now().UnixMilli(),
		Result: types.ToolResult{
			Content: []types.Content{types.Text("Path not found")},
			IsError: true,
		},
	})
}

// TestObserveToolCallDoesNotWaitForStalledSink stalls a flush after it has
// taken sink ownership and proves the tool-call path still returns and
// enqueues its record. Regression: holding the queue lock across SQLite I/O
// made ObserveToolCall wait for a slow disk.
func TestObserveToolCallDoesNotWaitForStalledSink(t *testing.T) {
	toolErrorsTestEnv(t)
	observeForSinkTest("sink-stalled-1")

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	releaseSink := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSink()
	toolErrorSinkHookForTests = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	t.Cleanup(func() { toolErrorSinkHookForTests = nil })

	flushed := make(chan struct{})
	go func() {
		FlushToolErrorQueue()
		close(flushed)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never reached the sink")
	}

	// The sink is now intentionally held mid-flush. The call path must not
	// wait for it: bounded in-memory work only.
	observed := make(chan struct{})
	go func() {
		observeForSinkTest("sink-stalled-2")
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("ObserveToolCall waited for stalled sink I/O")
	}
	if depth := ToolErrorQueueDepthForTests(); depth != 1 {
		t.Errorf("queue depth while sink stalled = %d, want 1", depth)
	}

	releaseSink()
	<-flushed
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != 2 {
		t.Errorf("persisted rows = %d, want 2", got)
	}
}

// TestRestoreKeepsQueueBound fills the queue while a locked-store batch is
// being restored and asserts the 256-row bound still holds.
func TestRestoreKeepsQueueBound(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	observeForSinkTest("restore-initial")

	lock := openTestDB(t, dir)
	if _, err := lock.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer func() { _ = lock.Close() }()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	releaseSink := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSink()
	toolErrorSinkHookForTests = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	t.Cleanup(func() { toolErrorSinkHookForTests = nil })

	flushed := make(chan struct{})
	go func() {
		FlushToolErrorQueue()
		close(flushed)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never reached the sink")
	}

	for i := 0; i < 300; i++ {
		observeForSinkTest(fmt.Sprintf("restore-%03d", i))
	}
	if depth := ToolErrorQueueDepthForTests(); depth != ToolErrorQueueCap {
		t.Fatalf("queue depth while sink stalled = %d, want %d", depth, ToolErrorQueueCap)
	}

	releaseSink()
	<-flushed
	if depth := ToolErrorQueueDepthForTests(); depth > ToolErrorQueueCap {
		t.Errorf("queue depth after busy restore = %d, want <= %d", depth, ToolErrorQueueCap)
	}
	if _, err := lock.Exec("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	FlushToolErrorQueue()
	if depth := ToolErrorQueueDepthForTests(); depth != 0 {
		t.Errorf("queue depth after unlock = %d, want 0", depth)
	}
}

// TestFlushOrderPreservedUnderConcurrentFlush runs several flushers against
// the same queue and asserts rows were still inserted in FIFO order.
func TestFlushOrderPreservedUnderConcurrentFlush(t *testing.T) {
	dir := toolErrorsTestEnv(t)
	const records = 120
	for i := 0; i < records; i++ {
		observeForSinkTest(fmt.Sprintf("order-%03d", i))
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				FlushToolErrorQueue(1)
			}
		}()
	}
	wg.Wait()
	FlushToolErrorQueue()

	db := openTestDB(t, dir)
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT session_hash FROM tool_errors ORDER BY rowid")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != records {
		t.Fatalf("rows = %d, want %d", len(got), records)
	}
	for i, hash := range got {
		if want := SessionHashFor(fmt.Sprintf("order-%03d", i)); hash != want {
			t.Fatalf("row %d session hash = %q, want %q (FIFO order broken)", i, hash, want)
		}
	}
}

// TestFlushToolErrorQueueBoundedBatches proves the explicit drain stays
// bounded per call and still drains everything when called without a limit.
func TestFlushToolErrorQueueBoundedBatches(t *testing.T) {
	toolErrorsTestEnv(t)
	const records = 120
	for i := 0; i < records; i++ {
		observeForSinkTest(fmt.Sprintf("bounded-%03d", i))
	}

	FlushToolErrorQueue(1)
	if depth := ToolErrorQueueDepthForTests(); depth != records-toolErrorFlushBatch {
		t.Errorf("depth after one batch = %d, want %d", depth, records-toolErrorFlushBatch)
	}
	if got := CountToolErrors().Total; got != int64(toolErrorFlushBatch) {
		t.Errorf("persisted after one batch = %d, want %d", got, toolErrorFlushBatch)
	}

	FlushToolErrorQueue()
	if depth := ToolErrorQueueDepthForTests(); depth != 0 {
		t.Errorf("depth after drain = %d, want 0", depth)
	}
	if got := CountToolErrors().Total; got != records {
		t.Errorf("persisted after drain = %d, want %d", got, records)
	}
}

// TestConcurrentObserveFlushReset exercises the queue/sink/reset boundaries
// under the race detector with the existing isolated fixtures.
func TestConcurrentObserveFlushReset(t *testing.T) {
	toolErrorsTestEnv(t)
	const (
		observers = 8
		perWorker = 20
		flushers  = 3
		resets    = 10
	)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < observers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for j := 0; j < perWorker; j++ {
				observeForSinkTest(fmt.Sprintf("race-%02d-%02d", worker, j))
			}
		}(i)
	}
	for i := 0; i < flushers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < resets; j++ {
				FlushToolErrorQueue(1)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for j := 0; j < resets; j++ {
			ResetToolErrorStateForTests()
		}
	}()
	close(start)
	wg.Wait()

	FlushToolErrorQueue()
	if depth := ToolErrorQueueDepthForTests(); depth != 0 {
		t.Errorf("depth after final drain = %d, want 0", depth)
	}
	before := CountToolErrors().Total
	if before > observers*perWorker {
		t.Errorf("persisted rows = %d, exceeds observed calls", before)
	}

	// The sink must still be usable after concurrent resets.
	observeForSinkTest("race-after-reset")
	FlushToolErrorQueue()
	if got := CountToolErrors().Total; got != before+1 {
		t.Errorf("post-reset row not persisted: total = %d, want %d", got, before+1)
	}
}
