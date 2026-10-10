// Package receipt extracts data from Russian fiscal receipts (QR codes).
package receipt

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/Mimic890/tsuke/internal/parse"
)

// Fiscal is the data encoded in a fiscal receipt QR code.
type Fiscal struct {
	When  time.Time
	Cents int64
	FN    string // fiscal drive number
	I     string // fiscal document number
	FP    string // fiscal sign
	N     int    // operation type, 1 = income (a purchase), 2 = return of income
}

// Type maps the receipt operation to a transaction type.
//
// 1 (purchase) and 4 (return of expense) are money leaving the user;
// 2 (return) and 3 (expense of the seller) are money coming back.
func (f Fiscal) Type() string {
	switch f.N {
	case 2, 3:
		return parse.Deposit
	default:
		return parse.Withdrawal
	}
}

// Ref is a compact identifier for notes: "fn=… i=… fp=…".
func (f Fiscal) Ref() string {
	return "fn=" + f.FN + " i=" + f.I + " fp=" + f.FP
}

// ErrNotFiscal is returned for QR payloads that are not a fiscal receipt.
var ErrNotFiscal = errors.New("not a fiscal receipt QR")

var timeLayouts = []string{
	"20060102T1504",
	"20060102T150405",
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
}

// ParseQR parses a payload like "t=20261010T0853&s=350.00&fn=…&i=…&fp=…&n=1".
// Times are interpreted in loc, as receipts carry no zone.
func ParseQR(payload string, loc *time.Location) (Fiscal, error) {
	payload = strings.TrimSpace(payload)
	if !strings.Contains(payload, "t=") || !strings.Contains(payload, "s=") {
		return Fiscal{}, ErrNotFiscal
	}
	// Some apps put the query behind a URL.
	if i := strings.Index(payload, "?"); i >= 0 {
		payload = payload[i+1:]
	}
	q, err := url.ParseQuery(payload)
	if err != nil {
		return Fiscal{}, ErrNotFiscal
	}
	f := Fiscal{FN: q.Get("fn"), I: q.Get("i"), FP: q.Get("fp")}

	cents, _, err := parse.ParseAmount(strings.ReplaceAll(q.Get("s"), " ", ""))
	if err != nil {
		return Fiscal{}, ErrNotFiscal
	}
	f.Cents = cents

	ts := q.Get("t")
	var parsed bool
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, ts, loc); err == nil {
			f.When, parsed = t, true
			break
		}
	}
	if !parsed {
		return Fiscal{}, ErrNotFiscal
	}

	f.N = 1
	switch q.Get("n") {
	case "2":
		f.N = 2
	case "3":
		f.N = 3
	case "4":
		f.N = 4
	}
	return f, nil
}

// Item is a single receipt line. It is reserved for Enricher implementations.
type Item struct {
	Name       string
	Cents      int64
	Quantity   float64
	TotalCents int64
}

// Enricher fetches line items for a receipt (e.g. from the tax service).
// v1 ships without an implementation; the bot calls it when one is configured.
type Enricher interface {
	Items(f Fiscal) ([]Item, error)
}
