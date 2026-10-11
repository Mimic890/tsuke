package parse

import (
	"regexp"
	"strings"
	"time"
)

// Transaction types, matching Firefly III.
const (
	Withdrawal = "withdrawal"
	Deposit    = "deposit"
	Transfer   = "transfer"
)

// Input is the result of parsing a quick-entry message.
type Input struct {
	Type        string
	Cents       int64 // 0 when no amount was found
	Description string
	Account     string // @account hint
	From, To    string // transfer endpoints
	Category    string
	Tags        []string
	When        time.Time
	HasWhen     bool // a date and/or time was given explicitly
}

var (
	thousandsHead = regexp.MustCompile(`^[+-]?\d{1,3}$`)
	thousandsTail = regexp.MustCompile(`(?i)^\d{3}(?:[.,]\d{1,2})?(?:к|k|тыс)?$`)
	arrowRe       = regexp.MustCompile(`\s*(?:->|→|>)\s*`)
)

// Parse parses a quick-entry message such as "350 кофе #еда @тбанк вчера".
func Parse(s string, now time.Time) Input {
	s = strings.NewReplacer("\u00a0", " ", "\u202f", " ").Replace(s)
	s = arrowRe.ReplaceAllString(s, " > ")
	toks := strings.Fields(s)
	in := Input{Type: Withdrawal}

	toks, sign, cents := extractAmount(toks, now)
	in.Cents = cents
	if sign > 0 {
		in.Type = Deposit
	}

	var (
		date *civil
		tm   *clock
		desc []string
	)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t == ">":
			if i > 0 && len(desc) > 0 {
				in.From = desc[len(desc)-1]
				desc = desc[:len(desc)-1]
			}
			if i+1 < len(toks) {
				in.To = toks[i+1]
				i++
			}
			in.Type = Transfer
		case len(t) > 1 && t[0] == '#':
			in.Category = unslug(t[1:])
		case len(t) > 1 && t[0] == '@':
			in.Account = unslug(t[1:])
		case len(t) > 1 && t[0] == '!':
			in.Tags = append(in.Tags, unslug(t[1:]))
		default:
			if v, ok := parseDateToken(t, now); ok && date == nil {
				date = &v
			} else if v, ok := parseTimeToken(t); ok && tm == nil {
				tm = &v
			} else {
				desc = append(desc, t)
			}
		}
	}
	in.Description = strings.Join(desc, " ")
	in.HasWhen = date != nil || tm != nil
	in.When = compose(now, date, tm)
	return in
}

// extractAmount finds the amount token, merges "1 500" style groups and
// returns the remaining tokens.
func extractAmount(toks []string, now time.Time) (rest []string, sign int, cents int64) {
	pick := -1
	dateLike := -1
	for i, t := range toks {
		if _, _, err := ParseAmount(t); err != nil {
			continue
		}
		if isDateLike(t, now) {
			if dateLike < 0 {
				dateLike = i
			}
			continue
		}
		pick = i
		break
	}
	if pick < 0 {
		pick = dateLike // "12.10" with no better candidate is an amount
	}
	if pick < 0 {
		return toks, 0, 0
	}
	end := pick + 1
	if thousandsHead.MatchString(toks[pick]) {
		raw := toks[pick]
		for end < len(toks) && thousandsTail.MatchString(toks[end]) {
			tail := toks[end]
			raw += tail
			end++
			if len(tail) != 3 { // decimals or a suffix end the number
				break
			}
		}
		toks[pick] = raw
	}
	cents, sign, err := ParseAmount(toks[pick])
	if err != nil {
		return toks, 0, 0
	}
	rest = append(rest, toks[:pick]...)
	rest = append(rest, toks[end:]...)
	return rest, sign, cents
}

func unslug(s string) string { return strings.ReplaceAll(s, "_", " ") }
