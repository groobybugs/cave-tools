package jsjson

import (
	"math"
	"testing"
)

// TestParseMatchesJSONParseNumbers pins the number semantics JSON.parse has:
// out-of-range literals become ±Infinity, underflow becomes signed zero, and
// integers beyond 2^53 round like V8.
func TestParseMatchesJSONParseNumbers(t *testing.T) {
	cases := []struct {
		name string
		text string
		want any
	}{
		{"overflow", "1e400", math.Inf(1)},
		{"overflow-negative", "-1e400", math.Inf(-1)},
		{"overflow-1e309", "1e309", math.Inf(1)},
		{"underflow", "1e-400", 0.0},
		{"underflow-negative", "-1e-400", math.Copysign(0, -1)},
		{"negative-zero", "-0", math.Copysign(0, -1)},
		{"fraction", "1.5", 1.5},
		{"bigint-rounds", "9007199254740993", float64(9007199254740992)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.text)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.text, err)
			}
			number, ok := got.(float64)
			if !ok {
				t.Fatalf("Parse(%q) = %#v, want float64", tc.text, got)
			}
			if math.IsNaN(tc.want.(float64)) {
				if !math.IsNaN(number) {
					t.Fatalf("Parse(%q) = %v, want NaN", tc.text, number)
				}
				return
			}
			if number != tc.want.(float64) {
				t.Fatalf("Parse(%q) = %v, want %v", tc.text, number, tc.want)
			}
			if tc.name == "underflow-negative" || tc.name == "negative-zero" {
				if !math.Signbit(number) {
					t.Fatalf("Parse(%q) = %v, want negative zero", tc.text, number)
				}
			}
		})
	}
}

func TestParseNestedNumbers(t *testing.T) {
	got, err := Parse(`{"a":[1e400,{"b":-1e400}],"c":"x"}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	root, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("Parse = %#v", got)
	}
	array, ok := root["a"].([]any)
	if !ok || len(array) != 2 {
		t.Fatalf("a = %#v", root["a"])
	}
	if value, _ := array[0].(float64); !math.IsInf(value, 1) {
		t.Fatalf("a[0] = %#v, want +Inf", array[0])
	}
	nested, _ := array[1].(map[string]any)
	if value, _ := nested["b"].(float64); !math.IsInf(value, -1) {
		t.Fatalf("a[1].b = %#v, want -Inf", nested["b"])
	}
	if root["c"] != "x" {
		t.Fatalf("c = %#v", root["c"])
	}
}

func TestParseStrictness(t *testing.T) {
	for _, text := range []string{"{} x", "{} {}", "[1e400]x", "", "01"} {
		if _, err := Parse(text); err == nil {
			t.Fatalf("Parse(%q) accepted trailing/invalid data", text)
		}
	}
}

// TestStringifyValueMatchesJSONStringify covers JSON.stringify's null for
// non-finite numbers, including nesting, and its copy (not mutate) behavior.
func TestStringifyValueMatchesJSONStringify(t *testing.T) {
	source := map[string]any{
		"inf":    math.Inf(1),
		"ninf":   math.Inf(-1),
		"nan":    math.NaN(),
		"normal": 1.5,
		"array":  []any{math.Inf(1), "x"},
	}
	got, ok := StringifyValue(source).(map[string]any)
	if !ok {
		t.Fatalf("StringifyValue = %#v", got)
	}
	if got["inf"] != nil || got["ninf"] != nil || got["nan"] != nil {
		t.Fatalf("non-finite values = %#v, want nil", got)
	}
	if got["normal"] != 1.5 {
		t.Fatalf("normal = %#v", got["normal"])
	}
	array, _ := got["array"].([]any)
	if array[0] != nil || array[1] != "x" {
		t.Fatalf("array = %#v", array)
	}
	if value, _ := source["inf"].(float64); !math.IsInf(value, 1) {
		t.Fatalf("StringifyValue mutated the source: %#v", source["inf"])
	}
}
