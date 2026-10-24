// JavaScript value semantics used by read.ts.
//
// read.ts hands several JSON values straight to JS coercions (String(x),
// Number(x) || 1, String.prototype.length, Array.prototype.slice,
// String.prototype.localeCompare). Go needs those exact semantics to keep the
// tool result byte-identical, so the small helpers below live with the tool
// rather than in a shared package (compression's equivalents are unexported).

package read

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// jsString mirrors String(value) for the JSON value shapes a tool argument can
// hold. JSON has no undefined, but a missing key reads as nil here.
func jsString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "undefined"
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(typed)
	case float32:
		return jsNumberString(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		if parsed, err := strconv.ParseFloat(string(typed), 64); err == nil {
			return jsNumberString(parsed)
		}
		return string(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			// Array.prototype.join renders null/undefined as "".
			if item == nil {
				parts = append(parts, "")
				continue
			}
			parts = append(parts, jsString(item))
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	default:
		return ""
	}
}

// jsNumber mirrors Number(value), including NaN for non-numeric strings.
func jsNumber(value any) float64 {
	switch typed := value.(type) {
	case nil:
		return math.NaN()
	case bool:
		if typed {
			return 1
		}
		return 0
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil && !isRangeError(err) {
			return math.NaN()
		}
		return parsed
	case string:
		return jsStringToNumber(typed)
	case []any:
		if len(typed) == 0 {
			return 0
		}
		return jsNumber(jsString(typed))
	default:
		return math.NaN()
	}
}

// isFalsy mirrors JS truthiness for `value || fallback`.
func isFalsy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case float64:
		return typed == 0 || math.IsNaN(typed)
	case float32:
		return typed == 0 || math.IsNaN(float64(typed))
	case int:
		return typed == 0
	case int64:
		return typed == 0
	case string:
		return typed == ""
	case []any:
		return false
	case map[string]any:
		return false
	default:
		return true
	}
}

// jsNumberString formats a float64 like ECMAScript Number::toString(10): plain
// decimal for 1e-6 <= |v| < 1e21, otherwise exponent form with no zero padding.
func jsNumberString(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		return "0"
	}
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	// Shortest round-trip digits in exponential form, then re-lay them out.
	exponential := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentText, _ := strings.Cut(exponential, "e")
	exponent, _ := strconv.Atoi(exponentText)
	digits := strings.Replace(mantissa, ".", "", 1)
	k := len(digits)
	n := exponent + 1 // value == 0.digits * 10^n

	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}

	head := digits
	if k > 1 {
		head = digits[:1] + "." + digits[1:]
	}
	if n-1 >= 0 {
		return sign + head + "e+" + strconv.Itoa(n-1)
	}
	return sign + head + "e-" + strconv.Itoa(-(n - 1))
}

// jsStringToNumber implements the StringNumericLiteral subset Number() accepts.
func jsStringToNumber(value string) float64 {
	trimmed := jsre.Trim(value)
	if trimmed == "" {
		return 0
	}
	switch trimmed {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(trimmed) > 2 && trimmed[0] == '0' {
		switch trimmed[1] {
		case 'x', 'X':
			return parseRadixFloat(trimmed[2:], 16)
		case 'b', 'B':
			return parseRadixFloat(trimmed[2:], 2)
		case 'o', 'O':
			return parseRadixFloat(trimmed[2:], 8)
		}
	}
	if !isDecimalLiteral(trimmed) {
		return math.NaN()
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil && !isRangeError(err) {
		return math.NaN()
	}
	return parsed
}

// isDecimalLiteral reports whether s is [+-]?(digits[.digits?]|.digits)([eE][+-]?digits)?.
func isDecimalLiteral(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[i] == '+' || s[i] == '-' {
		i++
	}
	digitsBefore := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		digitsBefore++
		i++
	}
	digitsAfter := 0
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			digitsAfter++
			i++
		}
	}
	if digitsBefore == 0 && digitsAfter == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exponentDigits := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			exponentDigits++
			i++
		}
		if exponentDigits == 0 {
			return false
		}
	}
	return i == len(s)
}

// parseRadixFloat accumulates 0x/0b/0o literals in float64, matching JS's
// precision loss for oversized values.
func parseRadixFloat(digits string, radix int) float64 {
	if digits == "" {
		return math.NaN()
	}
	value := 0.0
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		var digit int
		switch {
		case c >= '0' && c <= '9':
			digit = int(c - '0')
		case c >= 'a' && c <= 'f':
			digit = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			digit = int(c-'A') + 10
		default:
			return math.NaN()
		}
		if digit >= radix {
			return math.NaN()
		}
		value = value*float64(radix) + float64(digit)
	}
	return value
}

func isRangeError(err error) bool {
	return errors.Is(err, strconv.ErrRange)
}

// utf16Len counts UTF-16 code units, like String.prototype.length.
func utf16Len(value string) int {
	units := 0
	for _, r := range value {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// utf16Prefix returns the first n UTF-16 code units of value, like
// value.slice(0, n). A surrogate pair that straddles the cut is dropped: a Go
// string cannot hold the lone surrogate TS would produce there.
func utf16Prefix(value string, n int) string {
	units := 0
	for index, r := range value {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > n {
			return value[:index]
		}
		units += width
		if units == n {
			_, size := utf8.DecodeRuneInString(value[index:])
			return value[:index+size]
		}
	}
	return value
}

// collationASCII is ICU root-collation order for printable ASCII, probed from
// Node's localeCompare (see TestJsLocaleCompare). Letters fold to one primary
// weight, so this string lists them once, lowercase.
const collationASCII = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

var collationPrimaryWeights = func() map[rune]int {
	weights := make(map[rune]int, len(collationASCII))
	for index, r := range collationASCII {
		weights[r] = index
		if r >= 'a' && r <= 'z' {
			weights[unicode.ToUpper(r)] = index
		}
	}
	return weights
}()

// collationPrimary is one rune's primary weight. Non-ASCII sorts after ASCII
// by lowercase code point, an approximation of ICU's accent ordering.
func collationPrimary(r rune) int {
	if weight, ok := collationPrimaryWeights[r]; ok {
		return weight
	}
	if r < 0x20 {
		return -100 + int(r)
	}
	if r < 0x80 {
		return 200
	}
	return 1000 + int(unicode.ToLower(r))
}

// jsLocaleCompare mirrors name.localeCompare(name) for the directory listing
// sort: primary (case-folded, ICU-ordered) first, then lowercase before
// uppercase.
func jsLocaleCompare(a string, b string) int {
	left := []rune(a)
	right := []rune(b)
	common := len(left)
	if len(right) < common {
		common = len(right)
	}
	for i := 0; i < common; i++ {
		leftWeight := collationPrimary(left[i])
		rightWeight := collationPrimary(right[i])
		if leftWeight != rightWeight {
			return compareInt(leftWeight, rightWeight)
		}
	}
	if len(left) != len(right) {
		return compareInt(len(left), len(right))
	}
	for i := 0; i < common; i++ {
		if left[i] == right[i] {
			continue
		}
		if unicode.IsLower(left[i]) {
			return -1
		}
		if unicode.IsLower(right[i]) {
			return 1
		}
		return compareInt(int(left[i]), int(right[i]))
	}
	return 0
}

func compareInt(a int, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// jsSliceBounds applies Array.prototype.slice index normalization for
// slice(start, end).
func jsSliceBounds(start float64, end float64, length int) (int, int) {
	from := jsInteger(start)
	to := jsInteger(end)
	size := int64(length)
	if from < 0 {
		from += size
		if from < 0 {
			from = 0
		}
	} else if from > size {
		from = size
	}
	if to < 0 {
		to += size
		if to < 0 {
			to = 0
		}
	} else if to > size {
		to = size
	}
	if to < from {
		to = from
	}
	return int(from), int(to)
}

// jsInteger is ToIntegerOrInfinity clamped to the int64 range.
func jsInteger(value float64) int64 {
	if math.IsNaN(value) {
		return 0
	}
	value = math.Trunc(value)
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	if value < math.MinInt64 {
		return math.MinInt64
	}
	return int64(value)
}
