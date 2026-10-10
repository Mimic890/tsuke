// Package parse turns free-form chat input into structured transaction data.
package parse

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrAmount is returned for strings that are not a valid amount.
var ErrAmount = errors.New("invalid amount")

// ParseAmount parses amounts such as "350", "1 500", "1500,50", "1.5к", "+50000".
// It returns the value in minor units (kopecks) and the explicit sign:
// +1 for a leading "+", -1 for a leading "-", 0 when absent.
func ParseAmount(s string) (cents int64, sign int, err error) {
	s = strings.NewReplacer("\u00a0", "", "\u202f", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, 0, ErrAmount
	}
	switch {
	case strings.HasPrefix(s, "+"):
		sign, s = 1, s[1:]
	case strings.HasPrefix(s, "-"):
		sign, s = -1, s[1:]
	case strings.HasPrefix(s, "\u2212"):
		sign, s = -1, strings.TrimPrefix(s, "\u2212")
	}

	mult := int64(1)
	lower := strings.ToLower(s)
	for _, suf := range []string{"тыс", "к", "k"} {
		if strings.HasSuffix(lower, suf) {
			mult = 1000
			s = s[:len(s)-len(suf)]
			break
		}
	}
	if s == "" {
		return 0, 0, ErrAmount
	}

	whole, frac, err := splitDecimal(s, mult > 1)
	if err != nil {
		return 0, 0, err
	}
	if len(frac) > 2 {
		return 0, 0, ErrAmount
	}
	frac += strings.Repeat("0", 2-len(frac))

	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, 0, ErrAmount
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, 0, ErrAmount
	}
	cents = (w*100 + f) * mult
	if cents <= 0 || cents > 1_000_000_000_000_00 {
		return 0, 0, ErrAmount
	}
	return cents, sign, nil
}

// splitDecimal separates the integer and fractional digits, deciding whether
// "." and "," are thousands or decimal separators.
func splitDecimal(s string, suffixed bool) (whole, frac string, err error) {
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != ',' {
			return "", "", ErrAmount
		}
	}
	last := strings.LastIndexAny(s, ".,")
	if last < 0 {
		return s, "", nil
	}
	hasDot, hasComma := strings.Contains(s, "."), strings.Contains(s, ",")
	count := strings.Count(s, ".") + strings.Count(s, ",")
	after := len(s) - last - 1

	decimal := false
	switch {
	case hasDot && hasComma:
		decimal = true
	case count > 1:
		decimal = false
	default:
		decimal = after != 3 || suffixed
	}
	if after == 0 {
		return "", "", ErrAmount
	}
	clean := func(x string) string { return strings.NewReplacer(".", "", ",", "").Replace(x) }
	if decimal {
		whole, frac = clean(s[:last]), s[last+1:]
		if whole == "" {
			whole = "0"
		}
		return whole, frac, nil
	}
	// Thousands separators: every group after the first must have 3 digits.
	groups := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == ',' })
	for i, g := range groups {
		if (i == 0 && len(g) > 3) || (i > 0 && len(g) != 3) {
			return "", "", ErrAmount
		}
	}
	return clean(s), "", nil
}

// FormatAPI renders cents as a plain decimal string for the Firefly API ("350.00").
func FormatAPI(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// FormatHuman renders cents for people: "1 500", "1 500,50".
func FormatHuman(cents int64) string {
	whole := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(r)
	}
	if f := cents % 100; f != 0 {
		fmt.Fprintf(&b, ",%02d", f)
	}
	return b.String()
}

// APIToCents converts a decimal string from the API ("350.000000") to cents.
func APIToCents(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	if f < 0 {
		f = -f
	}
	return int64(f*100 + 0.5)
}
