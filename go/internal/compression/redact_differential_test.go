package compression

import (
	"math/rand"
	"strings"
	"testing"
)

// redactSecretsBaseline is the pre-optimization implementation. It stays in
// the test file as an independent oracle: the production path may only skip a
// regex when that regex provably cannot match.
func redactSecretsBaseline(input string) string {
	result := input
	for _, rule := range patterns {
		regex := rule.pattern.Regex
		matches := regex.FindAllStringSubmatchIndex(result, -1)
		if len(matches) == 0 {
			continue
		}
		var replaced strings.Builder
		last := 0
		for _, match := range matches {
			replaced.WriteString(result[last:match[0]])
			replaced.Write(regex.ExpandString(nil, rule.replacement, result, match))
			last = match[1]
		}
		replaced.WriteString(result[last:])
		result = replaced.String()
	}
	return result
}

func redactDifferentialCases() []struct {
	name  string
	input string
} {
	// A representative of every pattern, plus the lengths immediately around
	// each minimum and the boundaries where the prefix/suffix is optional.
	cases := []struct {
		name  string
		input string
	}{
		{"bearer start", "bearer abcdefgh"},
		{"bearer middle", "prefix bearer abcdefgh suffix"},
		{"bearer end", "prefix bearer abcdefgh"},
		{"bearer min-1", "bearer abcdefg"},
		{"bearer case", "BEARER ABCDEFGH"},
		{"authorization basic", "authorization: Basic dXNlcjpwYXNz"},
		{"authorization bearer", "AUTHORIZATION:\u00a0Bearer abcdefgh"},
		{"authorization token", "authorization:\u2028token\u2029abcdefgh"},
		{"aws start", "AKIAIOSFODNN7EXAMPLE"},
		{"aws middle", "id AKIAABCDEFGHIJKLMNOP x"},
		{"aws min-1", "AKIAABCDEFGHIJKLMNO"},
		{"api-key hyphen", "api-key=abcdefgh"},
		{"api_key underscore", "api_key: abcdefgh"},
		{"apikey no separator", "apikey=abcdefgh"},
		{"access-key hyphen", "access-key=abcdefgh"},
		{"access_key underscore", "access_key: abcdefgh"},
		{"accesskey no separator", "accesskey=abcdefgh"},
		{"secret-key hyphen", "secret-key=abcdefgh"},
		{"secret_key underscore", "secret_key: abcdefgh"},
		{"secretkey no separator", "secretkey=abcdefgh"},
		{"token", "token=abcdefgh"},
		{"password", "password=abcdefgh"},
		{"passwd", "passwd=abcdefgh"},
		{"pwd", "pwd=abcdefgh"},
		{"secret", "secret=abcdefgh"},
		{"api-key wrong separator", "api-key abcdefgh"},
		{"private key multiline", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----"},
		{"private key no newline", "-----BEGIN PRIVATE KEY-----MIIEow-----END PRIVATE KEY-----"},
		{"private key already redacted", "-----BEGIN PRIVATE KEY-----[REDACTED:Private key block]-----END PRIVATE KEY-----"},
		{"github", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"github min-1", "ghp_" + strings.Repeat("a", 19)},
		{"github min", "ghp_" + strings.Repeat("a", 20)},
		{"github all prefixes", "gho_abcdefghijklmnopqrst ghu_abcdefghijklmnopqrst ghs_abcdefghijklmnopqrst ghr_abcdefghijklmnopqrst"},
		{"gitlab", "glpat-abcdefghijklmnopqrst"},
		{"gitlab min-1", "glpat-" + strings.Repeat("a", 19)},
		{"gitlab min", "glpat-" + strings.Repeat("a", 20)},
		{"openai", "sk-abcdefghijklmnopqrstuvwxyz12345"},
		{"openai min-1", "sk-" + strings.Repeat("a", 19)},
		{"openai min", "sk-" + strings.Repeat("a", 20)},
		{"anthropic", "sk-ant-abcdefghijklmnopqrstuvwxyz12345"},
		{"anthropic min-1", "sk-ant-" + strings.Repeat("a", 19)},
		{"anthropic min", "sk-ant-" + strings.Repeat("a", 20)},
		{"npm", "npm_abcdefghijklmnop"},
		{"npm min-1", "npm_abcdefghi"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij"},
		{"jwt min-1 first segment", "eyJ" + strings.Repeat("a", 9) + "." + strings.Repeat("b", 10) + ".c"},
		{"jwt min first segment", "eyJ" + strings.Repeat("a", 10) + "." + strings.Repeat("b", 10) + ".c"},
		{"jwt min-1 second segment", "eyJ" + strings.Repeat("a", 10) + "." + strings.Repeat("b", 9) + ".c"},
		{"jwt min second segment", "eyJ" + strings.Repeat("a", 10) + "." + strings.Repeat("b", 10) + ".c"},
		{"api-key uppercase", "API-KEY=abcdefgh"},
		{"token uppercase", "TOKEN=abcdefgh"},
		{"password uppercase", "PASSWORD=abcdefgh"},
		{"secret-key uppercase", "SECRET-KEY=abcdefgh"},
		{"database postgres", "postgres://user:s3cr3tp4ss@host:5432/db"},
		{"database postgresql", "postgresql://app:s3cret@db/x"},
		{"database mysql", "mysql://u:p@host/db"},
		{"database mongodb", "mongodb://u:p@host/db"},
		{"database mongodb srv", "mongodb+srv://u:pw1@c.example.net/db"},
		{"database redis empty user", "redis://:pw@cache:6379"},
		{"database rediss", "rediss://u:pw2@cache:6380"},
		{"generic key", "key=" + strings.Repeat("a", 32)},
		{"generic token", "token=" + strings.Repeat("a", 32)},
		{"generic secret", "secret:" + strings.Repeat("a", 32)},
		{"generic password", "password=" + strings.Repeat("a", 32)},
		{"generic credential", "credential=" + strings.Repeat("a", 32)},
		{"generic auth", "auth=" + strings.Repeat("a", 32)},
		{"generic quoted", "password = \"" + strings.Repeat("b", 32) + "\""},
		{"generic single quoted", "secret: '" + strings.Repeat("b", 32) + "'"},
		{"generic min-1", "key=" + strings.Repeat("a", 31)},
		{"nbsp bearer", "Authorization: Bearer\u00a0abcdef123456"},
		{"bom bearer", "Authorization:\ufeffBearer\u00a0abcdef123456"},
		{"line separator bearer", "bearer\u2028abcdefgh"},
		{"paragraph separator bearer", "bearer\u2029abcdefgh"},
		{"nbsp generic", "key\u00a0=\u00a0" + strings.Repeat("a", 32)},
		{"bom generic", "key\ufeff=\ufeff" + strings.Repeat("a", 32)},
		{"line separator generic", "key\u2028=\u2029" + strings.Repeat("a", 32)},
		{"paragraph separator generic", "key\u2029=\u2028" + strings.Repeat("a", 32)},
		{"newline generic", "key=\n" + strings.Repeat("a", 32)},
		{"emoji cjk", "😀 中文 token=ghp_abcdefghijklmnopqrstuvwxyz0123456789 中文 😀"},
		{"emoji before aws", "😀中文 AKIAIOSFODNN7EXAMPLE"},
		{"cjk before private key", "中文-----BEGIN PRIVATE KEY-----\nMIIEow\n-----END PRIVATE KEY-----"},
		{"kelvin token", "to\u212Aen=abcdefgh"},
		{"long s password", "pa\u017Fsword=abcdefgh"},
		{"kelvin generic", "\u212Aey=" + strings.Repeat("a", 32)},
		{"long s generic", "pa\u017Fecret=" + strings.Repeat("a", 32)},
		{"invalid before bearer", string([]byte{0xff, 0xfe}) + " bearer abcdefgh"},
		{"invalid inside bearer", "bearer abcdef" + string([]byte{0x80}) + "gh"},
		{"invalid before aws", string([]byte{0x80}) + "AKIAIOSFODNN7EXAMPLE"},
		{"invalid before generic", string([]byte{0xff}) + "key=" + strings.Repeat("a", 32)},
		{"multiple bearer", "bearer abcdefgh bearer ijklmnop"},
		{"multiple generic", "key=" + strings.Repeat("a", 32) + " token=" + strings.Repeat("b", 32)},
		{"multiple classes", "bearer abcdefgh AKIAIOSFODNN7EXAMPLE token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"aws before api", "api_key=AKIAIOSFODNN7EXAMPLE"},
		{"private key containing token", "-----BEGIN PRIVATE KEY-----\ntoken=abcdefgh\n-----END PRIVATE KEY-----"},
		{"bearer label then status", "bearer abcdefgh\nstatus=200\n"},
		{"generic after bearer label", "bearer abcdefgh token=" + strings.Repeat("a", 32)},
		{"quoted generic after api label", "token=abcdefgh password='" + strings.Repeat("b", 32) + "'"},
		{"long generic 1680", "key=" + strings.Repeat("a", 1680)},
		{"long generic 3000", "key=" + strings.Repeat("a", 3000)},
	}
	return cases
}

func TestRedactSecretsMatchesBaseline(t *testing.T) {
	for _, test := range redactDifferentialCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got := RedactSecrets(test.input)
			want := redactSecretsBaseline(test.input)
			if got != want {
				t.Fatalf("RedactSecrets(%q)\n got: %q\nwant: %q", test.input, got, want)
			}
			if twice := RedactSecrets(got); twice != got {
				t.Fatalf("not idempotent: %q -> %q", got, twice)
			}
		})
	}
}

// TestRedactSecretsLateLargeSecret exercises the suffix scan with the only
// match near the end of a ~630 KiB payload.
func TestRedactSecretsLateLargeSecret(t *testing.T) {
	input := strings.Repeat("ordinary log line\n", 35000) + "token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	got := RedactSecrets(input)
	want := redactSecretsBaseline(input)
	if got != want {
		t.Fatalf("large late-secret output differs from baseline")
	}
	if !strings.Contains(got, "token=[REDACTED:API key param]") {
		t.Fatalf("late secret was not redacted: %q", got[len(got)-80:])
	}
}

// TestRedactSecretsUnicodeCaseFold pins the existing Go SimpleFold behavior
// (wider than TypeScript's non-u case folding). The optimized path must fall
// back to the regex instead of treating these as ASCII near-misses.
func TestRedactSecretsUnicodeCaseFold(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"to\u212Aen=abcdefgh", "to\u212Aen=[REDACTED:API key param]"},
		{"pa\u017Fsword=abcdefgh", "pa\u017Fsword=[REDACTED:API key param]"},
	}
	for _, test := range tests {
		got := RedactSecrets(test.input)
		if got != test.want {
			t.Fatalf("RedactSecrets(%q) = %q, want %q", test.input, got, test.want)
		}
		if baseline := redactSecretsBaseline(test.input); got != baseline {
			t.Fatalf("RedactSecrets(%q) = %q, baseline %q", test.input, got, baseline)
		}
	}
}

// TestRedactSecretsRedactionCount covers the counter contract separately from
// the output: every regex match increments it, including a second pass over an
// already-redacted private-key block whose replacement is unchanged.
func TestRedactSecretsRedactionCount(t *testing.T) {
	before := redactionCount.Load()
	_ = RedactSecrets("bearer abcdefgh bearer ijklmnop")
	if delta := redactionCount.Load() - before; delta != 2 {
		t.Fatalf("multiple-match count delta = %d, want 2", delta)
	}

	first := RedactSecrets("-----BEGIN PRIVATE KEY-----\nMIIEow\n-----END PRIVATE KEY-----")
	before = redactionCount.Load()
	second := RedactSecrets(first)
	if second != first {
		t.Fatalf("already-redacted private key changed: %q -> %q", first, second)
	}
	if delta := redactionCount.Load() - before; delta != 1 {
		t.Fatalf("already-redacted private key count delta = %d, want 1", delta)
	}
}

// TestRedactSecretsRandomizedAgainstBaseline keeps the prefix/suffix proof
// honest with arbitrary ASCII, Unicode, whitespace and invalid UTF-8 bytes.
func TestRedactSecretsRandomizedAgainstBaseline(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	alphabets := []string{
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 =:._-/\"'\n\t",
		"\u00a0\ufeff\u2028\u2029\u212a\u017f😀中文",
	}
	fragments := []string{
		"bearer abcdefgh", "Authorization: Basic dXNlcjpwYXNz", "AKIAIOSFODNN7EXAMPLE",
		"api-key=abcdefgh", "api_key=abcdefgh", "apikey=abcdefgh", "access-key=abcdefgh",
		"secret_key=abcdefgh", "token=abcdefgh", "password=abcdefgh", "secret=abcdefgh",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "glpat-abcdefghijklmnopqrst",
		"sk-abcdefghijklmnopqrstuvwxyz12345", "sk-ant-abcdefghijklmnopqrstuvwxyz12345",
		"npm_abcdefghijklmnop", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij",
		"postgres://user:s3cr3tp4ss@host:5432/db", "key=" + strings.Repeat("a", 32),
		"-----BEGIN PRIVATE KEY-----\nMIIEow\n-----END PRIVATE KEY-----",
		"[REDACTED:Bearer token]", "[REDACTED:Generic long secret]",
	}
	for i := 0; i < 300; i++ {
		var input strings.Builder
		alphabet := alphabets[rng.Intn(len(alphabets))]
		for j := 0; j < rng.Intn(160); j++ {
			r := []rune(alphabet)[rng.Intn(len([]rune(alphabet)))]
			input.WriteRune(r)
		}
		if rng.Intn(2) == 0 {
			fragment := fragments[rng.Intn(len(fragments))]
			at := rng.Intn(input.Len() + 1)
			raw := input.String()
			input.Reset()
			input.WriteString(raw[:at])
			input.WriteString(fragment)
			input.WriteString(raw[at:])
		}
		if rng.Intn(8) == 0 {
			input.WriteByte(byte(0x80 + rng.Intn(128)))
		}
		got := RedactSecrets(input.String())
		want := redactSecretsBaseline(input.String())
		if got != want {
			t.Fatalf("case %d input %q\n got: %q\nwant: %q", i, input.String(), got, want)
		}
	}
}
