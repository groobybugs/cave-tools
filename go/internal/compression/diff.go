package compression

import (
	"fmt"
	"strings"
)

// Minimal line-based unified diff (no dependency). Trims the common prefix and
// suffix, then runs an LCS over the changed middle. Returns nil when the
// middle is too large to diff cheaply; callers then send full content.

// port of src/compression/diff.ts:CONTEXT_LINES
const contextLines = 3

// port of src/compression/diff.ts:MAX_LCS_CELLS
const maxLCSCells = 4_000_000

// diffOp is one line of a unified diff.
// port of src/compression/diff.ts:Op
type diffOp struct {
	Kind  byte
	Line  string
	OldNo int
	NewNo int
}

// diffOps returns the edit script for a and b, or ok=false when the changed
// middle is too large to diff cheaply.
// port of src/compression/diff.ts:diffOps
func diffOps(a []string, b []string) ([]diffOp, bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	midA := a[pre : len(a)-suf]
	midB := b[pre : len(b)-suf]
	n := len(midA)
	m := len(midB)
	if int64(n+1)*int64(m+1) > maxLCSCells {
		return nil, false
	}

	// lcs[i][j] = LCS length of midA[i..] and midB[j..]
	w := m + 1
	lcs := make([]uint32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if midA[i] == midB[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}

	ops := make([]diffOp, 0, len(a)+len(b))
	for k := 0; k < pre; k++ {
		ops = append(ops, diffOp{Kind: ' ', Line: a[k], OldNo: k + 1, NewNo: k + 1})
	}
	i := 0
	j := 0
	for i < n || j < m {
		if i < n && j < m && midA[i] == midB[j] {
			ops = append(ops, diffOp{Kind: ' ', Line: midA[i], OldNo: pre + i + 1, NewNo: pre + j + 1})
			i++
			j++
		} else if i < n && (j == m || lcs[(i+1)*w+j] >= lcs[i*w+j+1]) {
			ops = append(ops, diffOp{Kind: '-', Line: midA[i], OldNo: pre + i + 1, NewNo: pre + j})
			i++
		} else {
			ops = append(ops, diffOp{Kind: '+', Line: midB[j], OldNo: pre + i, NewNo: pre + j + 1})
			j++
		}
	}
	for k := 0; k < suf; k++ {
		ops = append(ops, diffOp{
			Kind:  ' ',
			Line:  a[len(a)-suf+k],
			OldNo: len(a) - suf + k + 1,
			NewNo: len(b) - suf + k + 1,
		})
	}
	return ops, true
}

// UnifiedDiff renders unified-diff hunks (no ---/+++ file header). It returns
// a pointer to "" when the texts are equal and nil when the middle is too large.
// port of src/compression/diff.ts:unifiedDiff
func UnifiedDiff(oldText string, newText string) *string {
	ops, ok := diffOps(strings.Split(oldText, "\n"), strings.Split(newText, "\n"))
	if !ok {
		return nil
	}

	changed := make([]int, 0)
	for idx, op := range ops {
		if op.Kind != ' ' {
			changed = append(changed, idx)
		}
	}
	if len(changed) == 0 {
		empty := ""
		return &empty
	}

	out := make([]string, 0)
	c := 0
	for c < len(changed) {
		start := max(0, changed[c]-contextLines)
		end := changed[c]
		// Merge changes whose context windows touch.
		for c+1 < len(changed) && changed[c+1]-end <= 2*contextLines {
			c++
			end = changed[c]
		}
		c++
		stop := min(len(ops)-1, end+contextLines)
		hunk := ops[start : stop+1]
		oldCount := 0
		newCount := 0
		for _, op := range hunk {
			if op.Kind != '+' {
				oldCount++
			}
			if op.Kind != '-' {
				newCount++
			}
		}
		// Start = first line present on that side; with no lines on that side,
		// the line number just before the insertion/deletion point.
		oldStart := hunk[0].OldNo
		for _, op := range hunk {
			if op.Kind != '+' {
				oldStart = op.OldNo
				break
			}
		}
		newStart := hunk[0].NewNo
		for _, op := range hunk {
			if op.Kind != '-' {
				newStart = op.NewNo
				break
			}
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount))
		for _, op := range hunk {
			out = append(out, string(op.Kind)+op.Line)
		}
	}
	diff := strings.Join(out, "\n")
	return &diff
}
