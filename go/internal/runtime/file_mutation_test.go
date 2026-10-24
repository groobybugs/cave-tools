package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Ports scripts/test-file-mutation-locks.mjs.

// withLocks is the error-only form of the generic WithFileLocks.
func withLocks(ctx context.Context, paths []string, operation func(context.Context) error) error {
	_, err := WithFileLocks(ctx, paths, func(inner context.Context) (struct{}, error) {
		return struct{}{}, operation(inner)
	})
	return err
}

func TestWithFileLocksDisjointConcurrent(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	first := filepath.Join(dir, "disjoint-a.txt")
	second := filepath.Join(dir, "disjoint-b.txt")
	if err := os.WriteFile(first, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readModifyWrite := func(inner context.Context, path, want, next string) error {
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(current) != want {
			t.Errorf("%s content = %q, want %q", path, current, want)
		}
		_, err = WriteIfUnchanged(inner, path, current, next)
		return err
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		errs <- withLocks(ctx, []string{first}, func(inner context.Context) error {
			return readModifyWrite(inner, first, "alpha\n", "alpha-1\n")
		})
	}()
	go func() {
		defer wait.Done()
		errs <- withLocks(ctx, []string{second}, func(inner context.Context) error {
			return readModifyWrite(inner, second, "bravo\n", "bravo-1\n")
		})
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	if body, _ := os.ReadFile(first); string(body) != "alpha-1\n" {
		t.Fatalf("first = %q", body)
	}
	if body, _ := os.ReadFile(second); string(body) != "bravo-1\n" {
		t.Fatalf("second = %q", body)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}
}

func TestWithFileLocksNestedAndBom(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	bomPath := filepath.Join(dir, "bom.txt")
	if err := os.WriteFile(bomPath, append([]byte{0xef, 0xbb, 0xbf}, []byte("bom\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	err := withLocks(ctx, []string{bomPath}, func(inner context.Context) error {
		_, err := WriteTextPreservingBom(inner, bomPath, "changed\n")
		return err
	})
	if err != nil {
		t.Fatalf("nested writeTextPreservingBom: %v", err)
	}
	body, err := os.ReadFile(bomPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 3 || body[0] != 0xef || body[1] != 0xbb || body[2] != 0xbf {
		t.Fatalf("BOM lost: % x", body)
	}
	if string(body[3:]) != "changed\n" {
		t.Fatalf("body = %q", body[3:])
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}

	optimistic := filepath.Join(dir, "optimistic.txt")
	if err := os.WriteFile(optimistic, []byte("actual\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = withLocks(ctx, []string{optimistic}, func(inner context.Context) error {
		_, err := WriteIfUnchanged(inner, optimistic, []byte("stale\n"), "overwrite\n")
		return err
	})
	if err == nil || !regexp.MustCompile(`File changed after it was read`).MatchString(err.Error()) {
		t.Fatalf("stale write error = %v", err)
	}
	if body, _ := os.ReadFile(optimistic); string(body) != "actual\n" {
		t.Fatalf("stale write modified the file: %q", body)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}
}

func TestWithFileLocksSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	target := filepath.Join(dir, "alias-target.txt")
	alias := filepath.Join(dir, "alias-link.txt")
	if err := os.WriteFile(target, []byte("alias-initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}

	err := withLocks(ctx, []string{alias}, func(inner context.Context) error {
		current, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		_, err = WriteIfUnchanged(inner, target, current, "alias-inner\n")
		return err
	})
	if err != nil {
		t.Fatalf("alias re-entry: %v", err)
	}
	if body, _ := os.ReadFile(target); string(body) != "alias-inner\n" {
		t.Fatalf("target = %q", body)
	}

	// The target and its alias must serialize on one lock.
	if err := os.WriteFile(target, []byte("serial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var active, maxActive atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = withLocks(ctx, []string{target}, func(context.Context) error {
			active.Add(1)
			for {
				old := maxActive.Load()
				if active.Load() <= old || maxActive.CompareAndSwap(old, active.Load()) {
					break
				}
			}
			close(entered)
			<-release
			active.Add(-1)
			return nil
		})
	}()
	w1bWait(t, entered, "alias holder entered")

	secondEntered := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_ = withLocks(ctx, []string{alias}, func(context.Context) error {
			active.Add(1)
			for {
				old := maxActive.Load()
				if active.Load() <= old || maxActive.CompareAndSwap(old, active.Load()) {
					break
				}
			}
			close(secondEntered)
			active.Add(-1)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("alias writer entered before the target lock was released")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	w1bWait(t, holderDone, "alias holder release")
	w1bWait(t, secondDone, "alias second")
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent writers = %d, want 1", got)
	}
}

func TestWithFileLocksOppositeOrder(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	left := filepath.Join(dir, "order-a.txt")
	right := filepath.Join(dir, "order-b.txt")
	if err := os.WriteFile(left, []byte("start-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("start-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writePair := func(paths []string, first, second, tag string) error {
		return withLocks(ctx, paths, func(inner context.Context) error {
			if _, err := LockedWrite(inner, first, tag+"-first\n"); err != nil {
				return err
			}
			_, err := LockedWrite(inner, second, tag+"-second\n")
			return err
		})
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		errs <- writePair([]string{left, right}, left, right, "one")
	}()
	go func() {
		defer wait.Done()
		errs <- writePair([]string{right, left}, right, left, "two")
	}()
	finished := make(chan struct{})
	go func() { wait.Wait(); close(finished) }()
	w1bWait(t, finished, "opposite-order acquisition")
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("opposite order: %v", err)
		}
	}

	leftText, _ := os.ReadFile(left)
	rightText, _ := os.ReadFile(right)
	one := string(leftText) == "one-first\n" && string(rightText) == "one-second\n"
	two := string(leftText) == "two-second\n" && string(rightText) == "two-first\n"
	if !one && !two {
		t.Fatalf("mixed state: left=%q right=%q", leftText, rightText)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}
}

func TestWithFileLocksCallbackThrow(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	path := filepath.Join(dir, "throw.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := withLocks(ctx, []string{path}, func(context.Context) error {
		return os.ErrInvalid
	})
	if err == nil || !strings.Contains(err.Error(), "invalid argument") {
		t.Fatalf("callback error = %v", err)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}

	err = withLocks(ctx, []string{path}, func(inner context.Context) error {
		_, err := WriteIfUnchanged(inner, path, []byte("before\n"), "after\n")
		return err
	})
	if err != nil {
		t.Fatalf("write after throw: %v", err)
	}
	if body, _ := os.ReadFile(path); string(body) != "after\n" {
		t.Fatalf("body = %q", body)
	}

	// A waiter queued behind a failing operation must still run.
	if err := os.WriteFile(path, []byte("queued\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	failedDone := make(chan struct{})
	queuedDone := make(chan struct{})
	var failedErr, queuedErr error
	go func() {
		defer close(failedDone)
		failedErr = withLocks(ctx, []string{path}, func(context.Context) error {
			close(entered)
			<-time.After(10 * time.Millisecond)
			return os.ErrClosed
		})
	}()
	w1bWait(t, entered, "failing operation entered")
	go func() {
		defer close(queuedDone)
		queuedErr = withLocks(ctx, []string{path}, func(inner context.Context) error {
			_, err := LockedWrite(inner, path, "queued-ran\n")
			return err
		})
	}()
	w1bWait(t, failedDone, "failing operation")
	w1bWait(t, queuedDone, "queued operation")
	if failedErr == nil {
		t.Fatal("first operation must fail")
	}
	if queuedErr != nil {
		t.Fatalf("queued operation: %v", queuedErr)
	}
	if body, _ := os.ReadFile(path); string(body) != "queued-ran\n" {
		t.Fatalf("queued write = %q", body)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}
}

func TestWithFileLocksManyWrites(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	path := filepath.Join(dir, "queue.txt")
	if err := os.WriteFile(path, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	current := []byte("v0\n")
	for index := 0; index < 150; index++ {
		next := "v" + strconv.Itoa(index+1) + "\n"
		err := withLocks(ctx, []string{path}, func(inner context.Context) error {
			_, err := WriteIfUnchanged(inner, path, current, next)
			return err
		})
		if err != nil {
			t.Fatalf("sequential write %d: %v", index, err)
		}
		current = []byte(next)
	}
	if body, _ := os.ReadFile(path); string(body) != "v150\n" {
		t.Fatalf("body = %q", body)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}

	var wait sync.WaitGroup
	errs := make(chan error, 100)
	for index := 0; index < 100; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errs <- withLocks(ctx, []string{path}, func(inner context.Context) error {
				_, err := LockedWrite(inner, path, "concurrent-"+strconv.Itoa(index)+"\n")
				return err
			})
		}(index)
	}
	finished := make(chan struct{})
	go func() { wait.Wait(); close(finished) }()
	w1bWait(t, finished, "concurrent queued writes")
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	body, _ := os.ReadFile(path)
	if !regexp.MustCompile(`^concurrent-\d+\n$`).Match(body) {
		t.Fatalf("body = %q", body)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}
}

func TestWithFileLocksDelayedCallbackReacquires(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	path := filepath.Join(dir, "delayed.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gate := make(chan struct{})
	attempted := make(chan struct{})
	delayedDone := make(chan struct{})
	var delayedErr error
	err := withLocks(ctx, []string{path}, func(inner context.Context) error {
		delayedCtx := inner
		go func() {
			<-gate
			close(attempted)
			_, delayedErr = WriteIfUnchanged(delayedCtx, path, []byte("base\n"), "delayed\n")
			close(delayedDone)
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("outer lock: %v", err)
	}

	holderEntered := make(chan struct{})
	holderRelease := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = withLocks(ctx, []string{path}, func(context.Context) error {
			close(holderEntered)
			<-holderRelease
			return nil
		})
	}()
	w1bWait(t, holderEntered, "delayed holder acquired")
	close(gate)
	w1bWait(t, attempted, "delayed callback attempted")

	select {
	case <-delayedDone:
		t.Fatal("delayed callback must wait instead of bypassing the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(holderRelease)
	w1bWait(t, holderDone, "holder release")
	w1bWait(t, delayedDone, "delayed callback")
	if delayedErr != nil {
		t.Fatalf("delayed write: %v", delayedErr)
	}
	if body, _ := os.ReadFile(path); string(body) != "delayed\n" {
		t.Fatalf("body = %q", body)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}
}

func TestWithFileLocksRejectsIncrementalNested(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	outer := filepath.Join(dir, "outer.txt")
	inner := filepath.Join(dir, "inner.txt")
	if err := os.WriteFile(outer, []byte("outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inner, []byte("inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := withLocks(ctx, []string{outer}, func(innerCtx context.Context) error {
		return withLocks(innerCtx, []string{inner}, func(context.Context) error {
			t.Error("nested operation must not run")
			return nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "Acquire every path up front") {
		t.Fatalf("nested acquisition error = %v", err)
	}
	want := "Cannot acquire file lock for " + inner + " while holding " + outer + ". " +
		"Acquire every path up front with withFileLocks([...]) before performing mutations."
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if queue := FileLockQueueSize(); queue != 0 {
		t.Fatalf("FileLockQueueSize = %d, want 0", queue)
	}

	err = withLocks(ctx, []string{outer, inner}, func(innerCtx context.Context) error {
		if _, err := LockedWrite(innerCtx, outer, "outer-ok\n"); err != nil {
			return err
		}
		_, err := LockedWrite(innerCtx, inner, "inner-ok\n")
		return err
	})
	if err != nil {
		t.Fatalf("both paths after rejection: %v", err)
	}
	if body, _ := os.ReadFile(outer); string(body) != "outer-ok\n" {
		t.Fatalf("outer = %q", body)
	}
	if body, _ := os.ReadFile(inner); string(body) != "inner-ok\n" {
		t.Fatalf("inner = %q", body)
	}
}

func TestBomAndByteHelpers(t *testing.T) {
	if got := JoinBom("\uFEFFx", false); got != "x" {
		t.Fatalf("JoinBom(strip) = %q", got)
	}
	if got := JoinBom("x", true); got != "\uFEFFx" {
		t.Fatalf("JoinBom(add) = %q", got)
	}
	if got := JoinBom("\uFEFF\uFEFFy", true); got != "\uFEFFy" {
		t.Fatalf("JoinBom(double) = %q", got)
	}

	if !SameBytes([]byte{1, 2}, []byte{1, 2}) || SameBytes([]byte{1}, []byte{1, 2}) {
		t.Fatal("SameBytes mismatch")
	}

	text, bom, err := DecodeUtf8PreserveBom(append([]byte{0xef, 0xbb, 0xbf}, []byte("hi")...))
	if err != nil || !bom || text != "hi" {
		t.Fatalf("DecodeUtf8PreserveBom(BOM) = (%q, %v, %v)", text, bom, err)
	}
	text, bom, err = DecodeUtf8PreserveBom([]byte("hi"))
	if err != nil || bom || text != "hi" {
		t.Fatalf("DecodeUtf8PreserveBom(plain) = (%q, %v, %v)", text, bom, err)
	}
	if _, _, err := DecodeUtf8PreserveBom([]byte{0xff}); err == nil ||
		err.Error() != "The encoded data was not valid for encoding utf-8" {
		t.Fatalf("DecodeUtf8PreserveBom(invalid) error = %v", err)
	}
}

func TestWriteTextPreservingBomCreatesFile(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	path := filepath.Join(dir, "nested", "new.txt")

	target, err := WriteTextPreservingBom(ctx, path, "\uFEFFhello")
	if err != nil {
		t.Fatalf("WriteTextPreservingBom: %v", err)
	}
	if target.Existed {
		t.Fatal("Existed = true for a new file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte("\uFEFFhello")) {
		t.Fatalf("body = % x", body)
	}
}

func TestLockedWriteCreatesParents(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	path := filepath.Join(dir, "a", "b", "file.txt")
	if _, err := LockedWrite(ctx, path, "data"); err != nil {
		t.Fatalf("LockedWrite: %v", err)
	}
	if body, _ := os.ReadFile(path); string(body) != "data" {
		t.Fatalf("body = %q", body)
	}
}

func TestWithFileLocksReturnsValue(t *testing.T) {
	dir := t.TempDir()
	ctx := w1bCtx(t, dir, nil)
	path := filepath.Join(dir, "value.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := WithFileLocks(ctx, []string{path}, func(inner context.Context) (string, error) {
		target, err := LockedWrite(inner, path, "y")
		return target.Canonical, err
	})
	if err != nil {
		t.Fatalf("WithFileLocks: %v", err)
	}
	want, _ := filepath.EvalSymlinks(path)
	if got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}
