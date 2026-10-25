package jsre

import (
	"bufio"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// The fixtures in testdata were generated with node 24.19.0 (the TS reference
// runtime, ICU 78.3 / Unicode 17.0) and pasted in unchanged:
//
//	# exhaustive single-code-point mapping (code-point-hex:lowercase-utf8-hex)
//	node -e 'const o=[];for(let cp=0;cp<=0x10ffff;cp++){if(cp>=0xd800&&cp<=0xdfff)continue;const s=String.fromCodePoint(cp),l=s.toLowerCase();if(l!==s)o.push(cp.toString(16)+":"+Buffer.from(l,"utf8").toString("hex"))}console.log(o.join("\n"))'
//
//	# Cased / Case_Ignorable recovery used for the tables in lower_unicode17.go
//	node -e 'for(let cp=0;cp<=0x10ffff;cp++){if(cp>=0xd800&&cp<=0xdfff)continue;const x=String.fromCodePoint(cp);const s1=(x+"\u03a3").toLowerCase();const s5=("A"+x+"\u03a3").toLowerCase();if(s1.includes("\u03c2"))console.log("cased",cp.toString(16));else if(s5.includes("\u03c2"))console.log("ignorable",cp.toString(16))}'
//
//	# context rows (input-utf8-hex<TAB>lowercase-utf8-hex): the Sigma / dotted-I
//	# matrices, every measured delta code point in context, one representative
//	# per Case_Ignorable category, the sixteen case-ignorable punctuation
//	# characters, and 120 seeded pseudo-random mixed strings.
//	node /tmp/lcprobe/gen_fixtures.mjs   # regenerates both fixtures
func TestToLowerJSCodePoints(t *testing.T) {
	mapped := readLowerMap(t, "testdata/lower_node.txt")
	if len(mapped) != 1488 {
		t.Fatalf("fixture has %d mappings, want 1488", len(mapped))
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		want := string(r)
		if low, ok := mapped[r]; ok {
			want = low
		}
		if got := ToLowerJS(string(r)); got != want {
			t.Fatalf("ToLowerJS(U+%04X) = % X, want % X", r, []byte(got), []byte(want))
		}
	}
}

func TestToLowerJSSequences(t *testing.T) {
	file, err := os.Open("testdata/lower_sequences.txt")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	rows := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			t.Fatalf("bad fixture row %q", line)
		}
		input := decodeHex(t, parts[0])
		want := decodeHex(t, parts[1])
		if got := ToLowerJS(input); got != want {
			t.Fatalf("ToLowerJS(% X) = % X, want % X", []byte(input), []byte(got), []byte(want))
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if rows < 1000 {
		t.Fatalf("fixture has %d rows, want the full generated set", rows)
	}
}

// TestToLowerJSInvalidUTF8 pins Go-side behavior for bytes JavaScript strings
// cannot contain: invalid sequences become U+FFFD exactly like strings.ToLower.
func TestToLowerJSInvalidUTF8(t *testing.T) {
	for _, input := range []string{"\xff", "A\xff", "\xe2\x82", "\xc3", "A\xffz"} {
		if got, want := ToLowerJS(input), strings.ToLower(input); got != want {
			t.Fatalf("ToLowerJS(% X) = % X, want % X", []byte(input), []byte(got), []byte(want))
		}
	}
}

// TestToLowerJSPreservesLength pins the fast path and that ASCII never grows.
func TestToLowerJSPreservesLength(t *testing.T) {
	if got := ToLowerJS(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := ToLowerJS("CAVE_Tools-Read"); got != "cave_tools-read" {
		t.Fatalf("ascii = %q", got)
	}
	// U+0130 is the only code point whose lowercase mapping is longer than one
	// rune, so all other inputs keep their rune count.
	for _, input := range []string{"Σ", "Ɤ", "𐵐", "AΣB"} {
		if got, want := utf8.RuneCountInString(ToLowerJS(input)), utf8.RuneCountInString(input); got != want {
			t.Fatalf("ToLowerJS(%q) rune count = %d, want %d", input, got, want)
		}
	}
}

func readLowerMap(t *testing.T, path string) map[rune]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()
	mapped := make(map[rune]string, 2048)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cpText, valueText, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("bad fixture row %q", line)
		}
		cp, err := strconv.ParseInt(cpText, 16, 32)
		if err != nil {
			t.Fatalf("bad code point in %q: %v", line, err)
		}
		mapped[rune(cp)] = decodeHex(t, valueText)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return mapped
}

func decodeHex(t *testing.T, value string) string {
	t.Helper()
	if value == "" {
		return ""
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("bad hex %q: %v", value, err)
	}
	return string(decoded)
}
