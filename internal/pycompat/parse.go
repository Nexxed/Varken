package pycompat

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

// ErrValue and ErrType mirror the Python exceptions the original code caught (ValueError, TypeError).
// ErrOverflow is OverflowError, which the original code usually did not catch.
var (
	ErrValue    = errors.New("ValueError")
	ErrType     = errors.New("TypeError")
	ErrOverflow = errors.New("OverflowError")
)

// isPySpace is str.isspace(), which is what float(), int() and str.strip() strip.
func isPySpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85:
		return true
	}
	return r > 0x7f && (unicode.Is(unicode.White_Space, r) || unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp)) &&
		r != 0x180e && r != 0x200b
}

// Strip is Python's str.strip() with no arguments.
func Strip(s string) string { return strings.TrimFunc(s, isPySpace) }

// asciiDigits replaces Unicode decimal digits (category Nd) with ASCII ones, as int() and float() accept them.
func asciiDigits(s string) (string, bool) {
	if isASCII(s) {
		return s, true
	}
	var b strings.Builder
	for _, r := range s {
		if r > 0x7f && unicode.Is(unicode.Nd, r) {
			b.WriteByte(byte('0' + digitValue(r)))
			continue
		}
		if r > 0x7f {
			return "", false
		}
		b.WriteRune(r)
	}
	return b.String(), true
}

// digitValue relies on every Nd block being a complete, contiguous 0-9 run.
func digitValue(r rune) int {
	for _, rng := range unicode.Nd.R16 {
		if rune(rng.Lo) <= r && r <= rune(rng.Hi) {
			return int(r-rune(rng.Lo)) % 10
		}
	}
	for _, rng := range unicode.Nd.R32 {
		if rune(rng.Lo) <= r && r <= rune(rng.Hi) {
			return int(r-rune(rng.Lo)) % 10
		}
	}
	return 0
}

// removeUnderscores accepts single underscores between digits only, as Python's numeric literals do.
func removeUnderscores(s string) (string, bool) {
	if !strings.Contains(s, "_") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '_' {
			if i == 0 || i == len(s)-1 || !isDigit(s[i-1]) || !isDigit(s[i+1]) {
				return "", false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String(), true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// ParseFloat is Python's float(str).
func ParseFloat(s string) (float64, error) {
	text, ok := asciiDigits(Strip(s))
	if !ok || text == "" {
		return 0, ErrValue
	}
	lower := strings.ToLower(text)
	body := strings.TrimLeft(lower, "+-")
	if len(lower)-len(body) > 1 {
		return 0, ErrValue
	}
	negative := strings.HasPrefix(lower, "-")
	switch body {
	case "inf", "infinity":
		if negative {
			return math.Inf(-1), nil
		}
		return math.Inf(1), nil
	case "nan":
		return math.NaN(), nil
	}
	if !validDecimal(body) {
		return 0, ErrValue
	}
	clean, ok := removeUnderscores(text)
	if !ok {
		return 0, ErrValue
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil && !isRangeErr(err) {
		return 0, ErrValue
	}
	return f, nil
}

// validDecimal checks digits[.digits][e[sign]digits] (with optional underscores) and nothing else, which keeps
// hex, "0x", "1,5" and Go-only syntax out.
func validDecimal(s string) bool {
	mantissa, exponent, hasExp := strings.Cut(s, "e")
	if hasExp {
		exponent = strings.TrimPrefix(strings.TrimPrefix(exponent, "+"), "-")
		if !allDigits(exponent) {
			return false
		}
	}
	whole, frac, hasPoint := strings.Cut(mantissa, ".")
	if !hasPoint {
		return allDigits(whole)
	}
	if whole == "" && frac == "" {
		return false
	}
	return (whole == "" || allDigits(whole)) && (frac == "" || allDigits(frac))
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) && s[i] != '_' {
			return false
		}
	}
	return true
}

// ParseInt is Python's int(str) (base 10), limited to int64.
func ParseInt(s string) (int64, error) {
	text, ok := asciiDigits(Strip(s))
	if !ok || text == "" {
		return 0, ErrValue
	}
	sign := ""
	if text[0] == '+' || text[0] == '-' {
		sign, text = text[:1], text[1:]
	}
	if !allDigits(text) {
		return 0, ErrValue
	}
	clean, ok := removeUnderscores(text)
	if !ok {
		return 0, ErrValue
	}
	n, err := strconv.ParseInt(sign+clean, 10, 64)
	if err != nil {
		return 0, ErrOverflow
	}
	return n, nil
}

// ToFloat is Python's float(value).
func ToFloat(v Value) (float64, error) {
	switch v.kind {
	case BoolKind:
		if v.b {
			return 1, nil
		}
		return 0, nil
	case IntKind:
		if v.s != "" {
			f, _ := new(big.Float).SetPrec(53).SetMode(big.ToNearestEven).SetString(v.s)
			if f == nil {
				return 0, ErrOverflow
			}
			out, _ := f.Float64()
			if math.IsInf(out, 0) {
				return 0, ErrOverflow
			}
			return out, nil
		}
		return float64(v.i), nil
	case FloatKind:
		return v.f, nil
	case StrKind:
		return ParseFloat(v.s)
	}
	return 0, ErrType
}

// TruncFloat is Python's int(f) for a float, limited to int64 (saturating).
func TruncFloat(f float64) (int64, error) {
	switch {
	case math.IsNaN(f):
		return 0, ErrValue
	case math.IsInf(f, 0):
		return 0, ErrOverflow
	case f >= math.MaxInt64:
		return math.MaxInt64, nil
	case f <= math.MinInt64:
		return math.MinInt64, nil
	}
	return int64(f), nil
}

// IntOfFloat is int(float(value)), the conversion both the arr helpers and the Tautulli helpers use.
func IntOfFloat(v Value) (int64, error) {
	f, err := ToFloat(v)
	if err != nil {
		return 0, err
	}
	return TruncFloat(f)
}

// ToInt is arr.to_int / tautulli._num with cast=int: int(float(value)), or def when that fails. Python let
// OverflowError (from "inf") escape and kill the job; here it also gives def.
func ToInt(v Value, def int64) int64 {
	n, err := IntOfFloat(v)
	if err != nil {
		return def
	}
	return n
}

// ToFloatOr is tautulli._num with cast=float: float(float(value)), or def when that fails.
func ToFloatOr(v Value, def float64) float64 {
	f, err := ToFloat(v)
	if err != nil {
		return def
	}
	return f
}

// IntExact is Python's int(value) for an int, bool, float (truncating) or numeric string (int(str) rules).
func IntExact(v Value) (int64, error) {
	switch v.kind {
	case BoolKind, FloatKind:
		return IntOfFloat(v)
	case IntKind:
		return v.i, nil
	case StrKind:
		return ParseInt(v.s)
	}
	return 0, ErrType
}

// IsSpace is str.isspace() for one character.
func IsSpace(r rune) bool { return isPySpace(r) }
