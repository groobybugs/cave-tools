package compression

import (
	"strconv"
	"strings"
	"testing"
)

// TestUnifiedDiff ports the re-read diff expectations from test.mjs (the
// "cave__read re-read diffs" block) at the unifiedDiff level, using outputs
// pinned against the TS reference.
func TestUnifiedDiff(t *testing.T) {
	base := make([]string, 100)
	for i := range base {
		base[i] = "line " + strconv.Itoa(i) + " of the original notes file"
	}

	v2 := append([]string(nil), base...)
	v2[50] = "line 50 CHANGED"

	v3 := append([]string(nil), base...)
	v3[10] = "line 10 changed"
	v3[90] = "line 90 changed"

	bigA := make([]string, 3000)
	bigB := make([]string, 3000)
	for i := range bigA {
		bigA[i] = "a" + strconv.Itoa(i)
		bigB[i] = "b" + strconv.Itoa(i)
	}

	tests := []struct {
		name    string
		oldText string
		newText string
		want    *string
	}{
		{
			name:    "equal texts",
			oldText: strings.Join(base, "\n"),
			newText: strings.Join(base, "\n"),
			want:    strPtr(""),
		},
		{
			name:    "small change",
			oldText: strings.Join(base, "\n"),
			newText: strings.Join(v2, "\n"),
			want: strPtr("@@ -48,7 +48,7 @@\n" +
				" line 47 of the original notes file\n" +
				" line 48 of the original notes file\n" +
				" line 49 of the original notes file\n" +
				"-line 50 of the original notes file\n" +
				"+line 50 CHANGED\n" +
				" line 51 of the original notes file\n" +
				" line 52 of the original notes file\n" +
				" line 53 of the original notes file"),
		},
		{
			name:    "two hunks",
			oldText: strings.Join(base, "\n"),
			newText: strings.Join(v3, "\n"),
			want: strPtr("@@ -8,7 +8,7 @@\n" +
				" line 7 of the original notes file\n" +
				" line 8 of the original notes file\n" +
				" line 9 of the original notes file\n" +
				"-line 10 of the original notes file\n" +
				"+line 10 changed\n" +
				" line 11 of the original notes file\n" +
				" line 12 of the original notes file\n" +
				" line 13 of the original notes file\n" +
				"@@ -88,7 +88,7 @@\n" +
				" line 87 of the original notes file\n" +
				" line 88 of the original notes file\n" +
				" line 89 of the original notes file\n" +
				"-line 90 of the original notes file\n" +
				"+line 90 changed\n" +
				" line 91 of the original notes file\n" +
				" line 92 of the original notes file\n" +
				" line 93 of the original notes file"),
		},
		{
			name:    "insert at head",
			oldText: "a\nb\nc",
			newText: "HEAD\na\nb\nc",
			want:    strPtr("@@ -1,3 +1,4 @@\n+HEAD\n a\n b\n c"),
		},
		{
			name:    "delete at tail",
			oldText: "a\nb\nc",
			newText: "a\nb",
			want:    strPtr("@@ -1,3 +1,2 @@\n a\n b\n-c"),
		},
		{
			name:    "insert at tail",
			oldText: "a\nb",
			newText: "a\nb\nTAIL",
			want:    strPtr("@@ -1,2 +1,3 @@\n a\n b\n+TAIL"),
		},
		{
			name:    "empty to x",
			oldText: "",
			newText: "x",
			want:    strPtr("@@ -1,1 +1,1 @@\n-\n+x"),
		},
		{
			name:    "x to empty",
			oldText: "x",
			newText: "",
			want:    strPtr("@@ -1,1 +1,1 @@\n-x\n+"),
		},
		{
			name:    "middle too large",
			oldText: strings.Join(bigA, "\n"),
			newText: strings.Join(bigB, "\n"),
			want:    nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := UnifiedDiff(test.oldText, test.newText)
			switch {
			case test.want == nil && got != nil:
				t.Fatalf("UnifiedDiff() = %q, want nil", *got)
			case test.want != nil && got == nil:
				t.Fatalf("UnifiedDiff() = nil, want %q", *test.want)
			case test.want != nil && got != nil && *got != *test.want:
				t.Fatalf("UnifiedDiff() = %q, want %q", *got, *test.want)
			}
		})
	}
}

// strPtr helps express the TS string | null return value.
func strPtr(s string) *string { return &s }
