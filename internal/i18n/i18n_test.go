package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestT(t *testing.T) {
	if got := T("err.account_not_found", "x"); got != "Счёт «x» не найден" {
		t.Errorf("got %q", got)
	}
	if got := T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key = %q", got)
	}
	Set("xx") // unknown language is ignored
	if !Has("start") {
		t.Error("language must stay ru")
	}
}

// Every key referenced from the bot must exist in the table.
func TestAllKeysDefined(t *testing.T) {
	static := regexp.MustCompile(`(?:i18n\.T|line)\("([a-z0-9_]+\.[a-z0-9_]+|start|help)"`)
	dynamic := map[string][]string{
		"type.":   {"withdrawal", "deposit", "transfer"},
		"prompt.": {"amount", "desc", "notes", "file", "amount_noqr"},
		"pick.":   {"from", "to", "category", "budget", "tags"},
		"period.": {"today", "week", "month"},
		"cmd.":    {"new", "last", "undo", "balance", "today", "week", "month", "refresh", "cancel", "help"},
		"wd.":     {"0", "1", "2", "3", "4", "5", "6"},
		"mon.":    {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"},
	}
	for prefix, names := range dynamic {
		for _, n := range names {
			if !Has(prefix + n) {
				t.Errorf("missing %s%s", prefix, n)
			}
		}
	}
	files, _ := filepath.Glob("../bot/*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range static.FindAllStringSubmatch(string(src), -1) {
			if !Has(m[1]) {
				t.Errorf("%s: key %q is not defined", filepath.Base(f), m[1])
			}
		}
	}
	for _, k := range []string{"err.tx_gone", "err.firefly_down"} {
		if !Has(k) {
			t.Errorf("missing %s", k)
		}
	}
}
