// JS value coercions for MCP tool arguments.
//
// The TS handler reads its arguments through JavaScript coercion
// (`String(args.x)`, `args.x ? ... : ...`). MCP passes JSON, so the values can
// be strings, numbers, booleans, null, arrays or objects. These helpers
// reproduce the ECMAScript results so output stays byte-identical.
//
// Duplicated per tool package: the shared JS layer (go/internal/tools,
// go/internal/jsre) has no argument-coercion helpers yet.
package bashstart

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

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
