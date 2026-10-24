package jsjson

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

// Parse decodes JSON with JSON.parse's acceptance rules: numbers keep their
// float64 semantics (UseNumber defers the conversion so an out-of-range
// literal cannot fail the decode), and, like JSON.parse/json.Unmarshal, exactly
// one top-level value is allowed.
//
// V8 never throws on magnitude: overflow becomes ±Inf and underflow becomes
// ±0, so strconv.ErrRange is the expected result, not a failure.
func Parse(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("unexpected data after top-level value")
		}
		return nil, err
	}
	return convertNumbers(value), nil
}

// StringifyValue rewrites a decoded value so encoding/json emits the text
// JSON.stringify writes: NaN and ±Infinity become null. Maps and slices are
// copied, never mutated, so callers can keep the original value for an
// in-process fallback (JSON.stringify does not change its input either).
func StringifyValue(value any) any {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil
		}
		return typed
	case json.Number:
		number := numberValue(typed)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil
		}
		return number
	case []any:
		copied := make([]any, len(typed))
		for i := range typed {
			copied[i] = StringifyValue(typed[i])
		}
		return copied
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, item := range typed {
			copied[key] = StringifyValue(item)
		}
		return copied
	default:
		return value
	}
}

// convertNumbers rewrites every json.Number in a decoded document to the
// float64 JSON.parse would have produced.
func convertNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return numberValue(typed)
	case []any:
		for i := range typed {
			typed[i] = convertNumbers(typed[i])
		}
		return typed
	case map[string]any:
		for key := range typed {
			typed[key] = convertNumbers(typed[key])
		}
		return typed
	default:
		return value
	}
}

// numberValue converts one JSON number token to the float64 JSON.parse yields.
func numberValue(number json.Number) float64 {
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && numErr.Err == strconv.ErrRange {
			return value
		}
		return 0
	}
	return value
}
