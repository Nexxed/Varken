package pycompat

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// ErrTimestamp is returned for a time string the InfluxDB client's parser (dateutil) would also reject, or one
// in a format this subset does not handle.
var ErrTimestamp = errors.New("unparseable timestamp")

// ParseTimestampNS converts a point's time string to epoch nanoseconds the way the Python InfluxDB client did:
// dateutil.parser.parse, naive times taken as UTC, then truncated to microseconds.
//
// It handles the ISO 8601 forms Varken and the upstream APIs produce: a date, optionally followed by 'T' or a
// space and HH:MM[:SS[(.|,)fraction]], optionally followed (with or without a space) by Z, UTC, GMT, ±HH,
// ±HHMM or ±HH:MM. Anything else is an error, where Python either guessed or raised.
func ParseTimestampNS(s string) (int64, error) {
	t, err := ParseDateTime(s)
	if err != nil {
		return 0, err
	}
	return UnixNanoChecked(t)
}

// UnixNanoChecked is t in epoch nanoseconds (truncated to microseconds), or an error outside int64's range.
func UnixNanoChecked(t time.Time) (int64, error) {
	t = TruncateToMicro(t)
	sec := new(big.Int).SetInt64(t.Unix())
	ns := sec.Mul(sec, big.NewInt(1e9))
	ns.Add(ns, big.NewInt(int64(t.Nanosecond())))
	if !ns.IsInt64() {
		return 0, ErrTimestamp
	}
	return ns.Int64(), nil
}

// ParseDateTime parses the ISO 8601 subset described on ParseTimestampNS.
func ParseDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) < 10 || s[4] != '-' || s[7] != '-' {
		return time.Time{}, ErrTimestamp
	}
	year, err1 := strconv.Atoi(s[0:4])
	month, err2 := strconv.Atoi(s[5:7])
	day, err3 := strconv.Atoi(s[8:10])
	if err1 != nil || err2 != nil || err3 != nil || !allDigits(s[0:4]) || !allDigits(s[5:7]) || !allDigits(s[8:10]) {
		return time.Time{}, ErrTimestamp
	}
	rest := s[10:]
	hour, minute, second, micro := 0, 0, 0, 0
	if rest != "" {
		if rest[0] != 'T' && rest[0] != ' ' {
			return time.Time{}, ErrTimestamp
		}
		rest = rest[1:]
		var ok bool
		hour, minute, second, micro, rest, ok = parseClock(rest)
		if !ok {
			return time.Time{}, ErrTimestamp
		}
	}
	loc, ok := parseZone(strings.TrimSpace(rest))
	if !ok {
		return time.Time{}, ErrTimestamp
	}
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, ErrTimestamp
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, loc), nil
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func twoDigits(s string) (int, bool) {
	if len(s) < 2 || !isDigit(s[0]) || !isDigit(s[1]) {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}

func parseClock(s string) (hour, minute, second, micro int, rest string, ok bool) {
	if hour, ok = twoDigits(s); !ok || len(s) < 5 || s[2] != ':' {
		return 0, 0, 0, 0, "", false
	}
	if minute, ok = twoDigits(s[3:]); !ok {
		return 0, 0, 0, 0, "", false
	}
	s = s[5:]
	if len(s) >= 3 && s[0] == ':' {
		if second, ok = twoDigits(s[1:]); !ok {
			return 0, 0, 0, 0, "", false
		}
		s = s[3:]
		if len(s) > 0 && (s[0] == '.' || s[0] == ',') {
			i := 1
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			frac := s[1:i]
			if frac == "" {
				return 0, 0, 0, 0, "", false
			}
			frac = (frac + "000000")[:6]
			micro, _ = strconv.Atoi(frac)
			s = s[i:]
		}
	}
	return hour, minute, second, micro, s, true
}

func parseZone(s string) (*time.Location, bool) {
	switch strings.ToUpper(s) {
	case "", "Z", "UTC", "GMT":
		return time.UTC, true
	}
	if s[0] != '+' && s[0] != '-' {
		return nil, false
	}
	sign := 1
	if s[0] == '-' {
		sign = -1
	}
	body := strings.Replace(s[1:], ":", "", 1)
	var hours, minutes int
	var ok bool
	switch len(body) {
	case 2:
		hours, ok = twoDigits(body)
	case 4:
		var okM bool
		hours, ok = twoDigits(body)
		minutes, okM = twoDigits(body[2:])
		ok = ok && okM
	}
	if !ok || hours > 23 || minutes > 59 {
		return nil, false
	}
	offset := sign * (hours*3600 + minutes*60)
	if offset == 0 {
		return time.UTC, true
	}
	return time.FixedZone("", offset), true
}

// SecondsSince is int(time.time() - ts) style arithmetic on float seconds.
func SecondsSince(now time.Time, ts float64) int64 {
	diff := float64(now.UnixMicro())/1e6 - ts
	if math.IsNaN(diff) || math.IsInf(diff, 0) {
		return 0
	}
	return int64(diff)
}
