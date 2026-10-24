package compression

import (
	"strconv"
	"strings"
	"testing"
)

var redactBenchSink string

// redactBenchmarkPayload builds the same kind of ~650 KiB CLI stdout the
// large-filter route uses: 8,000 deterministic log lines. The secret variant
// places representative matches at the start, middle and end.
func redactBenchmarkPayload(lines int, withSecrets bool) string {
	var out strings.Builder
	out.Grow(lines * 88)
	for i := 0; i < lines; i++ {
		switch {
		case withSecrets && i == 0:
			out.WriteString("bearer abcdefgh12345678\n")
		case withSecrets && i == lines/2:
			out.WriteString("postgres://user:s3cr3t@host:5432/db\n")
		case withSecrets && i == lines-1:
			out.WriteString("token=ghp_abcdefghijklmnopqrstuvwxyz0123456789\n")
		default:
			out.WriteString("2026-02-")
			out.WriteString(strconv.Itoa(i%28 + 1))
			out.WriteString("T00:00:00Z GET /api/v1/alpha/bravo-")
			out.WriteString(strconv.Itoa(i))
			out.WriteString(" status=200 bytes=")
			out.WriteString(strconv.Itoa((i * 131) % 50000))
			out.WriteString(" dur=")
			out.WriteString(strconv.Itoa((i * 13) % 800))
			out.WriteString("ms\n")
		}
	}
	return out.String()
}

// BenchmarkRedactSecrets exercises the hot path on clean and secret-bearing
// large CLI output. It is a microbenchmark; process and compression-pipeline
// costs are measured separately by scripts/bench-ts-go.mjs.
func BenchmarkRedactSecrets(b *testing.B) {
	cases := []struct {
		name        string
		withSecrets bool
	}{
		{name: "clean", withSecrets: false},
		{name: "secret", withSecrets: true},
	}
	for _, testCase := range cases {
		payload := redactBenchmarkPayload(8000, testCase.withSecrets)
		b.Run(testCase.name, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				redactBenchSink = RedactSecrets(payload)
			}
		})
	}
}
