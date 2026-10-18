package query

import (
	"strconv"
	"strings"
	"time"

	"github.com/samber/oops"
)

// Valid ranges, from Postgres' own limits : dates in days since 2000-01-01, timestamps
// in microseconds since 2000-01-01 00:00:00 (end exclusive).
const (
	pgEpochUnixDays = 10957 // days from 1970-01-01 to 2000-01-01

	dateMinDays   = -2451545 // 4714-11-24 BC
	dateMaxDays   = 2145031948
	dateInfinity  = 0x7fffffff
	dateNegInf    = -0x80000000
	tsMinMicros   = -211813488000000000
	tsEndMicros   = 9223371331200000000 // 294277-01-01
	tsInfinity    = 0x7fffffffffffffff
	tsNegInfinity = -0x8000000000000000

	microsPerSecond = 1000000
	microsPerDay    = 86400 * microsPerSecond
)

// daysFromCivil returns the days from 1970-01-01 to the proleptic Gregorian date
// y-m-d, y being an astronomical year (1 BC is year 0).
func daysFromCivil(y, m, d int64) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 && y%400 != 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

func isLeap(y int64) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

func daysInMonth(y, m int64) int64 {
	switch m {
	case 2:
		if isLeap(y) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// timeFields is a parsed date-time text.
type timeFields struct {
	year, month, day      int64 // astronomical year
	hour, min, sec, micro int64
	hasZone               bool
	zoneSeconds           int64 // seconds east of UTC, when hasZone
}

// digitsAt reads exactly n digits at s[i:] .
func digitsAt(s string, i, n int) (int64, bool) {
	if i+n > len(s) {
		return 0, false
	}
	var v int64
	for k := 0; k < n; k++ {
		c := s[i+k]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	return v, true
}

// parseDateTimeText reads the ISO 8601 forms of specs/typed-write-loading.md ## Date
// and time strings. wantTime false reads a date only (type date).
func parseDateTimeText(typ, s string, wantTime bool) (timeFields, error) {
	var f timeFields
	in := s
	bc := false
	if len(in) > 3 && strings.EqualFold(in[len(in)-3:], " BC") {
		bc = true
		in = in[:len(in)-3]
	}

	// year : 4 or more digits
	i := 0
	for i < len(in) && in[i] >= '0' && in[i] <= '9' {
		i++
	}
	if i < 4 || i > 7 {
		return f, errBadValue(typ, s, "the year needs 4 to 7 digits")
	}
	year, _ := digitsAt(in, 0, i)
	if i+6 > len(in) || in[i] != '-' || in[i+3] != '-' {
		return f, errBadValue(typ, s, "expected YYYY-MM-DD")
	}
	month, ok1 := digitsAt(in, i+1, 2)
	day, ok2 := digitsAt(in, i+4, 2)
	if !ok1 || !ok2 {
		return f, errBadValue(typ, s, "expected YYYY-MM-DD")
	}
	if bc {
		if year == 0 {
			return f, errBadValue(typ, s, "there is no year 0")
		}
		year = 1 - year
	} else if year == 0 {
		return f, errBadValue(typ, s, "there is no year 0")
	}
	if month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) {
		return f, errBadValue(typ, s, "no such date")
	}
	f.year, f.month, f.day = year, month, day

	rest := in[i+6:]
	if rest == "" {
		if !wantTime {
			return f, nil
		}
		return f, nil // a date alone is midnight
	}
	if !wantTime {
		return f, errBadValue(typ, s, "a date takes no time")
	}
	if rest[0] != 'T' && rest[0] != ' ' {
		return f, errBadValue(typ, s, "expected T or a space after the date")
	}
	rest = rest[1:]
	hour, ok1 := digitsAt(rest, 0, 2)
	if !ok1 || len(rest) < 5 || rest[2] != ':' {
		return f, errBadValue(typ, s, "expected hh:mm")
	}
	minute, ok2 := digitsAt(rest, 3, 2)
	if !ok2 {
		return f, errBadValue(typ, s, "expected hh:mm")
	}
	f.hour, f.min = hour, minute
	rest = rest[5:]
	if len(rest) >= 3 && rest[0] == ':' {
		sec, ok := digitsAt(rest, 1, 2)
		if !ok {
			return f, errBadValue(typ, s, "expected seconds")
		}
		f.sec = sec
		rest = rest[3:]
		if len(rest) > 0 && rest[0] == '.' {
			k := 1
			var frac, scale int64 = 0, 100000
			for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
				if k > 6 {
					return f, errBadValue(typ, s, "at most 6 fractional digits")
				}
				frac += int64(rest[k]-'0') * scale
				scale /= 10
				k++
			}
			if k == 1 {
				return f, errBadValue(typ, s, "no fractional digits")
			}
			f.micro = frac
			rest = rest[k:]
		}
	}
	if f.hour > 24 || f.min > 59 || f.sec > 59 || (f.hour == 24 && (f.min != 0 || f.sec != 0 || f.micro != 0)) {
		return f, errOutOfRange(typ, s)
	}

	// zone : Z, or a numeric offset, optionally after one space
	if rest == "" {
		return f, nil
	}
	if rest[0] == ' ' {
		rest = rest[1:]
	}
	switch {
	case rest == "Z" || rest == "z":
		f.hasZone = true
	case len(rest) >= 3 && (rest[0] == '+' || rest[0] == '-'):
		oh, ok := digitsAt(rest, 1, 2)
		if !ok {
			return f, errBadValue(typ, s, "bad offset")
		}
		var om int64
		tail := rest[3:]
		if tail != "" {
			if tail[0] == ':' {
				tail = tail[1:]
			}
			if len(tail) != 2 {
				return f, errBadValue(typ, s, "bad offset")
			}
			if om, ok = digitsAt(tail, 0, 2); !ok {
				return f, errBadValue(typ, s, "bad offset")
			}
		}
		if oh > 15 || om > 59 {
			return f, errOutOfRange(typ, s)
		}
		f.hasZone = true
		f.zoneSeconds = oh*3600 + om*60
		if rest[0] == '-' {
			f.zoneSeconds = -f.zoneSeconds
		}
	default:
		return f, errBadValue(typ, s, "unrecognized text after the time")
	}
	return f, nil
}

func (f timeFields) epochDays() int64 {
	return daysFromCivil(f.year, f.month, f.day) - pgEpochUnixDays
}

func infinityWord(s string) (sign int, ok bool) {
	switch strings.ToLower(s) {
	case "infinity":
		return 1, true
	case "-infinity":
		return -1, true
	}
	return 0, false
}

func nodeString(typ string, n Node) (string, error) {
	if n.Kind() != KindString {
		return "", errWrongKind(typ, n)
	}
	s, err := n.StrictString()
	if err != nil {
		return "", oops.Wrapf(err, "write: reading %s", typ)
	}
	return s, nil
}

func encodeDate(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	if n.Kind() == KindDate { // CBOR tag 100 : days since 1970-01-01
		d, err := n.(daysNode).Days()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading date")
		}
		days := d - pgEpochUnixDays
		if d < -1<<40 || d > 1<<40 || days < dateMinDays || days > dateMaxDays {
			return nil, errOutOfRange("date", strconv.FormatInt(d, 10))
		}
		return appendInt32(dst, int32(days)), nil
	}
	s, err := nodeString("date", n)
	if err != nil {
		return nil, err
	}
	if sign, ok := infinityWord(s); ok {
		if sign > 0 {
			return appendInt32(dst, dateInfinity), nil
		}
		return appendInt32(dst, dateNegInf), nil
	}
	f, err := parseDateTimeText("date", s, false)
	if err != nil {
		return nil, err
	}
	days := f.epochDays()
	if days < dateMinDays || days > dateMaxDays {
		return nil, errOutOfRange("date", s)
	}
	return appendInt32(dst, int32(days)), nil
}

func encodeTimestamp(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
	return encodeTimestampText(dst, n, "timestamp", ec)
}

func encodeTimestamptz(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
	return encodeTimestampText(dst, n, "timestamptz", ec)
}

// encodeTimestampText serves both timestamp types. A text without an offset is a wall-clock time in the
// session zone for a timestamptz, and the wall clock itself for a timestamp, which ignores any offset. An
// instant (CBOR tag 0 or 1) is placed as is for a timestamptz, and read as the wall clock it shows in the
// session zone for a timestamp.
func encodeTimestampText(dst []byte, n Node, typ string, ec *encodeContext) ([]byte, error) {
	timestamptz := typ == "timestamptz"
	loc := ec.location()
	if n.Kind() == KindInstant {
		t, err := n.(instantNode).Instant()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading %s", typ)
		}
		sec := t.Unix() - pgEpochUnixDays*86400
		if sec < tsMinMicros/microsPerSecond-1 || sec > tsEndMicros/microsPerSecond+1 {
			return nil, errOutOfRange(typ, t.UTC().Format(time.RFC3339))
		}
		micros := sec*microsPerSecond + int64((t.Nanosecond()+500)/1000) // rounded to the nearest microsecond
		if !timestamptz {
			if ec != nil && ec.zoneErr != nil {
				return nil, errUnknownZone(typ, t.UTC().Format(time.RFC3339), ec.zoneName)
			}
			_, off := t.In(loc).Zone()
			micros += int64(off) * microsPerSecond
		}
		if micros < tsMinMicros || micros >= tsEndMicros {
			return nil, errOutOfRange(typ, t.UTC().Format(time.RFC3339))
		}
		return appendInt64(dst, micros), nil
	}
	s, err := nodeString(typ, n)
	if err != nil {
		return nil, err
	}
	if sign, ok := infinityWord(s); ok {
		if sign > 0 {
			return appendInt64(dst, tsInfinity), nil
		}
		return appendInt64(dst, tsNegInfinity), nil
	}
	f, err := parseDateTimeText(typ, s, true)
	if err != nil {
		return nil, err
	}
	days := f.epochDays()
	if days < tsMinMicros/microsPerDay-1 || days > tsEndMicros/microsPerDay+1 {
		return nil, errOutOfRange(typ, s)
	}
	var micros int64
	switch {
	case timestamptz && !f.hasZone:
		if ec != nil && ec.zoneErr != nil {
			return nil, errUnknownZone(typ, s, ec.zoneName)
		}
		wall := days*86400 + f.hour*3600 + f.min*60 + f.sec // the wall clock read as if it were UTC, in seconds since 2000
		micros = (wall-zoneOffsetAt(loc, wall))*microsPerSecond + f.micro
	default:
		secs := days*86400 + f.hour*3600 + f.min*60 + f.sec
		if timestamptz {
			secs -= f.zoneSeconds // a timestamp ignores the offset ; a timestamptz applies it
		}
		micros = secs*microsPerSecond + f.micro
	}
	if micros < tsMinMicros || micros >= tsEndMicros {
		return nil, errOutOfRange(typ, s)
	}
	return appendInt64(dst, micros), nil
}

// zoneOffsetAt returns the offset, in seconds east of UTC, Postgres applies to a wall-clock
// reading in loc. wall is the reading as seconds since 2000-01-01 00:00:00, as if it were UTC.
// Across a clock change, a reading skipped by a spring-forward gap gets the offset in force before
// the change, and a reading that occurs twice in a fall-back overlap gets the offset in force after it.
func zoneOffsetAt(loc *time.Location, wall int64) int64 {
	if loc == time.UTC {
		return 0
	}
	offsetAtInstant := func(secsSince2000 int64) int64 {
		_, off := time.Unix(secsSince2000+pgEpochUnixDays*86400, 0).In(loc).Zone()
		return int64(off)
	}
	const window = 86400
	before, after := offsetAtInstant(wall-window), offsetAtInstant(wall+window)
	if before == after {
		return before
	}
	validBefore := offsetAtInstant(wall-before) == before
	validAfter := offsetAtInstant(wall-after) == after
	switch {
	case validBefore && validAfter, validAfter && !validBefore:
		return after
	case validBefore:
		return before
	}
	return before // in a gap : neither offset holds at its own instant
}
