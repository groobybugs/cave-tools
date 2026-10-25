// Collation parity tests. The fixtures under testdata/ are generated from Node
// (see testdata/gen_locale_fixture.mjs) and are the reference for both the
// locale resolution and the ordering the x/text collator has to reproduce.
package runtime

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"

	"golang.org/x/text/collate"
)

// localeEnvFixture mirrors testdata/locale_env.json.
type localeEnvFixture struct {
	Node      string `json:"node"`
	ICU       string `json:"icu"`
	CLDR      string `json:"cldr"`
	Unicode   string `json:"unicode"`
	Probe     []string
	Scenarios []struct {
		Name     string
		Env      map[string]string
		Resolved string
		Order    []int
	} `json:"scenarios"`
}

// collateCorpusFixture mirrors testdata/collate_corpus.json.
type collateCorpusFixture struct {
	Node    string `json:"node"`
	ICU     string `json:"icu"`
	CLDR    string `json:"cldr"`
	Unicode string `json:"unicode"`
	Locales []struct {
		EnvValue string `json:"envValue"`
		Locale   string `json:"locale"`
	} `json:"locales"`
	Names  []string            `json:"names"`
	Orders map[string][]int    `json:"orders"`
	Pairs  map[string][][3]int `json:"pairs"`
}

func loadFixture(t *testing.T, name string, target any) {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run `node go/internal/runtime/testdata/gen_locale_fixture.mjs`)", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

var (
	envFixtureOnce    sync.Once
	envFixtureValue   localeEnvFixture
	corpusFixtureOnce sync.Once
	corpusFixtureVal  collateCorpusFixture
)

func envFixture(t *testing.T) *localeEnvFixture {
	t.Helper()
	envFixtureOnce.Do(func() { loadFixture(t, "locale_env.json", &envFixtureValue) })
	return &envFixtureValue
}

func corpusFixture(t *testing.T) *collateCorpusFixture {
	t.Helper()
	corpusFixtureOnce.Do(func() { loadFixture(t, "collate_corpus.json", &corpusFixtureVal) })
	return &corpusFixtureVal
}

func fixtureLookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

// TestResolveLocaleMatchesNode replays every LANG/LC_ALL/LC_COLLATE scenario
// the generator measured against Node, and checks the probe list sorts into the
// same order under the locale our resolver picks. This pins the ICU env
// precedence (LC_ALL -> LC_MESSAGES -> LANG, "C"/"POSIX" -> en_US_POSIX, a set
// but empty variable -> root) as well as the collator itself.
func TestResolveLocaleMatchesNode(t *testing.T) {
	fixture := envFixture(t)
	t.Logf("fixture from node %s, ICU %s, CLDR %s, Unicode %s", fixture.Node, fixture.ICU, fixture.CLDR, fixture.Unicode)
	for _, scenario := range fixture.Scenarios {
		scenario := scenario
		t.Run(scenario.Name, func(t *testing.T) {
			tag := resolveLocale(fixtureLookup(scenario.Env))
			if tag != scenario.Resolved {
				t.Fatalf("locale for %v = %q, want %q", scenario.Env, tag, scenario.Resolved)
			}
			probe := append([]string{}, fixture.Probe...)
			sortStringsLocaleIn(tag, probe)
			want := make([]string, len(scenario.Order))
			for index, position := range scenario.Order {
				want[index] = fixture.Probe[position]
			}
			if !equalStrings(probe, want) {
				t.Fatalf("probe order for %s differs\n got: %q\nwant: %q", scenario.Name, probe, want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// TestASCIICollationMatchesCollator proves the printable-ASCII fast path is
// sign-identical to the x/text collator for every locale that enables it: all
// 95*95 character pairs, all pairs of two-character strings from a
// case/punctuation/digit alphabet, and a random printable-ASCII string corpus.
// Without this the fast path and the collator could disagree and make a sort
// intransitive. It also walks collate.Supported() to make sure no locale with a
// tailored ASCII order slipped into asciiFastPath.
func TestASCIICollationMatchesCollator(t *testing.T) {
	characters := make([]byte, 0, 95)
	for char := byte(0x20); char <= 0x7e; char++ {
		characters = append(characters, char)
	}

	// Two-character strings over an alphabet that exercises primary (punctuation,
	// digits, letters) and tertiary (case) weights.
	alphabet := []byte("aAzZ09 _-.)(Qq!~")
	var pairCorpus []string
	for _, left := range alphabet {
		pairCorpus = append(pairCorpus, string(left))
		for _, right := range alphabet {
			pairCorpus = append(pairCorpus, string([]byte{left, right}))
		}
	}

	// Every locale that enables the fast path must agree with its collator on
	// the single-character order; Turkish (which tailors I/i) must not opt in.
	for _, tag := range collate.Supported() {
		if !asciiFastPath(tag.String()) {
			continue
		}
		for _, left := range characters {
			for _, right := range characters {
				a, b := string(left), string(right)
				if got, want := asciiCompare(a, b), collatorSign(t, tag.String(), a, b); sign(got) != want {
					t.Fatalf("%s: asciiCompare(%q, %q) = %d, collator = %d", tag, a, b, got, want)
				}
			}
		}
	}
	if asciiFastPath("tr-TR") || asciiFastPath("az") {
		t.Fatal("Turkish/Azerbaijani tailor I/i and must not use the ASCII fast path")
	}

	for _, tag := range []string{"en-US", "en", "und"} {
		if !asciiFastPath(tag) {
			t.Fatalf("%s should use the ASCII fast path", tag)
		}
		// Single characters: 95 * 95 pairs.
		for _, left := range characters {
			for _, right := range characters {
				a, b := string(left), string(right)
				if got, want := asciiCompare(a, b), collatorSign(t, tag, a, b); sign(got) != want {
					t.Fatalf("%s: asciiCompare(%q, %q) = %d, collator = %d", tag, a, b, got, want)
				}
			}
		}
		for _, a := range pairCorpus {
			for _, b := range pairCorpus {
				if got, want := asciiCompare(a, b), collatorSign(t, tag, a, b); sign(got) != want {
					t.Fatalf("%s: asciiCompare(%q, %q) = %d, collator = %d", tag, a, b, got, want)
				}
			}
		}
		// Random strings, including case-only twins and prefix relationships.
		random := rand.New(rand.NewSource(0xc011a7e))
		for i := 0; i < 20000; i++ {
			makeName := func() string {
				length := 1 + random.Intn(8)
				buffer := make([]byte, length)
				for index := range buffer {
					buffer[index] = characters[random.Intn(len(characters))]
				}
				return string(buffer)
			}
			a, b := makeName(), makeName()
			if got, want := asciiCompare(a, b), collatorSign(t, tag, a, b); sign(got) != want {
				t.Fatalf("%s: asciiCompare(%q, %q) = %d, collator = %d", tag, a, b, got, want)
			}
		}
		// LocaleCompareIn must route these through the same function.
		if got, want := sign(LocaleCompareIn(tag, "A.txt", "a.txt")), collatorSign(t, tag, "A.txt", "a.txt"); got != want {
			t.Fatalf("%s: LocaleCompareIn(%q, %q) = %d, collator = %d", tag, "A.txt", "a.txt", got, want)
		}
	}
}

// collatorSign is the collator's sign for a pair without the ASCII fast path.
func collatorSign(t *testing.T, tag, a, b string) int {
	t.Helper()
	handle, pool := borrowCollator(tag)
	result := handle.collator.CompareString(a, b)
	pool.Put(handle)
	return sign(result)
}

func sign(value int) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	}
	return 0
}

// TestLocaleCollationCorpus is the differential test against Node: 27k names
// covering Latin-1/Extended, Greek, Cyrillic, Arabic, Hebrew, Thai, Devanagari,
// CJK, kana, Hangul, emoji/ZWJ, combining sequences, digits, case, width and
// ignorable-only differences, sorted exactly as ICU sorts them for en-US (also
// reached via LANG=C), es-ES, sv-SE and tr-TR. Pairwise signs are checked too,
// because a matching order can hide sign-level differences.
//
// x/text ships CLDR 23 collation tables (Unicode 6.2) while Node 24 carries
// ICU 78 (CLDR 48), so two classes of names are expected to differ:
//
//   - characters added after Unicode 6.2 (emoji, ZWJ sequences, skin tones,
//     variation selectors), which x/text can only sort as unassigned code
//     points, and
//   - Han and kana, whose collation data was regenerated between the two CLDR
//     releases.
//
// Those names are excluded from the exact-sequence assertion and covered by the
// concordance floors instead; every other name must match Node exactly. The
// concrete residual pairs are logged per locale and listed in docs/go-port.md.
func TestLocaleCollationCorpus(t *testing.T) {
	fixture := corpusFixture(t)
	t.Logf("fixture from node %s, ICU %s, CLDR %s, Unicode %s; %d names",
		fixture.Node, fixture.ICU, fixture.CLDR, fixture.Unicode, len(fixture.Names))
	if len(fixture.Names) < 20000 {
		t.Fatalf("corpus too small: %d names", len(fixture.Names))
	}
	for _, locale := range fixture.Locales {
		locale := locale
		t.Run(locale.EnvValue, func(t *testing.T) {
			tag := resolveLocale(fixtureLookup(map[string]string{"LANG": locale.EnvValue}))
			if locale.Locale != "default" && tag != locale.Locale {
				t.Fatalf("resolved %q, want %q", tag, locale.Locale)
			}

			names := append([]string{}, fixture.Names...)
			sortStringsLocaleIn(tag, names)
			wantOrder := fixture.Orders[locale.EnvValue]
			if len(wantOrder) != len(names) {
				t.Fatalf("fixture order has %d entries, corpus has %d", len(wantOrder), len(names))
			}
			expected := make([]string, len(wantOrder))
			for position, index := range wantOrder {
				expected[position] = fixture.Names[index]
			}

			// Exact sequence equality outside the CLDR-version-skew classes.
			gotStable := make([]string, 0, len(names))
			wantStable := make([]string, 0, len(expected))
			for _, name := range names {
				if !hasVersionSkewRune(name) {
					gotStable = append(gotStable, name)
				}
			}
			for _, name := range expected {
				if !hasVersionSkewRune(name) {
					wantStable = append(wantStable, name)
				}
			}
			stableEqual := equalStrings(gotStable, wantStable)
			if !stableEqual {
				first := -1
				for index := range wantStable {
					if index >= len(gotStable) || gotStable[index] != wantStable[index] {
						first = index
						break
					}
				}
				t.Fatalf("%d of %d names outside the documented skew classes are out of order (first at %d: got %q, want %q)",
					len(gotStable)-len(wantStable), len(wantStable), first, sliceAt(gotStable, first), sliceAt(wantStable, first))
			}

			// Concordance: how many adjacent pairs of Node's order does Go keep?
			reversed := 0
			var examples []string
			for position := 0; position+1 < len(expected); position++ {
				if LocaleCompareIn(tag, expected[position], expected[position+1]) > 0 {
					reversed++
					if len(examples) < 6 {
						examples = append(examples,
							fmt.Sprintf("%q then %q", expected[position], expected[position+1]))
					}
				}
			}
			concordance := 1 - float64(reversed)/float64(len(expected)-1)

			// Pairwise signs, including pairs inside the skew classes.
			signMismatches := 0
			var signExamples []string
			for _, pair := range fixture.Pairs[locale.EnvValue] {
				a, b, wantSign := fixture.Names[pair[0]], fixture.Names[pair[1]], pair[2]
				if got := sign(LocaleCompareIn(tag, a, b)); got != wantSign {
					signMismatches++
					if len(signExamples) < 6 {
						signExamples = append(signExamples,
							fmt.Sprintf("%q vs %q: got %d, want %d", a, b, got, wantSign))
					}
				}
			}
			signRate := 1 - float64(signMismatches)/float64(len(fixture.Pairs[locale.EnvValue]))

			t.Logf("%s (%s): %d/%d names outside skew match exactly; adjacent concordance %.5f (%d/%d reversed); pair signs %.5f (%d/%d)",
				locale.EnvValue, tag, len(wantStable), len(names), concordance, reversed, len(expected)-1,
				signRate, signMismatches, len(fixture.Pairs[locale.EnvValue]))
			for _, example := range examples {
				t.Logf("  reversed: %s", example)
			}
			for _, example := range signExamples {
				t.Logf("  sign: %s", example)
			}

			// Floors keep the wiring honest: they allow the documented skew (worst
			// measured locale is tr-TR at ~0.9956) and nothing more.
			if concordance < 0.99 {
				t.Fatalf("adjacent concordance %.5f below the 0.99 floor", concordance)
			}
			if signRate < 0.99 {
				t.Fatalf("pair sign match rate %.5f below the 0.99 floor", signRate)
			}
		})
	}
}

func sliceAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return "<none>"
	}
	return values[index]
}

// hasVersionSkewRune reports whether a name contains a character whose
// collation x/text's CLDR 23 tables cannot match ICU 78: anything added to
// Unicode after 6.2 in the symbol/emoji planes, plus the Han and kana data that
// was regenerated for later CLDR releases.
func hasVersionSkewRune(name string) bool {
	for _, char := range name {
		switch {
		case char >= 0x1F000: // emoji, symbols, skin tones, ZWJ sequences
			return true
		case char >= 0x2600 && char <= 0x27BF: // misc symbols and dingbats
			return true
		case char == 0x200D: // zero-width joiner
			return true
		case char >= 0xFE00 && char <= 0xFE0F: // variation selectors
			return true
		case unicode.Is(unicode.Han, char):
			return true
		case char >= 0x3040 && char <= 0x30FF: // hiragana, katakana
			return true
		case char >= 0xFF66 && char <= 0xFF9F: // halfwidth katakana
			return true
		}
	}
	return false
}

// TestSortStableByLocaleKeepsInputOrderForEqualKeys pins the JS stability
// contract: names the collator considers equal must keep their input order and
// must not be reordered by an added tie-break.
func TestSortStableByLocaleKeepsInputOrderForEqualKeys(t *testing.T) {
	const tag = "en-US"
	if got := LocaleCompareIn(tag, "a", "a\u00ad"); got != 0 {
		t.Fatalf("soft hyphen must be ignorable at the compared strength, got %d", got)
	}
	names := []string{"a\u00ad", "a", "A\u00ad", "A", "a\u00adb"}
	sorted := append([]string{}, names...)
	sortStringsLocaleIn(tag, sorted)
	if !equalStrings(sorted, names) {
		t.Fatalf("equal-key names were reordered: got %q, want %q", sorted, names)
	}
}

// TestLocaleCompareConcurrent guards the per-locale collator pools: parallel
// comparisons must not race (run with -race) and must agree with serial ones.
func TestLocaleCompareConcurrent(t *testing.T) {
	names := []string{"éclair", "Éclair", "eclair", "中村", "ｆｕｌｌ", "Straße", "strasse", "ıi", "İi", "a", "A", "😀"}
	want := make([][]int, len(names))
	for i, a := range names {
		want[i] = make([]int, len(names))
		for j, b := range names {
			want[i][j] = sign(LocaleCompareIn("sv-SE", a, b))
		}
	}
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for round := 0; round < 50; round++ {
				for i, a := range names {
					for j, b := range names {
						if got := sign(LocaleCompareIn("sv-SE", a, b)); got != want[i][j] {
							t.Errorf("concurrent compare %q/%q = %d, want %d", a, b, got, want[i][j])
							return
						}
					}
				}
			}
		}()
	}
	wait.Wait()
}

// TestCanonicalLocale pins the POSIX-name to BCP 47 mapping for the shapes the
// fixtures cover.
func TestCanonicalLocale(t *testing.T) {
	cases := map[string]string{
		"":                 "und",
		"C":                "en-US",
		"POSIX":            "en-US",
		"C.UTF-8":          "en-US",
		"en_US.UTF-8":      "en-US",
		"es_ES.UTF-8":      "es-ES",
		"de_DE.UTF-8":      "de-DE",
		"sv_SE.UTF-8":      "sv-SE",
		"tr_TR.UTF-8":      "tr-TR",
		"ja_JP.UTF-8":      "ja-JP",
		"invalid":          "invalid",
		"bogus_XX.UTF-8":   "bogus-XX",
		"en_US_POSIX":      "en-US",
		"tr_TR.UTF-8@euro": "tr-TR-x-lvariant-euro",
	}
	for input, want := range cases {
		if got := canonicalLocale(input); got != want {
			t.Fatalf("canonicalLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func BenchmarkLocaleCompareASCII(b *testing.B) {
	left := "src/runtime/ripgrep.ts"
	right := "src/tools/read.ts"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		LocaleCompareIn("en-US", left, right)
	}
}

func BenchmarkLocaleCompareNonASCII(b *testing.B) {
	left := "каталог/пример-файла.go"
	right := "каталог/Пример-файла.go"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		LocaleCompareIn("en-US", left, right)
	}
}

func BenchmarkLocaleCompareNonASCIIExtended(b *testing.B) {
	left := "中文目录/ｅｘａｍｐｌｅ-😀.txt"
	right := "中文目录/Ｅｘａｍｐｌｅ-😀.txt"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		LocaleCompareIn("en-US", left, right)
	}
}

// benchmarkNames builds a deterministic 10k-name find result: mostly ASCII paths
// with a slice of non-ASCII names, which is what a real tree looks like.
func benchmarkNames(count int) []string {
	random := rand.New(rand.NewSource(7))
	scripts := []string{"é", "ü", "ñ", "ø", "ç", "中", "あ", "가", "а", "α", "😀"}
	names := make([]string, 0, count)
	directories := []string{"src", "internal", "docs", "testdata", "src/runtime", "go/internal/tools"}
	for index := 0; index < count; index++ {
		directory := directories[random.Intn(len(directories))]
		name := fmt.Sprintf("file%04d", random.Intn(10000))
		if random.Intn(8) == 0 {
			name += scripts[random.Intn(len(scripts))]
		}
		if random.Intn(5) == 0 {
			name = strings.ToUpper(name[:1]) + name[1:]
		}
		names = append(names, directory+"/"+name+".go")
	}
	return names
}

func BenchmarkSortStringsLocale10k(b *testing.B) {
	base := benchmarkNames(10000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		names := append([]string{}, base...)
		SortStringsLocale(names)
	}
}

func BenchmarkSortBytewise10k(b *testing.B) {
	base := benchmarkNames(10000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		names := append([]string{}, base...)
		sort.Strings(names)
	}
}
