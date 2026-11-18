package arr

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	py "github.com/Nexxed/Varken/internal/pycompat"
)

// TimeleftSeconds converts an *arr timeleft string ("HH:MM:SS" or "D.HH:MM:SS") to seconds, like arr.timeleft_seconds.
// ok is false when the value is absent or unparseable. Python raised OverflowError for an infinite seconds part
// ("00:00:inf"), which killed the job; that is also reported as not ok here.
func TimeleftSeconds(v py.Value) (int64, bool) {
	s, isStr := v.AsString()
	if !v.Truthy() || !isStr {
		return 0, false
	}
	var days int64
	first, _, _ := strings.Cut(s, ":")
	if strings.Contains(first, ".") {
		dayPart, rest, _ := strings.Cut(s, ".")
		d, err := py.ParseInt(dayPart)
		if err != nil {
			return 0, false
		}
		days, s = d, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, err1 := py.ParseInt(parts[0])
	minutes, err2 := py.ParseInt(parts[1])
	secondsF, err3 := py.ParseFloat(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	seconds, err := py.TruncFloat(secondsF)
	if err != nil {
		return 0, false
	}
	return days*86400 + hours*3600 + minutes*60 + seconds, true
}

// DurationSeconds converts a .NET TimeSpan string ("HH:MM:SS.fffffff" or "D.HH:MM:SS[.fffffff]") to float seconds
// rounded to milliseconds, like arr_history.duration_seconds (0.0 when absent or unparseable).
func DurationSeconds(v py.Value) float64 {
	s, isStr := v.AsString()
	if !v.Truthy() || !isStr {
		return 0
	}
	var days int64
	first, rest, _ := strings.Cut(s, ":")
	if strings.Contains(first, ".") {
		dayPart, hourPart, _ := strings.Cut(first, ".")
		d, err := py.ParseInt(dayPart)
		if err != nil {
			return 0
		}
		days, first = d, hourPart
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 2 {
		return 0
	}
	hours, err1 := py.ParseInt(first)
	minutes, err2 := py.ParseInt(parts[0])
	seconds, err3 := py.ParseFloat(parts[1])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return py.Round(float64(days*86400+hours*3600+minutes*60)+seconds, 3)
}

// ParseArrDate parses an *arr ISO 8601 timestamp to UTC, like arr_history.parse_arr_date: not ok when absent,
// unparseable, or before 1971 (the 0001-01-01 sentinel).
//
// It keeps a quirk of the Python version: when the text has a fraction, every digit after the '.' (including
// those of a UTC offset) is gathered into the fraction, so "…00.5+02:00" loses its offset. The *arr APIs write
// UTC ("Z") timestamps, where this makes no difference.
func ParseArrDate(v py.Value) (time.Time, bool) {
	s, isStr := v.AsString()
	if !isStr || s == "" {
		return time.Time{}, false
	}
	text := strings.ReplaceAll(py.Strip(s), "Z", "+00:00")
	if head, rest, found := strings.Cut(text, "."); found {
		var digits []rune
		for _, r := range rest {
			if isPyDigit(r) {
				digits = append(digits, r)
			}
		}
		tz := string([]rune(rest)[len(digits):])
		fraction := digits
		if len(fraction) > 6 {
			fraction = fraction[:6]
		}
		text = head + "." + string(fraction) + strings.Repeat("0", 6-len(fraction)) + tz
	}
	t, year, ok := fromISOFormat(text)
	if !ok || year < 1971 { // the year as written, before conversion to UTC
		return time.Time{}, false
	}
	return t, true
}

// isPyDigit approximates str.isdigit() (decimal digits; Python also counts a few superscripts).
func isPyDigit(r rune) bool {
	return ('0' <= r && r <= '9') || (r > 0x7f && unicode.IsDigit(r))
}

func digitsAt(s string, i, n int) (int, bool) {
	if i+n > len(s) {
		return 0, false
	}
	v := 0
	for j := i; j < i+n; j++ {
		if s[j] < '0' || s[j] > '9' {
			return 0, false
		}
		v = v*10 + int(s[j]-'0')
	}
	return v, true
}

// parseISODate reads an ISO date at the start of s (YYYY-MM-DD, YYYYMMDD, YYYY-Www[-D] or YYYYWww[D]) and
// returns it with the number of bytes used.
func parseISODate(s string) (time.Time, int, bool) {
	year, ok := digitsAt(s, 0, 4)
	if !ok || year < 1 {
		return time.Time{}, 0, false
	}
	pos := 4
	extended := pos < len(s) && s[pos] == '-'
	if extended {
		pos++
	}
	if pos < len(s) && s[pos] == 'W' {
		week, ok := digitsAt(s, pos+1, 2)
		if !ok {
			return time.Time{}, 0, false
		}
		pos += 3
		day := 1
		if extended && pos < len(s) && s[pos] == '-' {
			if day, ok = digitsAt(s, pos+1, 1); !ok {
				return time.Time{}, 0, false
			}
			pos += 2
		} else if !extended && pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
			day = int(s[pos] - '0')
			pos++
		}
		if week < 1 || week > 53 || day < 1 || day > 7 {
			return time.Time{}, 0, false
		}
		// ISO week 1 contains January 4th.
		jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
		monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
		d := monday.AddDate(0, 0, (week-1)*7+day-1)
		if isoYear, _ := d.ISOWeek(); isoYear != year {
			return time.Time{}, 0, false // week 53 of a year that has 52
		}
		return d, pos, true
	}
	month, ok := digitsAt(s, pos, 2)
	if !ok {
		return time.Time{}, 0, false
	}
	pos += 2
	if extended {
		if pos >= len(s) || s[pos] != '-' {
			return time.Time{}, 0, false
		}
		pos++
	}
	day, ok := digitsAt(s, pos, 2)
	if !ok {
		return time.Time{}, 0, false
	}
	pos += 2
	if month < 1 || month > 12 || day < 1 || day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return time.Time{}, 0, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), pos, true
}

// DateFromISOFormat is Python's date.fromisoformat (3.13): the text must be 7, 8 or 10 characters long.
func DateFromISOFormat(s string) (time.Time, bool) {
	switch utf8.RuneCountInString(s) {
	case 7, 8, 10:
	default:
		return time.Time{}, false
	}
	d, _, ok := parseISODate(s)
	return d, ok
}

// dateLength is how many bytes of s datetime.fromisoformat treats as the date.
func dateLength(s string) int {
	if len(s) > 4 && s[4] == '-' {
		if len(s) > 5 && s[5] == 'W' {
			if len(s) > 8 && s[8] == '-' {
				return 10
			}
			return 8
		}
		return 10
	}
	if len(s) > 4 && s[4] == 'W' {
		if len(s) > 7 && s[7] >= '0' && s[7] <= '9' {
			return 8
		}
		return 7
	}
	return 8
}

// FromISOFormat is Python's datetime.fromisoformat (3.13) for the forms timestamps take, returned in UTC. A
// naive time is taken as UTC.
func FromISOFormat(s string) (time.Time, bool) {
	t, _, ok := fromISOFormat(s)
	return t, ok
}

// fromISOFormat also returns the year as written (before any offset is applied).
func fromISOFormat(s string) (time.Time, int, bool) {
	if len(s) < 7 {
		return time.Time{}, 0, false
	}
	n := dateLength(s)
	if len(s) < n {
		return time.Time{}, 0, false
	}
	d, used, ok := parseISODate(s[:n])
	if !ok || used != n {
		return time.Time{}, 0, false
	}
	if len(s) == n {
		return d, d.Year(), true
	}
	_, size := utf8.DecodeRuneInString(s[n:]) // any single separator character
	rest := s[n+size:]
	if rest == "" {
		return time.Time{}, 0, false
	}
	clock := rest
	zone := ""
	if i := strings.IndexAny(rest, "+-Z"); i >= 0 {
		clock, zone = rest[:i], rest[i:]
	}
	hour, minute, second, micro, ok := parseClock(clock)
	if !ok {
		return time.Time{}, 0, false
	}
	t := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, second, micro*1000, time.UTC)
	if zone != "" {
		offset, ok := parseOffset(zone)
		if !ok {
			return time.Time{}, 0, false
		}
		t = t.Add(-offset)
	}
	return t, d.Year(), true
}

// parseClock reads HH[:MM[:SS[(.|,)f+]]] or the basic HH[MM[SS[(.|,)f+]]].
func parseClock(s string) (hour, minute, second, micro int, ok bool) {
	values := [3]int{}
	pos, count := 0, 0
	extended := len(s) > 2 && s[2] == ':'
	for count < 3 {
		if count > 0 {
			if pos >= len(s) || s[pos] == '.' || s[pos] == ',' {
				break
			}
			if extended {
				if s[pos] != ':' {
					return 0, 0, 0, 0, false
				}
				pos++
			}
		}
		v, okDigits := digitsAt(s, pos, 2)
		if !okDigits {
			return 0, 0, 0, 0, false
		}
		values[count] = v
		pos += 2
		count++
	}
	if pos < len(s) && (s[pos] == '.' || s[pos] == ',') {
		if count < 3 {
			return 0, 0, 0, 0, false
		}
		pos++
		start := pos
		for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
			pos++
		}
		if pos == start {
			return 0, 0, 0, 0, false
		}
		frac := s[start:pos]
		if len(frac) > 6 {
			frac = frac[:6]
		}
		frac += strings.Repeat("0", 6-len(frac))
		micro, _ = digitsAt(frac, 0, 6)
	}
	if pos != len(s) {
		return 0, 0, 0, 0, false
	}
	hour, minute, second = values[0], values[1], values[2]
	if hour > 23 || minute > 59 || second > 59 {
		return 0, 0, 0, 0, false
	}
	return hour, minute, second, micro, true
}

// parseOffset reads a UTC offset: Z, or ±HH[:MM[:SS[.ffffff]]] (or the basic form), under 24 hours.
func parseOffset(s string) (time.Duration, bool) {
	if s == "Z" {
		return 0, true
	}
	sign := time.Duration(1)
	switch s[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	h, m, sec, micro, ok := parseClock(s[1:])
	if !ok {
		return 0, false
	}
	return sign * (time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second +
		time.Duration(micro)*time.Microsecond), true
}

// quality is the {"quality": {"name": ...}} object of a queue item or file.
type quality struct {
	Quality py.Obj[struct {
		Name       py.Value `json:"name"`
		Resolution py.Value `json:"resolution"`
	}] `json:"quality"`
}

// QualityName is arr.quality_name: quality['quality']['name'], or "Unknown" when that lookup fails. A present but
// null name stays None (the tag is then left out), as in Python.
func QualityName(q py.Obj[quality]) py.Value {
	inner := q.V().Quality
	if !q.OK() || !inner.OK() || !inner.V().Name.Exists() {
		return py.String("Unknown")
	}
	return inner.V().Name
}
