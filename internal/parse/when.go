package parse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	dateRe = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})(?:\.(\d{2}|\d{4}))?$`)
	timeRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

// civil is a calendar date without a time zone.
type civil struct{ y, m, d int }

// clock is a time of day.
type clock struct{ h, m int }

// parseDateToken understands "сегодня", "вчера", "позавчера", "12.10", "12.10.25", "12.10.2025".
func parseDateToken(tok string, now time.Time) (civil, bool) {
	tok = strings.ToLower(tok)
	switch tok {
	case "сегодня":
		return civilOf(now), true
	case "вчера":
		return civilOf(now.AddDate(0, 0, -1)), true
	case "позавчера":
		return civilOf(now.AddDate(0, 0, -2)), true
	}
	m := dateRe.FindStringSubmatch(tok)
	if m == nil {
		return civil{}, false
	}
	d, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	if d < 1 || d > 31 || mo < 1 || mo > 12 {
		return civil{}, false
	}
	y := now.Year()
	explicit := m[3] != ""
	if explicit {
		y, _ = strconv.Atoi(m[3])
		if y < 100 {
			y += 2000
		}
	}
	c := civil{y, mo, d}
	if !validCivil(c) {
		return civil{}, false
	}
	// A bare day.month in the future most likely means the previous year.
	if !explicit && time.Date(y, time.Month(mo), d, 0, 0, 0, 0, now.Location()).After(now.AddDate(0, 0, 1)) {
		c.y--
		if !validCivil(c) {
			return civil{}, false
		}
	}
	return c, true
}

func parseTimeToken(tok string) (clock, bool) {
	m := timeRe.FindStringSubmatch(tok)
	if m == nil {
		return clock{}, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	if h > 23 || mi > 59 {
		return clock{}, false
	}
	return clock{h, mi}, true
}

func civilOf(t time.Time) civil { return civil{t.Year(), int(t.Month()), t.Day()} }

func validCivil(c civil) bool {
	t := time.Date(c.y, time.Month(c.m), c.d, 0, 0, 0, 0, time.UTC)
	return t.Year() == c.y && int(t.Month()) == c.m && t.Day() == c.d
}

// isDateLike reports whether the token looks like a date ("12.10", "вчера").
func isDateLike(tok string, now time.Time) bool {
	_, ok := parseDateToken(tok, now)
	return ok
}

func compose(now time.Time, d *civil, c *clock) time.Time {
	y, mo, day := now.Year(), int(now.Month()), now.Day()
	if d != nil {
		y, mo, day = d.y, d.m, d.d
	}
	h, mi, sec := now.Hour(), now.Minute(), 0
	if c != nil {
		h, mi = c.h, c.m
	}
	return time.Date(y, time.Month(mo), day, h, mi, sec, 0, now.Location())
}

// ParseWhen parses a whole string consisting only of date and time tokens
// ("вчера 9:15", "12.10 14:30", "14:30"). Missing parts default to now.
func ParseWhen(s string, now time.Time) (time.Time, bool) {
	toks := strings.Fields(s)
	if len(toks) == 0 || len(toks) > 2 {
		return time.Time{}, false
	}
	var d *civil
	var c *clock
	for _, t := range toks {
		if v, ok := parseDateToken(t, now); ok && d == nil {
			d = &v
		} else if v, ok := parseTimeToken(t); ok && c == nil {
			c = &v
		} else {
			return time.Time{}, false
		}
	}
	return compose(now, d, c), true
}
