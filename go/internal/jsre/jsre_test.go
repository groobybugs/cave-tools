package jsre

import (
	"regexp"
	"strings"
	"testing"
)

// The expected values below were produced with node 24.19.0 (the TS reference
// runtime) and pasted in as Go literals:
//
//	# whitespace code points matched by /\s/
//	node -e 'let o=[],p=-9;for(let c=0;c<=0x10ffff;c++)if(/\s/.test(String.fromCodePoint(c))){if(c===p+1)o[o.length-1][1]=c;else o.push([c,c]);p=c}console.log(JSON.stringify(o))'
//
//	# one regex row: full matches, or null when the pattern does not match
//	node -e 'console.log(JSON.stringify("a\u00a0b".match(new RegExp("\\s","g"))))'
//
//	# trim row
//	node -e 'for (const s of ["\u00a0abc\u00a0"]) console.log(JSON.stringify([s.trim(),s.trimStart(),s.trimEnd()]))'
//
//	# split row
//	node -e 'console.log(JSON.stringify(" a".split(/\s+/)))'

// jsWhitespaceRanges is the set of code points /\s/ matches, in order.
// node: see the one-liner above; it prints exactly these ten ranges.
var jsWhitespaceRanges = [][2]rune{
	{0x9, 0xd}, {0x20, 0x20}, {0xa0, 0xa0}, {0x1680, 0x1680},
	{0x2000, 0x200a}, {0x2028, 0x2029}, {0x202f, 0x202f},
	{0x205f, 0x205f}, {0x3000, 0x3000}, {0xfeff, 0xfeff},
}

// inJSSpaceSet reports whether r is in the node-derived set.
func inJSSpaceSet(r rune) bool {
	for _, span := range jsWhitespaceRanges {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	return false
}

// TestIsSpaceMatchesNode sweeps every code point and proves IsSpace is exactly
// the set /\s/ matches in node.
func TestIsSpaceMatchesNode(t *testing.T) {
	for r := rune(0); r <= 0x10ffff; r++ {
		if got, want := IsSpace(r), inJSSpaceSet(r); got != want {
			t.Fatalf("IsSpace(%U) = %v, want %v", r, got, want)
		}
	}
}

// TestWhitespaceClassMatchesNode proves the translated `\s` and `\S` classes
// agree with IsSpace (and therefore with node) for every code point.
func TestWhitespaceClassMatchesNode(t *testing.T) {
	space := MustCompile(`\s`, "")
	nonSpace := MustCompile(`\S`, "")
	for r := rune(0); r <= 0x10ffff; r++ {
		s := string(r)
		if got, want := space.MatchString(s), inJSSpaceSet(r); got != want {
			t.Fatalf(`\s matches %U = %v, want %v`, r, got, want)
		}
		if got, want := nonSpace.MatchString(s), !inJSSpaceSet(r); got != want {
			t.Fatalf(`\S matches %U = %v, want %v`, r, got, want)
		}
	}
}

func TestCompileMatchesNode(t *testing.T) {
	tests := []struct {
		name   string
		source string
		flags  string
		input  string
		want   []string
	}{
		{"s-matches-nbsp", `\s`, "g", "a\u00a0b", []string{"\u00a0"}},
		{"s-matches-bom", `\s`, "g", "a\ufeffb", []string{"\ufeff"}},
		{"s-matches-ogham", `\s`, "g", "a\u1680b", []string{"\u1680"}},
		{"s-matches-enquad", `\s`, "g", "a\u2000b", []string{"\u2000"}},
		{"s-matches-hairspace", `\s`, "g", "a\u200ab", []string{"\u200a"}},
		{"s-no-match-200b", `\s`, "g", "a\u200bb", nil},
		{"s-no-match-nel", `\s`, "g", "a\u0085b", nil},
		{"s-matches-ls-ps", `\s`, "g", "a\u2028b\u2029c", []string{"\u2028", "\u2029"}},
		{"S-upper", `\S`, "g", "a\u00a0b\ufeffc", []string{"a", "b", "c"}},
		{"s-in-class", `[\s]`, "g", "a\u00a0b", []string{"\u00a0"}},
		{"negated-class-s", `[^\s]`, "g", "a\u00a0b", []string{"a", "b"}},
		{"class-s-S", `[\s\S]`, "g", "a\n\u00a0b", []string{"a", "\n", "\u00a0", "b"}},
		{"class-S", `[\S]`, "g", "a\u00a0b", []string{"a", "b"}},
		{"negated-class-S", `[^\S]`, "g", "a\u00a0b", []string{"\u00a0"}},
		{"class-with-negated-s", `[a-c^\s]`, "g", "a^\u00a0b", []string{"a", "^", "\u00a0", "b"}},
		{"dot-default", `.`, "g", "a\nb\rc\u2028d\u2029e", []string{"a", "b", "c", "d", "e"}},
		{"dot-s-flag", `.`, "gs", "a\nb\rc\u2028d\u2029e", []string{"a", "\n", "b", "\r", "c", "\u2028", "d", "\u2029", "e"}},
		{"dot-plus-default", `.+`, "g", "ab\ncd", []string{"ab", "cd"}},
		{"dot-plus-s", `.+`, "gs", "ab\ncd", []string{"ab\ncd"}},
		{"dot-in-class-is-literal", `[.]`, "g", "a.b", []string{"."}},
		{"negated-dot-class", `[^.]`, "g", "a.b", []string{"a", "b"}},
		{"caret-multiline", `^\s+`, "gm", "a\n  b", []string{"  "}},
		{"caret-singleline", `^\s+`, "g", "a\n  b", nil},
		{"dollar-multiline", `b$`, "gm", "a\nb\nc", []string{"b"}},
		{"i-flag", `abc`, "gi", "ABC abc AbC", []string{"ABC", "abc", "AbC"}},
		{"i-flag-class", `[a-z]+`, "gi", "ABC Def", []string{"ABC", "Def"}},
		{"word-boundary", `\bcat\b`, "g", "cat cats scat cat", []string{"cat", "cat"}},
		{"d-class", `\d+`, "g", "a12b\u0663\u0664c", []string{"12"}},
		{"w-class", `\w+`, "g", "a_1-\u00e9b", []string{"a_1", "b"}},
		{"escaped-b-in-class", `[\b]`, "g", "a\bb", []string{"\b"}},
		{"empty-class-never-matches", `[]]`, "g", "a]b", nil},
		{"negated-empty-class-any", `[^]`, "g", "a\nb", []string{"a", "\n", "b"}},
		{"class-start-bracket", `[]a]`, "g", "a]", nil},
		{"class-caret-literal", `[\^a]+`, "g", "^^aa", []string{"^^aa"}},
		{"hex-escape", `\x41+`, "g", "AAA", []string{"AAA"}},
		{"unicode-escape", `\u00e9+`, "g", "e\u00e9\u00e9", []string{"\u00e9\u00e9"}},
		{"unicode-brace-escape", `\u{1f600}`, "gu", "x\U0001F600y", []string{"\U0001F600"}},
		{"vertical-tab-escape", `\v`, "g", "a\vb", []string{"\v"}},
		{"escaped-slash", `a\/b`, "g", "a/b", []string{"a/b"}},
		{"noncapture", `(?:ab)+`, "g", "abab", []string{"abab"}},
		{"nested-class-range", `[a-c\d]+`, "g", "ab12x", []string{"ab12"}},
		{"lazy-any", `[\s\S]*?`, "g", "ab\ncd", []string{"", "", "", "", "", ""}},
		{"redact-key", `(bearer\s+)([a-zA-Z0-9\-_.]{8,})`, "gi", "Authorization: Bearer\u00a0abcdef123456", []string{"Bearer\u00a0abcdef123456"}},
		{"aws-key", `(AKIA[0-9A-Z]{16})`, "g", "id AKIAABCDEFGHIJKLMNOP x", []string{"AKIAABCDEFGHIJKLMNOP"}},
		{"classify-ssh", `^ssh\s`, "i", "ssh\u00a0host", []string{"ssh\u00a0"}},
		{"classify-cat", `\bcat\s`, "i", "cat\u00a0file", []string{"cat\u00a0"}},
		{
			"ts-fn-pattern",
			`^(\s*)(export\s+)?(async\s+)?function\s+(\w+)(?:\s*<[^>]*>)?\s*\(([^)]*)\)(?:\s*:\s*([^{]+))?\s*\{?`,
			"",
			"\u00a0export async function load<T>(a: T): Promise<T> {",
			[]string{"\u00a0export async function load<T>(a: T): Promise<T> {"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			re, err := Compile(test.source, test.flags)
			if err != nil {
				t.Fatalf("Compile(%q, %q): %v", test.source, test.flags, err)
			}
			got := re.FindAllString(test.input, -1)
			if len(got) != len(test.want) {
				t.Fatalf("matches = %q, want %q", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("match %d = %q, want %q (all: %q)", i, got[i], test.want[i], got)
				}
			}
		})
	}
}

// TestCompileCaptureGroups pins group numbering through the translation: the
// TS signature pattern keeps its seven groups.
func TestCompileCaptureGroups(t *testing.T) {
	re := MustCompile(`^(\s*)(export\s+)?(async\s+)?function\s+(\w+)(?:\s*<[^>]*>)?\s*\(([^)]*)\)(?:\s*:\s*([^{]+))?\s*\{?`, "")
	got := re.FindStringSubmatch("\u00a0export async function load<T>(a: T): Promise<T> {")
	want := []string{
		"\u00a0export async function load<T>(a: T): Promise<T> {",
		"\u00a0",
		"export ",
		"async ",
		"load",
		"a: T",
		"Promise<T> ",
	}
	if len(got) != len(want) {
		t.Fatalf("groups = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("group %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTrimMatchesNode(t *testing.T) {
	tests := []struct {
		input     string
		trim      string
		trimStart string
		trimEnd   string
	}{
		{"", "", "", ""},
		{" ", "", "", ""},
		{"abc", "abc", "abc", "abc"},
		{"  abc  ", "abc", "abc  ", "  abc"},
		{"\u00a0abc\u00a0", "abc", "abc\u00a0", "\u00a0abc"},
		{"\ufeff abc \ufeff", "abc", "abc \ufeff", "\ufeff abc"},
		{"\u0085abc\u0085", "\u0085abc\u0085", "\u0085abc\u0085", "\u0085abc\u0085"},
		{"\u2028abc\u2029", "abc", "abc\u2029", "\u2028abc"},
		{"\v\fabc\t", "abc", "abc\t", "\v\fabc"},
		{"\u3000abc\u3000", "abc", "abc\u3000", "\u3000abc"},
		{"\u2000abc\u200a", "abc", "abc\u200a", "\u2000abc"},
		{"\u1680abc\u1680", "abc", "abc\u1680", "\u1680abc"},
		{"a b", "a b", "a b", "a b"},
		{"\n\rabc\n\r", "abc", "abc\n\r", "\n\rabc"},
	}

	for _, test := range tests {
		if got := Trim(test.input); got != test.trim {
			t.Errorf("Trim(%q) = %q, want %q", test.input, got, test.trim)
		}
		if got := TrimStart(test.input); got != test.trimStart {
			t.Errorf("TrimStart(%q) = %q, want %q", test.input, got, test.trimStart)
		}
		if got := TrimEnd(test.input); got != test.trimEnd {
			t.Errorf("TrimEnd(%q) = %q, want %q", test.input, got, test.trimEnd)
		}
	}
}

func TestSplitWSMatchesNode(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{""}},
		{" ", []string{"", ""}},
		{"a b", []string{"a", "b"}},
		{" a", []string{"", "a"}},
		{"a ", []string{"a", ""}},
		{"  a   b  ", []string{"", "a", "b", ""}},
		{"a", []string{"a"}},
		{"\u00a0a\u00a0b\ufeff", []string{"", "a", "b", ""}},
		{"a\u0085b", []string{"a\u0085b"}},
		{"a\nb\rc", []string{"a", "b", "c"}},
		{"a\u2028b\u2029c", []string{"a", "b", "c"}},
		{"\t\v\f a \t", []string{"", "a", ""}},
	}

	for _, test := range tests {
		got := SplitWS(test.input)
		if len(got) != len(test.want) {
			t.Fatalf("SplitWS(%q) = %q, want %q", test.input, got, test.want)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Fatalf("SplitWS(%q)[%d] = %q, want %q", test.input, i, got[i], test.want[i])
			}
		}
	}
}

// TestGlobalEmptyMatchIteration documents the one place Go and JS global
// iteration cannot agree: for a pattern that can match the empty string,
// String.prototype.match keeps the empty match that abuts a previous match and
// the one at end of input, while regexp.FindAllString drops the abutting one.
// No ported TS pattern relies on this (every one of them needs at least one
// character, via `+`, `{n,}` or a literal), so the translation stays exact
// where it is used.
func TestGlobalEmptyMatchIteration(t *testing.T) {
	// node: "a b".match(/\s*/g) -> ["", " ", "", ""]
	if got, want := MustCompile(`\s*`, "g").FindAllString("a b", -1), []string{"", " ", ""}; !equalStrings(got, want) {
		t.Fatalf("FindAllString = %q, want %q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCompileRejectsUnsupported names every construct Go cannot express, so a
// future port fails loudly instead of silently matching something else.
func TestCompileRejectsUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		flags   string
		wantErr string
	}{
		{"lookbehind", `(?<=a)b`, "", "lookbehind"},
		{"negative lookbehind", `(?<!a)b`, "", "lookbehind"},
		{"lookahead", `a(?=b)`, "", "lookahead"},
		{"negative lookahead", `a(?!b)`, "", "lookahead"},
		{"named group", `(?<name>a)`, "", "named groups"},
		{"backreference", `(a)\1`, "", "backreferences"},
		{"named backreference", `a\k<x>`, "", "named backreferences"},
		{"octal escape", `\01`, "", "octal"},
		{"unknown escape", `\q`, "", "unsupported escape"},
		{"unsupported flag", `a`, "y", "unsupported flag"},
		{"brace escape without u flag", `\u{41}`, "", "u flag"},
		{"unterminated class", `[abc`, "", "unterminated"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.source, test.flags)
			if err == nil {
				t.Fatalf("Compile(%q, %q) = nil error, want %q", test.source, test.flags, test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Compile(%q, %q) error = %q, want it to mention %q", test.source, test.flags, err, test.wantErr)
			}
		})
	}
}

// TestTranslateKeepsSourcesReadable guards the reviewability rule: the
// translated pattern still starts with the TS source for a regex with no
// JavaScript-only constructs.
func TestTranslateKeepsSourcesReadable(t *testing.T) {
	translated, err := Translate(`^auth\s+login\b`, "i")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.HasPrefix(translated, "(?i)^auth[") {
		t.Fatalf("Translate() = %q, want an (?i) prefix and an expanded \\s class", translated)
	}
	if _, err := regexp.Compile(translated); err != nil {
		t.Fatalf("translated pattern is not valid RE2: %v", err)
	}
}
