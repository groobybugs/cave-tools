package compress

import (
	"math"
	"testing"
)

// TestJSNumberToStringSpecials pins ECMAScript Number::toString for the values
// that are not finite. Infinity must not collapse to NaN.
func TestJSNumberToStringSpecials(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  string
	}{
		{"NaN", math.NaN(), "NaN"},
		{"positive infinity", math.Inf(1), "Infinity"},
		{"negative infinity", math.Inf(-1), "-Infinity"},
		{"positive zero", 0, "0"},
		{"negative zero", math.Copysign(0, -1), "0"},
		{"integer", 42, "42"},
		{"fraction", 0.5, "0.5"},
		{"large", 1e21, "1e+21"},
		{"small", 1e-7, "1e-7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := jsNumberToString(test.value); got != test.want {
				t.Fatalf("jsNumberToString(%v) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}
