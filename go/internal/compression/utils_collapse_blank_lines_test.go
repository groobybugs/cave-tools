package compression

import (
	"math/rand"
	"strings"
	"testing"
)

func collapseBlankLinesOracle(text string) string {
	return blankLinesRE.ReplaceAllString(text, "\n\n")
}

func TestCollapseBlankLinesMatchesRegex(t *testing.T) {
	cases := []string{
		"",
		"a",
		"a\n",
		"a\n\n",
		"a\n\n\n",
		"a\n\n\nb",
		"a\n\n\n\nb",
		"a\r\n\r\nb",
		"a\r\n\r\n\r\nb",
		"a\r\n\n\r\n\nb",
		"\n\n\n",
		"\r\n\r\n\r\n",
		"a\rb\r\n\r\n\r\nc",
		"a\n\r\n\n\r\nb",
		"no line endings",
		"😀\u2028\n\n\n中文",
		string([]byte{0xff, '\r', '\n', '\r', '\n', '\r', '\n', 0x80}),
	}
	for _, input := range cases {
		got := CollapseBlankLines(input)
		want := collapseBlankLinesOracle(input)
		if got != want {
			t.Fatalf("CollapseBlankLines(%q) = %q, want %q", input, got, want)
		}
		if twice := CollapseBlankLines(got); twice != got {
			t.Fatalf("CollapseBlankLines not idempotent: %q -> %q", got, twice)
		}
	}
}

func TestCollapseBlankLinesRandomizedAgainstRegex(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	alphabet := []byte("ab\r\n\x00\xff")
	for i := 0; i < 10000; i++ {
		raw := make([]byte, rng.Intn(220))
		for j := range raw {
			raw[j] = alphabet[rng.Intn(len(alphabet))]
		}
		input := string(raw)
		got := CollapseBlankLines(input)
		want := collapseBlankLinesOracle(input)
		if got != want {
			t.Fatalf("case %d input %q\n got %q\nwant %q", i, input, got, want)
		}
	}
}

// BenchmarkCollapseBlankLines uses the same large shape as the CLI fixture:
// deterministic log lines with a blank-line run every 37 lines.
func BenchmarkCollapseBlankLines(b *testing.B) {
	var text strings.Builder
	for i := 0; i < 8000; i++ {
		if i > 0 && i%37 == 0 {
			text.WriteString("\n\n")
		}
		text.WriteString("2026-02-01T00:00:00Z GET /api/v1/alpha/bravo status=200 bytes=123 dur=456ms\n")
	}
	input := text.String()
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CollapseBlankLines(input)
	}
}
