package pycompat

import (
	"bytes"
	"crypto/md5" //nolint:gosec // md5 is the hash Varken has always written; it is an identifier, not security.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// FloatRepr is Python's repr(float): the shortest digits that round-trip, written in fixed notation unless the
// decimal exponent is below -4 or at least 16, and always with a fractional part in fixed notation.
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // e.g. "-1.2345e+06"
	sign := ""
	if sci[0] == '-' {
		sign = "-"
		sci = sci[1:]
	}
	mantissa, expText, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mantissa, ".", "", 1)
	if exp < -4 || exp >= 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		expSign := "+"
		if exp < 0 {
			expSign = "-"
			exp = -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, out, expSign, exp)
	}
	point := exp + 1 // digits before the decimal point
	switch {
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}

// Round is Python's round(x, ndigits) for floats: correctly rounded, ties to even on the exact binary value.
func Round(x float64, ndigits int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) || x == 0 {
		return x
	}
	if ndigits >= 0 {
		text := strconv.FormatFloat(x, 'f', ndigits, 64)
		out, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return x
		}
		if out == 0 {
			return math.Copysign(0, x)
		}
		return out
	}
	pow := math.Pow(10, float64(-ndigits))
	y := x / pow
	r := math.RoundToEven(y)
	return math.Copysign(r*pow, x)
}

// FormatPad is Python's format(value, '0>N') for the value types the collectors pass (int, float, str, bool).
// It fails like Python does for None.
func FormatPad(v Value, width int) (string, error) {
	var text string
	switch v.kind {
	case AbsentKind, NullKind:
		return "", fmt.Errorf("TypeError: unsupported format string passed to NoneType.__format__")
	case BoolKind:
		text = "0"
		if v.b {
			text = "1"
		}
	case ListKind, DictKind:
		return "", fmt.Errorf("TypeError: unsupported format string passed to %s.__format__", v.typeName())
	default:
		text = v.String()
	}
	if n := utf8.RuneCountInString(text); n < width {
		text = strings.Repeat("0", width-n) + text
	}
	return text, nil
}

func (v Value) typeName() string {
	return [...]string{"NoneType", "NoneType", "bool", "int", "float", "str", "list", "dict"}[v.kind]
}

// Truncate is Python's text[:n] (by code point).
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// RuneLen is Python's len() of a str.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

// Hash is Varken's hashit(): the md5 hex digest of the UTF-8 text.
func Hash(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// IsoFormat is Python's datetime.isoformat() for an aware datetime: microseconds only when non-zero, and the
// UTC offset as +HH:MM (with :SS when it has seconds).
func IsoFormat(t time.Time) string {
	var b strings.Builder
	b.WriteString(t.Format("2006-01-02T15:04:05"))
	if us := t.Nanosecond() / 1000; us != 0 {
		fmt.Fprintf(&b, ".%06d", us)
	}
	_, offset := t.Zone()
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	fmt.Fprintf(&b, "%c%02d:%02d", sign, offset/3600, offset%3600/60)
	if s := offset % 60; s != 0 {
		fmt.Fprintf(&b, ":%02d", s)
	}
	return b.String()
}

// TruncateToMicro drops sub-microsecond precision, which Python datetimes do not have.
func TruncateToMicro(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

// ReprString is Python's repr(str).
func ReprString(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r > 0x7f && !isPrintable(r):
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// isPrintable approximates str.isprintable(): everything but categories Cc, Cf, Cs, Co, Cn, Zl, Zp and Zs (other
// than the ASCII space).
func isPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zl, unicode.Zp, unicode.Zs) {
		return false
	}
	return unicode.IsGraphic(r) || unicode.IsMark(r)
}

// reprJSON prints a JSON list or object the way Python prints the list/dict json.loads would return.
func reprJSON(raw string) string {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var b strings.Builder
	if err := writeRepr(dec, &b); err != nil {
		return raw
	}
	return b.String()
}

func writeRepr(dec *json.Decoder, b *strings.Builder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		closing := map[json.Delim]string{'[': "]", '{': "}"}[t]
		b.WriteString(string(t))
		first := true
		for dec.More() {
			if !first {
				b.WriteString(", ")
			}
			first = false
			if t == '{' {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				b.WriteString(ReprString(key.(string)))
				b.WriteString(": ")
			}
			if err := writeRepr(dec, b); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		b.WriteString(closing)
	default:
		b.WriteString(FromAny(t).Repr())
	}
	return nil
}
