// JS value coercions for MCP tool arguments.
//
// The TS handler reads its arguments through JavaScript coercion
// (`String(args.pattern)`, `Math.max(1, args.limit)`, `args.path ? ...`). MCP
// passes JSON, so the values can be strings, numbers, booleans, null, arrays or
// objects. These helpers reproduce the ECMAScript results so output and error
// messages stay byte-identical.
//
// Duplicated per tool package: the shared JS layer (go/internal/tools,
// go/internal/jsre) has no argument-coercion helpers yet.
package grep

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// decimalLiteralRE is the JS StrDecimalLiteral grammar; strconv.ParseFloat is
// looser (it accepts "inf", "nan", hex floats and underscores).
var decimalLiteralRE = jsre.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`, "")

// jsString mirrors the ECMAScript ToString abstract operation for values that
// can appear in JSON arguments.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(v)
	case float32:
		return jsNumberString(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return string(v)
		}
		return jsNumberString(number)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item == nil {
				continue
			}
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(v, ",")
	default:
		return "[object Object]"
	}
}

// jsNumber mirrors the ECMAScript ToNumber abstract operation.
func jsNumber(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return math.NaN()
		}
		return number
	case string:
		return stringToNumber(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case []any:
		if len(v) == 0 {
			return 0
		}
		return jsNumber(jsString(v))
	default:
		return math.NaN()
	}
}

// stringToNumber mirrors Number(string): JS whitespace trim, empty string 0,
// Infinity spellings, unsigned 0x/0b/0o prefixes and decimal/exponent syntax.
func stringToNumber(value string) float64 {
	s := jsre.Trim(value)
	if s == "" {
		return 0
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		number, err := strconv.ParseUint(s[2:], 16, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(number)
	}
	if strings.HasPrefix(s, "0b") || strings.HasPrefix(s, "0B") {
		number, err := strconv.ParseUint(s[2:], 2, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(number)
	}
	if strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0O") {
		number, err := strconv.ParseUint(s[2:], 8, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(number)
	}
	if !decimalLiteralRE.MatchString(s) {
		return math.NaN()
	}
	number, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if rangeErr, ok := err.(*strconv.NumError); ok && rangeErr.Err == strconv.ErrRange {
			return number
		}
		return math.NaN()
	}
	return number
}

// numberValue reports whether value is a JS number (not a numeric string) and
// returns it. Used for `typeof args.context === "number"`.
func numberValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

// jsTruthy mirrors ECMAScript ToBoolean.
func jsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	case float32:
		return v != 0 && !math.IsNaN(float64(v))
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		return err != nil || (number != 0 && !math.IsNaN(number))
	case []any:
		return true
	case []string:
		return true
	default:
		return true
	}
}

// jsNumberString mirrors ECMAScript Number::toString(10) closely enough for
// the values tool arguments carry.
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
	abs := math.Abs(value)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	formatted := strconv.FormatFloat(value, 'e', -1, 64)
	if index := strings.IndexByte(formatted, 'e'); index >= 0 {
		mantissa, exponent := formatted[:index], formatted[index+1:]
		exponentSign := ""
		if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
			exponentSign, exponent = exponent[:1], exponent[1:]
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent == "" {
			exponent = "0"
		}
		formatted = mantissa + "e" + exponentSign + exponent
	}
	return formatted
}
