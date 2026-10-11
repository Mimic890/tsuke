package parse

import (
	"sort"
	"strings"
	"unicode"
)

// Normalize lowercases, folds ё to е and drops everything but letters and digits,
// so that "Т-Банк", "т банк" and "тбанк" compare equal.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == 'ё' {
			r = 'е'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Rank returns indices of names matching the query, best first: exact, prefix,
// word prefix, substring. Ties keep the input order, so callers pass names
// sorted by relevance (e.g. usage).
func Rank(query string, names []string) []int {
	q := Normalize(query)
	if q == "" {
		return nil
	}
	type hit struct{ idx, score int }
	var hits []hit
	for i, n := range names {
		nn := Normalize(n)
		score := 0
		switch {
		case nn == q:
			score = 4
		case strings.HasPrefix(nn, q):
			score = 3
		case wordPrefix(n, q):
			score = 2
		case strings.Contains(nn, q):
			score = 1
		}
		if score > 0 {
			hits = append(hits, hit{i, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.idx
	}
	return out
}

func wordPrefix(name, q string) bool {
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if strings.HasPrefix(Normalize(w), q) {
			return true
		}
	}
	return false
}

// Best returns the index of the best match or -1.
func Best(query string, names []string) int {
	if r := Rank(query, names); len(r) > 0 {
		return r[0]
	}
	return -1
}
