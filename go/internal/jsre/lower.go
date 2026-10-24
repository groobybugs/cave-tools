package jsre

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ToLowerJS mirrors String.prototype.toLowerCase.
//
// Go's strings.ToLower differs from JavaScript's mapping in three measured
// ways (node 24.19.0, the TS reference runtime, against Go 1.26.3):
//
//   - U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE is an unconditional
//     SpecialCasing entry: JavaScript maps it to U+0069 U+0307 ("i̇", two
//     UTF-16 code units), Go maps it to a plain "i".
//   - The Final_Sigma rule maps U+03A3 to U+03C2 at the end of a word: a cased
//     letter precedes it (skipping case-ignorable characters) and no cased
//     letter follows. Go always maps it to U+03C3.
//   - Go's unicode tables are Unicode 15.0.0 while node's ICU is Unicode 17.0,
//     so newer case pairs and newer Cased / Case_Ignorable classifications are
//     missing. The measured delta lives in lower_unicode17.go.
//
// ASCII input is unaffected by all three and takes the strings.ToLower fast
// path.
func ToLowerJS(s string) string {
	if isASCIIString(s) {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var out strings.Builder
	out.Grow(len(s))
	for i, r := range runes {
		switch r {
		case '\u0130':
			out.WriteRune('\u0069')
			out.WriteRune('\u0307')
		case '\u03a3':
			if isFinalSigmaJS(runes, i) {
				out.WriteRune('\u03c2')
			} else {
				out.WriteRune('\u03c3')
			}
		default:
			if mapped, ok := nodeExtraLower[r]; ok {
				out.WriteRune(mapped)
				continue
			}
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

// isFinalSigmaJS reports whether the sigma at runes[i] satisfies the
// Final_Sigma condition of SpecialCasing.txt: preceded by a cased letter with
// only case-ignorable characters in between, and not followed by a cased
// letter with only case-ignorable characters in between.
func isFinalSigmaJS(runes []rune, i int) bool {
	casedBefore := false
	for j := i - 1; j >= 0; j-- {
		if isCaseIgnorableJS(runes[j]) {
			continue
		}
		casedBefore = isCasedJS(runes[j])
		break
	}
	if !casedBefore {
		return false
	}
	for j := i + 1; j < len(runes); j++ {
		if isCaseIgnorableJS(runes[j]) {
			continue
		}
		return !isCasedJS(runes[j])
	}
	return true
}

// isCasedJS reports the Unicode Cased property as node's ICU reports it.
func isCasedJS(r rune) bool {
	for _, missing := range nodeMissingCased {
		if r == missing {
			return false
		}
	}
	for _, span := range nodeExtraCased {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Uppercase, r) || unicode.Is(unicode.Other_Lowercase, r)
}

// isCaseIgnorableJS reports the Unicode Case_Ignorable property as node's ICU
// reports it: Mn, Me, Cf, Lm, Sk plus the sixteen punctuation characters in
// DerivedCoreProperties.txt.
func isCaseIgnorableJS(r rune) bool {
	for _, missing := range nodeNotCaseIgnorable {
		if r == missing {
			return false
		}
	}
	for _, span := range nodeExtraCaseIgnorable {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	switch r {
	case '\u0027', '\u002e', '\u003a', '\u00b7', '\u0387', '\u05f4',
		'\u2018', '\u2019', '\u2024', '\u2027', '\ufe13', '\ufe52',
		'\ufe55', '\uff07', '\uff0e', '\uff1a':
		return true
	}
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) ||
		unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Lm, r) ||
		unicode.Is(unicode.Sk, r)
}

// isASCIIString reports whether every byte is ASCII.
func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
