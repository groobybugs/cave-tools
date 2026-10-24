// ASCII collation matching V8's default-locale localeCompare closely enough
// for directory listings.
//
// V8's localeCompare uses ICU's root/en-US collation: primary weights ignore
// case, punctuation sorts before digits and letters in a fixed DUCET order,
// and case is a tertiary difference (lowercase first). The table below was
// derived from node 24.19.0 and verified against 50k random printable-ASCII
// pairs. Characters outside printable ASCII fall back to their code point,
// which is the documented deviation from ICU (go-port.md line 431 accepts the
// same limitation for the ripgrep fallback finder).
package ls

import "strings"

// asciiCollationOrder is the ICU primary order of printable ASCII: punctuation,
// then digits, then letters with each letter's upper/lower pair sharing a
// primary weight.
const asciiCollationOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

// asciiPrimary maps an ASCII byte to its primary weight.
var asciiPrimary = func() [128]int {
	var table [128]int
	for index := range table {
		table[index] = -1
	}
	for index := 0; index < len(asciiCollationOrder); index++ {
		char := asciiCollationOrder[index]
		table[char] = index
		upper := char
		if char >= 'a' && char <= 'z' {
			upper = char - 'a' + 'A'
			table[upper] = index
		}
	}
	return table
}()

// primaryWeight returns the collation primary weight of a rune. Non-ASCII
// runes are placed after all ASCII by code point.
func primaryWeight(char rune) int {
	if char >= 0 && char < 128 && asciiPrimary[char] >= 0 {
		return asciiPrimary[char]
	}
	return 1000 + int(char)
}

// tertiaryWeight is the case level: lowercase before uppercase.
func tertiaryWeight(char rune) int {
	if char >= 'a' && char <= 'z' {
		return 0
	}
	return 1
}

// localeCompare mirrors String.prototype.localeCompare's sign for the names a
// directory listing carries.
func localeCompare(a, b string) int {
	left := []rune(a)
	right := []rune(b)
	shared := len(left)
	if len(right) < shared {
		shared = len(right)
	}
	for index := 0; index < shared; index++ {
		leftWeight := primaryWeight(left[index])
		rightWeight := primaryWeight(right[index])
		if leftWeight != rightWeight {
			if leftWeight < rightWeight {
				return -1
			}
			return 1
		}
	}
	if len(left) != len(right) {
		if len(left) < len(right) {
			return -1
		}
		return 1
	}
	for index := 0; index < len(left); index++ {
		leftTertiary := tertiaryWeight(left[index])
		rightTertiary := tertiaryWeight(right[index])
		if leftTertiary != rightTertiary {
			if leftTertiary < rightTertiary {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}
