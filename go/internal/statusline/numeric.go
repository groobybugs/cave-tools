package statusline

import (
	"math"
	"math/big"
	"strconv"
)

// JsToFixed mirrors Number.prototype.toFixed, including its round-half-away
// rule: Go's strconv rounds half to even, which would turn 14.5k into "14k".
// port of src/status-ui.ts:jsToFixed
func JsToFixed(x float64, digits int) string {
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.IsInf(x, 1) {
		return "Infinity"
	}
	if math.IsInf(x, -1) {
		return "-Infinity"
	}
	if math.Abs(x) >= 1e21 {
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	neg := x < 0
	abs := math.Abs(x)
	rat := new(big.Rat).SetFloat64(abs)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	rat.Mul(rat, new(big.Rat).SetInt(scale))
	q, rem := new(big.Int).QuoRem(rat.Num(), rat.Denom(), new(big.Int))
	rem.Abs(rem)
	rem.Lsh(rem, 1)
	if rem.Cmp(rat.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	out := q.String()
	if digits > 0 {
		for len(out) <= digits {
			out = "0" + out
		}
		out = out[:len(out)-digits] + "." + out[len(out)-digits:]
	}
	if neg {
		return "-" + out
	}
	return out
}
