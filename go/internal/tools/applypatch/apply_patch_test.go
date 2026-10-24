package applypatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Ports the cave__apply_patch assertions of test.mjs (section 7a) plus the
// stripHeredoc equivalence cases (the TS regex uses a backreference).

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cave-applypatch-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func testCtx(t *testing.T, cwd string) context.Context {
	t.Helper()
	return runtime.RunWithContext(context.Background(), &runtime.CallContext{
		Cwd:      cwd,
		Env:      map[string]string{"CAVE_TOOLS_MODE": "enforce"},
		ClientID: "local",
	}, func(ctx context.Context) context.Context { return ctx })
}

func run(t *testing.T, cwd string, args map[string]any) types.ToolResult {
	t.Helper()
	result, err := handle(testCtx(t, cwd), args)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

func patch(t *testing.T, cwd, patchText string) types.ToolResult {
	t.Helper()
	return run(t, cwd, map[string]any{"patchText": patchText})
}

func textOf(result types.ToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(types.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func mustSucceed(t *testing.T, result types.ToolResult, label string) string {
	t.Helper()
	if result.IsError {
		t.Fatalf("%s failed: %s", label, textOf(result))
	}
	return textOf(result)
}

func mustFail(t *testing.T, result types.ToolResult, label, want string) string {
	t.Helper()
	if !result.IsError {
		t.Fatalf("%s must fail, got %q", label, textOf(result))
	}
	message := textOf(result)
	if want != "" && !strings.Contains(message, want) {
		t.Fatalf("%s message %q does not contain %q", label, message, want)
	}
	return message
}

func TestApplyPatchBasics(t *testing.T) {
	dir := t.TempDir()

	addFile := filepath.Join(dir, "added.txt")
	message := mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Add File: "+addFile+"\n+one\n+two\n*** End Patch"), "add")
	if got := readFile(t, addFile); got != "one\ntwo\n" {
		t.Fatalf("add content = %q", got)
	}
	if !strings.Contains(message, "Success. Updated the following files:") {
		t.Fatalf("add message = %q", message)
	}
	if !strings.Contains(message, "A ") || !strings.Contains(message, "added.txt") {
		t.Fatalf("add summary = %q", message)
	}

	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+addFile+"\n@@\n-one\n+ONE\n two\n*** End Patch"), "update")
	if got := readFile(t, addFile); got != "ONE\ntwo\n" {
		t.Fatalf("update content = %q", got)
	}

	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Delete File: "+addFile+"\n*** End Patch"), "delete")
	if _, err := os.ReadFile(addFile); err == nil {
		t.Fatalf("delete left the file behind")
	}

	// add/update/delete in one patch, plus nested-dir add.
	combinedDir := filepath.Join(dir, "combined")
	nested := filepath.Join(combinedDir, "nested", "new.txt")
	modify := writeFile(t, filepath.Join(combinedDir, "modify.txt"), "line1\nline2\n")
	del := writeFile(t, filepath.Join(combinedDir, "delete.txt"), "obsolete\n")
	message = mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Add File: "+nested+"\n+created\n*** Delete File: "+del+"\n*** Update File: "+modify+"\n@@\n-line2\n+changed\n*** End Patch"), "combined")
	if got := readFile(t, nested); got != "created\n" {
		t.Fatalf("nested content = %q", got)
	}
	if got := readFile(t, modify); got != "line1\nchanged\n" {
		t.Fatalf("modify content = %q", got)
	}
	if _, err := os.ReadFile(del); err == nil {
		t.Fatalf("delete left the file behind")
	}
	for _, want := range []string{"A ", "D ", "M "} {
		if !strings.Contains(message, want) {
			t.Fatalf("combined summary %q missing %q", message, want)
		}
	}

	// multiple hunks in one file.
	multi := writeFile(t, filepath.Join(combinedDir, "multi.txt"), "line1\nline2\nline3\nline4\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+multi+"\n@@\n-line2\n+changed2\n@@\n-line4\n+changed4\n*** End Patch"), "multi hunk")
	if got := readFile(t, multi); got != "line1\nchanged2\nline3\nchanged4\n" {
		t.Fatalf("multi content = %q", got)
	}

	// insert-only hunk.
	insertOnly := writeFile(t, filepath.Join(combinedDir, "insert_only.txt"), "alpha\nomega\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+insertOnly+"\n@@\n alpha\n+beta\n omega\n*** End Patch"), "insert only")
	if got := readFile(t, insertOnly); got != "alpha\nbeta\nomega\n" {
		t.Fatalf("insert content = %q", got)
	}

	// appends trailing newline on update.
	noNewline := writeFile(t, filepath.Join(combinedDir, "no_newline.txt"), "no newline at end")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+noNewline+"\n@@\n-no newline at end\n+first line\n+second line\n*** End Patch"), "append newline")
	if got := readFile(t, noNewline); got != "first line\nsecond line\n" {
		t.Fatalf("append content = %q", got)
	}

	// legacy non-@@ update shape.
	legacy := writeFile(t, filepath.Join(combinedDir, "legacy.txt"), "alpha\nbeta\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+legacy+"\n-alpha\n+ALPHA\n beta\n*** End Patch"), "legacy shape")
	if got := readFile(t, legacy); got != "ALPHA\nbeta\n" {
		t.Fatalf("legacy content = %q", got)
	}
}

func TestApplyPatchMoves(t *testing.T) {
	dir := t.TempDir()

	moveSrc := writeFile(t, filepath.Join(dir, "old", "name.txt"), "old content\n")
	moveDst := filepath.Join(dir, "renamed", "dir", "name.txt")
	message := mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+moveSrc+"\n*** Move to: "+moveDst+"\n@@\n-old content\n+new content\n*** End Patch"), "move")
	if _, err := os.ReadFile(moveSrc); err == nil {
		t.Fatalf("move left the source behind")
	}
	if got := readFile(t, moveDst); got != "new content\n" {
		t.Fatalf("move content = %q", got)
	}
	if !strings.Contains(message, "M ") || !strings.Contains(message, "name.txt") {
		t.Fatalf("move summary = %q", message)
	}

	// move overwrites existing destination.
	overwriteSrc := writeFile(t, filepath.Join(dir, "old2", "name.txt"), "from\n")
	overwriteDst := writeFile(t, filepath.Join(dir, "renamed2", "dir", "name.txt"), "existing\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+overwriteSrc+"\n*** Move to: "+overwriteDst+"\n@@\n-from\n+new\n*** End Patch"), "move overwrite")
	if _, err := os.ReadFile(overwriteSrc); err == nil {
		t.Fatalf("move overwrite left the source behind")
	}
	if got := readFile(t, overwriteDst); got != "new\n" {
		t.Fatalf("move overwrite content = %q", got)
	}

	// add overwrites an existing file (opencode parity).
	dup := writeFile(t, filepath.Join(dir, "duplicate.txt"), "old content\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Add File: "+dup+"\n+new content\n*** End Patch"), "add overwrite")
	if got := readFile(t, dup); got != "new content\n" {
		t.Fatalf("add overwrite content = %q", got)
	}

	// BOM preservation on move.
	bomSrc := writeFile(t, filepath.Join(dir, "bom_move_src.cs"), "\uFEFFhello\n")
	bomDst := filepath.Join(dir, "bom_move_dst.cs")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+bomSrc+"\n*** Move to: "+bomDst+"\n@@\n-hello\n+world\n*** End Patch"), "bom move")
	if _, err := os.ReadFile(bomSrc); err == nil {
		t.Fatalf("bom move left the source behind")
	}
	if got := readFile(t, bomDst); got != "\uFEFFworld\n" {
		t.Fatalf("bom move content = %q", got)
	}
}

func TestApplyPatchRejections(t *testing.T) {
	dir := t.TempDir()

	missingUpdate := filepath.Join(dir, "missing.txt")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+missingUpdate+"\n@@\n-nope\n+better\n*** End Patch"), "missing update", "apply_patch verification failed: Failed to read file to update")

	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Delete File: "+missingUpdate+"\n*** End Patch"), "missing delete", "apply_patch verification failed: Failed to read file for deletion")

	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Frobnicate File: foo\n*** End Patch"), "invalid header", "apply_patch verification failed")

	mustFail(t, patch(t, dir, "*** Begin Patch\n*** End Patch"), "empty patch", "patch rejected: empty patch")

	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Add File: "+filepath.Join(dir, "x.txt")+"\n+x\n"), "missing end marker", "apply_patch verification failed")

	// verification failure leaves no side effects (atomicity).
	atomicDir := filepath.Join(dir, "atomic")
	atomicPatch := "*** Begin Patch\n*** Add File: " + filepath.Join(atomicDir, "created.txt") + "\n+hello\n*** Update File: " + filepath.Join(atomicDir, "missing.txt") + "\n@@\n-old\n+new\n*** End Patch"
	mustFail(t, patch(t, dir, atomicPatch), "atomic", "apply_patch verification failed")
	if _, err := os.ReadFile(filepath.Join(atomicDir, "created.txt")); err == nil {
		t.Fatalf("atomic patch wrote the add")
	}

	// missing context rejects, target unchanged.
	ctxFile := writeFile(t, filepath.Join(dir, "ctx", "modify.txt"), "line1\nline2\n")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+ctxFile+"\n@@\n-missing\n+changed\n*** End Patch"), "missing context", "apply_patch verification failed")
	if got := readFile(t, ctxFile); got != "line1\nline2\n" {
		t.Fatalf("missing context changed the file: %q", got)
	}

	// Delete of a directory is rejected before writes.
	dirDeleteDir := filepath.Join(dir, "dir-delete")
	dirToDelete := filepath.Join(dirDeleteDir, "dir")
	if err := os.MkdirAll(dirToDelete, 0o755); err != nil {
		t.Fatal(err)
	}
	dirDeletePatch := "*** Begin Patch\n*** Add File: " + filepath.Join(dirDeleteDir, "new.txt") + "\n+before\n*** Delete File: " + dirToDelete + "\n*** End Patch"
	mustFail(t, patch(t, dir, dirDeletePatch), "delete directory", "Cannot delete directory with Delete File")
	if _, err := os.ReadFile(filepath.Join(dirDeleteDir, "new.txt")); err == nil {
		t.Fatalf("delete-directory patch wrote the add")
	}

	// Add over directory rejected before earlier add writes.
	addDirDir := filepath.Join(dir, "add-dir")
	addDirTarget := filepath.Join(addDirDir, "dir-target")
	if err := os.MkdirAll(addDirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	addDirPatch := "*** Begin Patch\n*** Add File: " + filepath.Join(addDirDir, "created.txt") + "\n+created\n*** Add File: " + addDirTarget + "\n+bad\n*** End Patch"
	mustFail(t, patch(t, dir, addDirPatch), "add over directory", "Cannot write file over directory")
	if _, err := os.ReadFile(filepath.Join(addDirDir, "created.txt")); err == nil {
		t.Fatalf("add-over-directory patch wrote the first add")
	}

	// Move over directory rejected before earlier add writes.
	moveDirRoot := filepath.Join(dir, "move-dir-target")
	moveDirSource := writeFile(t, filepath.Join(moveDirRoot, "src.txt"), "src\n")
	moveDirTarget := filepath.Join(moveDirRoot, "dir-target")
	if err := os.MkdirAll(moveDirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	moveDirPatch := "*** Begin Patch\n*** Add File: " + filepath.Join(moveDirRoot, "created.txt") + "\n+created\n*** Update File: " + moveDirSource + "\n*** Move to: " + moveDirTarget + "\n@@\n-src\n+from-src\n*** End Patch"
	mustFail(t, patch(t, dir, moveDirPatch), "move over directory", "Cannot write file over directory")
	if _, err := os.ReadFile(filepath.Join(moveDirRoot, "created.txt")); err == nil {
		t.Fatalf("move-over-directory patch wrote the add")
	}
	if got := readFile(t, moveDirSource); got != "src\n" {
		t.Fatalf("move-over-directory changed the source: %q", got)
	}
}

func TestApplyPatchConflictGuards(t *testing.T) {
	dir := t.TempDir()

	// Move to same canonical path as source is rejected.
	selfSrc := writeFile(t, filepath.Join(dir, "self.txt"), "stay\n")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+selfSrc+"\n*** Move to: "+selfSrc+"\n@@\n-stay\n+changed\n*** End Patch"), "self move", "Move source and destination resolve to same path")
	if got := readFile(t, selfSrc); got != "stay\n" {
		t.Fatalf("self move changed the file: %q", got)
	}

	// Move + Add to same destination rejected.
	moveAddSrc := writeFile(t, filepath.Join(dir, "move-add", "src.txt"), "src\n")
	moveAddDst := filepath.Join(dir, "move-add", "dst.txt")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+moveAddSrc+"\n*** Move to: "+moveAddDst+"\n@@\n-src\n+from-src\n*** Add File: "+moveAddDst+"\n+from-add\n*** End Patch"), "move+add", "already")
	if got := readFile(t, moveAddSrc); got != "src\n" {
		t.Fatalf("move+add changed the source: %q", got)
	}
	if _, err := os.ReadFile(moveAddDst); err == nil {
		t.Fatalf("move+add wrote the destination")
	}

	// Move + Update destination rejected.
	moveUpdSrc := writeFile(t, filepath.Join(dir, "move-upd", "src.txt"), "src\n")
	moveUpdDst := writeFile(t, filepath.Join(dir, "move-upd", "dst.txt"), "existing\n")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+moveUpdSrc+"\n*** Move to: "+moveUpdDst+"\n@@\n-src\n+from-src\n*** Update File: "+moveUpdDst+"\n@@\n-existing\n+changed\n*** End Patch"), "move+update", "move destination")

	// Two moves to the same destination rejected.
	moveTwoA := writeFile(t, filepath.Join(dir, "move-two", "a.txt"), "A\n")
	moveTwoB := writeFile(t, filepath.Join(dir, "move-two", "b.txt"), "B\n")
	moveTwoDst := filepath.Join(dir, "move-two", "shared.txt")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+moveTwoA+"\n*** Move to: "+moveTwoDst+"\n@@\n-A\n+from-A\n*** Update File: "+moveTwoB+"\n*** Move to: "+moveTwoDst+"\n@@\n-B\n+from-B\n*** End Patch"), "two moves", "moves multiple sources to same destination")

	// Move destination then delete same destination rejected.
	moveDelSrc := writeFile(t, filepath.Join(dir, "move-del", "src.txt"), "src\n")
	moveDelDst := writeFile(t, filepath.Join(dir, "move-del", "dst.txt"), "dst\n")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+moveDelSrc+"\n*** Move to: "+moveDelDst+"\n@@\n-src\n+from-src\n*** Delete File: "+moveDelDst+"\n*** End Patch"), "move+delete", "deletes a path that is also a move destination")
	if readFile(t, moveDelSrc) != "src\n" || readFile(t, moveDelDst) != "dst\n" {
		t.Fatalf("move+delete changed files")
	}

	// Move swap rejected before writes.
	swapA := writeFile(t, filepath.Join(dir, "move-swap", "a.txt"), "A\n")
	swapB := writeFile(t, filepath.Join(dir, "move-swap", "b.txt"), "B\n")
	mustFail(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+swapA+"\n*** Move to: "+swapB+"\n@@\n-A\n+from-A\n*** Update File: "+swapB+"\n*** Move to: "+swapA+"\n@@\n-B\n+from-B\n*** End Patch"), "move swap", "move destination")
	if readFile(t, swapA) != "A\n" || readFile(t, swapB) != "B\n" {
		t.Fatalf("move swap changed files")
	}
}

func TestApplyPatchMatchTiers(t *testing.T) {
	dir := t.TempDir()

	// end-of-file anchor.
	eofFile := writeFile(t, filepath.Join(dir, "tail.txt"), "alpha\nlast\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+eofFile+"\n@@\n-last\n+end\n*** End of File\n*** End Patch"), "eof anchor")
	if got := readFile(t, eofFile); got != "alpha\nend\n" {
		t.Fatalf("eof content = %q", got)
	}

	// @@ context disambiguation.
	ctxAmbFile := writeFile(t, filepath.Join(dir, "multi_ctx.txt"), "fn a\nx=10\ny=2\nfn b\nx=10\ny=20\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+ctxAmbFile+"\n@@ fn b\n-x=10\n+x=11\n*** End Patch"), "context")
	if got := readFile(t, ctxAmbFile); got != "fn a\nx=10\ny=2\nfn b\nx=11\ny=20\n" {
		t.Fatalf("context content = %q", got)
	}

	// trailing-whitespace match.
	wsFile := writeFile(t, filepath.Join(dir, "trailing_ws.txt"), "line1  \nline2\nline3   \n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+wsFile+"\n@@\n-line2\n+changed\n*** End Patch"), "trailing ws")
	if got := readFile(t, wsFile); got != "line1  \nchanged\nline3   \n" {
		t.Fatalf("trailing ws content = %q", got)
	}

	// leading-whitespace match.
	leadWsFile := writeFile(t, filepath.Join(dir, "leading_ws.txt"), "  line1\nline2\n  line3\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+leadWsFile+"\n@@\n-line2\n+changed\n*** End Patch"), "leading ws")
	if got := readFile(t, leadWsFile); got != "  line1\nchanged\n  line3\n" {
		t.Fatalf("leading ws content = %q", got)
	}

	// Unicode punctuation match.
	uniFile := writeFile(t, filepath.Join(dir, "unicode.txt"), "He said \u201Chello\u201D\nsome\u2014dash\nend\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+uniFile+"\n@@\n-He said \"hello\"\n+He said \"hi\"\n*** End Patch"), "unicode")
	if got := readFile(t, uniFile); got != "He said \"hi\"\nsome\u2014dash\nend\n" {
		t.Fatalf("unicode content = %q", got)
	}

	// NBSP normalization.
	nbspFile := writeFile(t, filepath.Join(dir, "nbsp.txt"), "line1\u00A0end\nline2\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+nbspFile+"\n@@\n-line1 end\n+line1 changed\n*** End Patch"), "nbsp")
	if got := readFile(t, nbspFile); got != "line1 changed\nline2\n" {
		t.Fatalf("nbsp content = %q", got)
	}
}

func TestApplyPatchBOMUpdate(t *testing.T) {
	dir := t.TempDir()
	bomFile := writeFile(t, filepath.Join(dir, "bom.cs"), "\uFEFFusing System;\n\nclass Test {}\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+bomFile+"\n@@\n class Test {}\n+class Next {}\n*** End Patch"), "bom update")
	if got := readFile(t, bomFile); got != "\uFEFFusing System;\n\nclass Test {}\nclass Next {}\n" {
		t.Fatalf("bom content = %q", got)
	}
}

func TestApplyPatchDuplicateUpdatesCoalesce(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, filepath.Join(dir, "dup.txt"), "a\nb\nc\n")
	mustSucceed(t, patch(t, dir, "*** Begin Patch\n*** Update File: "+file+"\n@@\n-a\n+A\n*** Update File: "+file+"\n@@\n-c\n+C\n*** End Patch"), "duplicate updates")
	if got := readFile(t, file); got != "A\nb\nC\n" {
		t.Fatalf("duplicate update content = %q", got)
	}
}

func TestApplyPatchHeredoc(t *testing.T) {
	dir := t.TempDir()

	heredocFile := filepath.Join(dir, "heredoc_test.txt")
	mustSucceed(t, patch(t, dir, "cat <<'EOF'\n*** Begin Patch\n*** Add File: "+heredocFile+"\n+heredoc content\n*** End Patch\nEOF"), "cat heredoc")
	if got := readFile(t, heredocFile); got != "heredoc content\n" {
		t.Fatalf("heredoc content = %q", got)
	}

	noCatFile := filepath.Join(dir, "heredoc_no_cat.txt")
	mustSucceed(t, patch(t, dir, "<<EOF\n*** Begin Patch\n*** Add File: "+noCatFile+"\n+no cat prefix\n*** End Patch\nEOF"), "bare heredoc")
	if got := readFile(t, noCatFile); got != "no cat prefix\n" {
		t.Fatalf("no-cat content = %q", got)
	}
}

func TestApplyPatchRequiresPatchText(t *testing.T) {
	dir := t.TempDir()

	result := run(t, dir, map[string]any{})
	if !result.IsError || !strings.Contains(textOf(result), "patchText is required") {
		t.Fatalf("missing patchText = %q", textOf(result))
	}

	result = run(t, dir, map[string]any{"patchText": "   \n\t "})
	if !result.IsError || !strings.Contains(textOf(result), "patchText is required") {
		t.Fatalf("blank patchText = %q", textOf(result))
	}

	result = patch(t, dir, "*** Begin Patch\n*** End Patch\n")
	if !result.IsError || !strings.Contains(textOf(result), "patch rejected: empty patch") {
		t.Fatalf("empty patch = %q", textOf(result))
	}
}

// TestStripHeredocEquivalence pins the hand-ported regex (backreference) on the
// inputs the TS pattern distinguishes.
func TestStripHeredocEquivalence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"cat single quotes", "cat <<'EOF'\nbody\nEOF", "body"},
		{"cat double quotes", "cat <<\"EOF\"\nbody\nEOF", "body"},
		{"bare", "<<EOF\nbody\nEOF", "body"},
		{"multi line body", "<<EOF\nline1\nline2\nEOF", "line1\nline2"},
		{"trailing whitespace", "<<EOF\nbody\nEOF  \n", "body"},
		{"header whitespace", "<<EOF   \nbody\nEOF", "body"},
		{"empty body", "<<EOF\n\nEOF", ""},
		{"mismatched delimiter", "<<EOF\nbody\nOTHER", "<<EOF\nbody\nOTHER"},
		{"no closing", "<<EOF\nbody", "<<EOF\nbody"},
		{"no newline after header", "<<EOF body\nEOF", "<<EOF body\nEOF"},
		{"not a heredoc", "*** Begin Patch\n*** End Patch", "*** Begin Patch\n*** End Patch"},
		{"cat without space", "cat<<EOF\nbody\nEOF", "cat<<EOF\nbody\nEOF"},
		{"delimiter needs word chars", "<<--\nbody\n--", "<<--\nbody\n--"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripHeredoc(tt.input); got != tt.want {
				t.Fatalf("stripHeredoc(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSeekSequenceTiers(t *testing.T) {
	lines := []string{"alpha   ", "beta\t", "gamma"}

	if got := seekSequence(lines, []string{"alpha", "beta"}, 0, false); got != 0 {
		t.Fatalf("rstrip tier = %d", got)
	}
	if got := seekSequence(lines, []string{"beta", "gamma"}, 0, false); got != 1 {
		t.Fatalf("trim tier = %d", got)
	}
	if got := seekSequence(lines, []string{"gamma"}, 0, true); got != 2 {
		t.Fatalf("eof tier = %d", got)
	}
	if got := seekSequence(lines, []string{"nope"}, 0, false); got != -1 {
		t.Fatalf("miss = %d", got)
	}
	if got := seekSequence(lines, nil, 0, false); got != -1 {
		t.Fatalf("empty pattern = %d", got)
	}
}

func TestNormalizeUnicode(t *testing.T) {
	input := "‘’‚‛“”„‟‐‑‒–—―…\u00A0"
	want := "''''\"\"\"\"------... "
	if got := normalizeUnicode(input); got != want {
		t.Fatalf("normalizeUnicode = %q, want %q", got, want)
	}
}
