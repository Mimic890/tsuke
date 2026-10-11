package parse

import (
	"reflect"
	"testing"
	"time"
)

var msk = time.FixedZone("MSK", 3*3600)

// now is Saturday 2026-10-10 15:20 MSK.
var now = time.Date(2026, 10, 10, 15, 20, 0, 0, msk)

func TestParseAmount(t *testing.T) {
	tests := []struct {
		in    string
		cents int64
		sign  int
		bad   bool
	}{
		{in: "350", cents: 35000},
		{in: "1 500", cents: 150000},
		{in: "1 500", cents: 150000},
		{in: "1500,50", cents: 150050},
		{in: "1500.50", cents: 150050},
		{in: "1.5к", cents: 150000},
		{in: "1,5k", cents: 150000},
		{in: "2к", cents: 200000},
		{in: "+50000", cents: 5000000, sign: 1},
		{in: "-12,5", cents: 1250, sign: -1},
		{in: "1.500", cents: 150000},
		{in: "1,234,567", cents: 123456700},
		{in: "1.234,56", cents: 123456},
		{in: "0,99", cents: 99},
		{in: ",5", cents: 50},
		{in: "", bad: true},
		{in: "abc", bad: true},
		{in: "12.345.6", bad: true},
		{in: "0", bad: true},
		{in: "к", bad: true},
		{in: "1.5.к", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			c, s, err := ParseAmount(tt.in)
			if (err != nil) != tt.bad {
				t.Fatalf("err = %v, want bad=%v", err, tt.bad)
			}
			if err == nil && (c != tt.cents || s != tt.sign) {
				t.Fatalf("got %d/%d, want %d/%d", c, s, tt.cents, tt.sign)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	if got := FormatAPI(35000); got != "350.00" {
		t.Errorf("FormatAPI = %q", got)
	}
	if got := FormatAPI(5); got != "0.05" {
		t.Errorf("FormatAPI = %q", got)
	}
	if got := FormatHuman(150050); got != "1 500,50" {
		t.Errorf("FormatHuman = %q", got)
	}
	if got := FormatHuman(35000); got != "350" {
		t.Errorf("FormatHuman = %q", got)
	}
	if got := APIToCents("350.000000000000"); got != 35000 {
		t.Errorf("APIToCents = %d", got)
	}
	if got := APIToCents("-12.505"); got != -1251 {
		t.Errorf("APIToCents negative = %d", got)
	}
	if got := FormatHuman(-123456); got != "-1\u00a0234,56" {
		t.Errorf("FormatHuman negative = %q", got)
	}
}

func TestParseWhen(t *testing.T) {
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, msk) }
	tests := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"вчера", at(2026, 10, 9, 15, 20), true},
		{"Позавчера", at(2026, 10, 8, 15, 20), true},
		{"сегодня", now, true},
		{"14:30", at(2026, 10, 10, 14, 30), true},
		{"9:05", at(2026, 10, 10, 9, 5), true},
		{"вчера 9:15", at(2026, 10, 9, 9, 15), true},
		{"9:15 вчера", at(2026, 10, 9, 9, 15), true},
		{"12.10", at(2025, 10, 12, 15, 20), true}, // future => previous year
		{"01.10", at(2026, 10, 1, 15, 20), true},
		{"12.10 14:30", at(2025, 10, 12, 14, 30), true},
		{"12.10.2026 14:30", at(2026, 10, 12, 14, 30), true},
		{"05.03.25", at(2025, 3, 5, 15, 20), true},
		{"31.02", time.Time{}, false},
		{"25:00", time.Time{}, false},
		{"кофе", time.Time{}, false},
		{"вчера кофе", time.Time{}, false},
		{"", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseWhen(tt.in, now)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && !got.Equal(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseInput(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, msk) }
	tests := []struct {
		name string
		in   string
		want Input
	}{
		{"simple", "350 кофе", Input{Type: Withdrawal, Cents: 35000, Description: "кофе", When: now}},
		{"amount last", "кофе 350", Input{Type: Withdrawal, Cents: 35000, Description: "кофе", When: now}},
		{"deposit", "+50000 зарплата", Input{Type: Deposit, Cents: 5000000, Description: "зарплата", When: now}},
		{"thousands", "1 500 такси", Input{Type: Withdrawal, Cents: 150000, Description: "такси", When: now}},
		{"decimal comma", "1500,50 аптека", Input{Type: Withdrawal, Cents: 150050, Description: "аптека", When: now}},
		{"k suffix", "1.5к ремонт", Input{Type: Withdrawal, Cents: 150000, Description: "ремонт", When: now}},
		{"transfer", "5000 тбанк > наличные", Input{Type: Transfer, Cents: 500000, From: "тбанк", To: "наличные", When: now}},
		{"transfer tight", "5000 тбанк>наличные снял", Input{Type: Transfer, Cents: 500000, From: "тбанк", To: "наличные", Description: "снял", When: now}},
		{"transfer default src", "700 > наличные", Input{Type: Transfer, Cents: 70000, To: "наличные", When: now}},
		{"tokens", "350 кофе с молоком #еда_вне_дома @тбанк !работа !командировка", Input{
			Type: Withdrawal, Cents: 35000, Description: "кофе с молоком", Category: "еда вне дома",
			Account: "тбанк", Tags: []string{"работа", "командировка"}, When: now}},
		{"yesterday", "350 кофе вчера", Input{Type: Withdrawal, Cents: 35000, Description: "кофе", HasWhen: true, When: at(9, 15, 20)}},
		{"date time", "350 кофе 05.10 14:30", Input{Type: Withdrawal, Cents: 35000, Description: "кофе", HasWhen: true, When: at(5, 14, 30)}},
		{"time only", "350 кофе 9:15", Input{Type: Withdrawal, Cents: 35000, Description: "кофе", HasWhen: true, When: at(10, 9, 15)}},
		{"date-like amount", "12.50 кофе", Input{Type: Withdrawal, Cents: 1250, Description: "кофе", When: now}},
		{"date before amount", "05.10 350 кофе", Input{Type: Withdrawal, Cents: 35000, Description: "кофе", HasWhen: true, When: at(5, 15, 20)}},
		{"no amount", "просто текст", Input{Type: Withdrawal, Description: "просто текст", When: now}},
		{"nbsp", "1 500 кофе", Input{Type: Withdrawal, Cents: 150000, Description: "кофе", When: now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.in, now)
			if tt.want.Tags == nil {
				tt.want.Tags = got.Tags
				if len(got.Tags) != 0 {
					t.Fatalf("unexpected tags %v", got.Tags)
				}
			}
			if !got.When.Equal(tt.want.When) {
				t.Errorf("When = %v, want %v", got.When, tt.want.When)
			}
			got.When, tt.want.When = time.Time{}, time.Time{}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestRank(t *testing.T) {
	names := []string{"Т-Банк Дебетовая", "Наличные", "Сбербанк", "Банк Тинькофф"}
	tests := []struct {
		q    string
		want []int
	}{
		{"тбанк", []int{0}},
		{"т-банк", []int{0}},
		{"НАЛ", []int{1}},
		{"банк", []int{3, 0, 2}}, // word-prefix before substring
		{"сбер", []int{2}},
		{"ъ", nil},
		{"", nil},
		{"zzz", nil},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			got := Rank(tt.q, names)
			if !reflect.DeepEqual(got, tt.want) && (len(got) != 0 || len(tt.want) != 0) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	if Best("наличные", names) != 1 || Best("qq", names) != -1 {
		t.Error("Best mismatch")
	}
	if Normalize("Ёлка-Палка 2") != "елкапалка2" {
		t.Errorf("Normalize = %q", Normalize("Ёлка-Палка 2"))
	}
}

func TestParseWhenBase(t *testing.T) {
	base := time.Date(2026, 9, 1, 8, 5, 0, 0, msk)
	got, ok := ParseWhenBase("14:30", now, base)
	if !ok || !got.Equal(time.Date(2026, 9, 1, 14, 30, 0, 0, msk)) {
		t.Errorf("time only: %v %v", got, ok)
	}
	got, ok = ParseWhenBase("вчера", now, base)
	if !ok || !got.Equal(time.Date(2026, 10, 9, 8, 5, 0, 0, msk)) {
		t.Errorf("date only: %v %v", got, ok)
	}
}
