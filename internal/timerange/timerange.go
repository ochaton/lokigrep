package timerange

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var absLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
	"01-02 15:04:05",
	"15:04:05",
	"15:04",
}

// Parse parses the -t argument: "<from>/<to>".
//
//	-2h/-1h                 two hours ago .. one hour ago
//	10m/                    ten minutes ago .. now
//	5m                      same as 5m/
//	2026-07-10T12:00:00/    absolute start .. now
//	2026-07-10/2026-07-11   absolute range
//	2026-07-10/-2h          absolute start .. two hours ago
//	midnight/               today 00:00 local .. now
//	yesterday/midnight      the whole of yesterday
//	1h/+15m                 one hour ago .. 45 minutes ago (+ is relative to from)
//	2 days ago/+10m         ten minutes starting two days ago
//	yesterday 14:00/+1h     an hour of yesterday afternoon
func Parse(spec string, now time.Time) (from, to time.Time, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return from, to, fmt.Errorf("empty time range")
	}

	fromTok, toTok := spec, ""
	if i := strings.Index(spec, "/"); i >= 0 {
		fromTok, toTok = strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
		if strings.Contains(toTok, "/") {
			return from, to, fmt.Errorf("time range %q has more than one %q", spec, "/")
		}
	}

	if fromTok == "" {
		return from, to, fmt.Errorf("time range %q has no start", spec)
	}
	from, err = parseInstant(fromTok, now, time.Time{})
	if err != nil {
		return from, to, fmt.Errorf("bad range start %q: %w", fromTok, err)
	}

	if toTok == "" {
		to = now
	} else {
		to, err = parseInstant(toTok, now, from)
		if err != nil {
			return from, to, fmt.Errorf("bad range end %q: %w", toTok, err)
		}
	}

	if !to.After(from) {
		return from, to, fmt.Errorf("range end %s is not after start %s",
			to.Format(time.RFC3339), from.Format(time.RFC3339))
	}
	return from, to, nil
}

// parseInstant resolves one side of a range. base is the already resolved range
// start, used only for "+d" offsets.
func parseInstant(tok string, now time.Time, base time.Time) (time.Time, error) {
	lower := strings.ToLower(tok)

	switch lower {
	case "now":
		return now, nil
	case "midnight", "today":
		return startOfDay(now), nil
	case "yesterday":
		return startOfDay(now).AddDate(0, 0, -1), nil
	case "tomorrow":
		return startOfDay(now).AddDate(0, 0, 1), nil
	}

	// "yesterday 14:00", "today 08:30"
	if t, ok := parseDayAtClock(lower, now); ok {
		return t, nil
	}

	// "+15m" and "+10 minutes" are relative to the range start, if we have one
	if strings.HasPrefix(lower, "+") {
		sp, ok := parseSpan(strings.TrimPrefix(lower, "+"))
		if !ok {
			return time.Time{}, fmt.Errorf("not a duration")
		}
		if base.IsZero() {
			return sp.before(now), nil
		}
		return sp.after(base), nil
	}

	// everything else that reads as a length of time means "ago": "2h",
	// "-2h", "2 days ago", "last 30 minutes", "1 hour 30 min ago"
	if sp, ok := parseSpan(stripAgo(lower)); ok {
		return sp.before(now), nil
	}

	// unix timestamps
	if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
		switch {
		case len(tok) >= 19:
			return time.Unix(0, n), nil
		case len(tok) >= 16:
			return time.Unix(0, n*1e3), nil
		case len(tok) >= 13:
			return time.Unix(0, n*1e6), nil
		case len(tok) >= 9:
			return time.Unix(n, 0), nil
		}
	}

	for _, layout := range absLayouts {
		t, err := time.ParseInLocation(layout, tok, time.Local)
		if err != nil {
			continue
		}
		// layouts without a date land in year 0; graft today's date onto them
		if t.Year() == 0 {
			y, m, d := now.Date()
			t = time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.Local)
		}
		return t, nil
	}

	return time.Time{}, fmt.Errorf("not a duration, timestamp or date")
}

// spanUnits maps the unit words people actually type to a length of time.
// Months and years are calendar steps, not fixed durations.
var spanUnits = map[string]span{
	"s": {dur: time.Second}, "sec": {dur: time.Second}, "secs": {dur: time.Second},
	"second": {dur: time.Second}, "seconds": {dur: time.Second},

	"m": {dur: time.Minute}, "min": {dur: time.Minute}, "mins": {dur: time.Minute},
	"minute": {dur: time.Minute}, "minutes": {dur: time.Minute},

	"h": {dur: time.Hour}, "hr": {dur: time.Hour}, "hrs": {dur: time.Hour},
	"hour": {dur: time.Hour}, "hours": {dur: time.Hour},

	"d": {dur: 24 * time.Hour}, "day": {dur: 24 * time.Hour}, "days": {dur: 24 * time.Hour},
	"w": {dur: 7 * 24 * time.Hour}, "week": {dur: 7 * 24 * time.Hour}, "weeks": {dur: 7 * 24 * time.Hour},

	"mo": {months: 1}, "month": {months: 1}, "months": {months: 1},
	"y": {years: 1}, "year": {years: 1}, "years": {years: 1},
}

// span is a length of time, possibly with a calendar part.
type span struct {
	dur    time.Duration
	months int
	years  int
}

func (s span) before(t time.Time) time.Time {
	return t.AddDate(-s.years, -s.months, 0).Add(-s.dur)
}

func (s span) after(t time.Time) time.Time {
	return t.AddDate(s.years, s.months, 0).Add(s.dur)
}

func (s span) isZero() bool { return s.dur == 0 && s.months == 0 && s.years == 0 }

var spanTerm = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([a-z]+)`)

// stripAgo removes the English wrapping around a length of time.
func stripAgo(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasSuffix(s, " ago"):
		return strings.TrimSpace(strings.TrimSuffix(s, " ago"))
	case strings.HasPrefix(s, "last "):
		return strings.TrimSpace(strings.TrimPrefix(s, "last "))
	case strings.HasPrefix(s, "past "):
		return strings.TrimSpace(strings.TrimPrefix(s, "past "))
	}
	return strings.TrimPrefix(s, "-")
}

// parseSpan reads a length of time written in any of the shapes people use:
// "10m", "1h30m", "2 days", "1 hour 30 min", "3mo". Every character has to be
// part of a number/unit pair, so dates and clock times fall through to the
// absolute parsers instead of being half-read as a duration.
func parseSpan(s string) (span, bool) {
	rest := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if rest == "" {
		return span{}, false
	}

	var total span
	for rest != "" {
		m := spanTerm.FindStringSubmatch(rest)
		if m == nil {
			return span{}, false
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return span{}, false
		}
		unit, ok := spanUnits[m[2]]
		if !ok {
			return span{}, false
		}
		switch {
		case unit.months != 0:
			total.months += unit.months * int(n)
		case unit.years != 0:
			total.years += unit.years * int(n)
		default:
			total.dur += time.Duration(n * float64(unit.dur))
		}
		rest = rest[len(m[0]):]
	}

	if total.isZero() {
		return span{}, false
	}
	return total, true
}

// parseDayAtClock understands "yesterday 14:00" and "today 08:30".
func parseDayAtClock(tok string, now time.Time) (time.Time, bool) {
	fields := strings.Fields(tok)
	if len(fields) != 2 {
		return time.Time{}, false
	}
	var day time.Time
	switch fields[0] {
	case "today", "midnight":
		day = startOfDay(now)
	case "yesterday":
		day = startOfDay(now).AddDate(0, 0, -1)
	case "tomorrow":
		day = startOfDay(now).AddDate(0, 0, 1)
	default:
		return time.Time{}, false
	}

	for _, layout := range []string{"15:04:05", "15:04"} {
		clock, err := time.ParseInLocation(layout, fields[1], time.Local)
		if err != nil {
			continue
		}
		return day.Add(time.Duration(clock.Hour())*time.Hour +
			time.Duration(clock.Minute())*time.Minute +
			time.Duration(clock.Second())*time.Second), true
	}
	return time.Time{}, false
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
