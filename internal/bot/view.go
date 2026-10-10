package bot

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/parse"
)

const pageSize = 8

type keyboard = [][]models.InlineKeyboardButton

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func markup(kb keyboard) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: kb}
}

func esc(s string) string { return html.EscapeString(s) }

func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return esc(s)
}

func fmtWhen(t time.Time) string {
	return fmt.Sprintf("%s, %s", t.Format("02.01.2006 15:04"), i18n.T("wd."+strconv.Itoa(int(t.Weekday()))))
}

// cb builds callback data for a draft: d:<id>:<cmd>.
func (d *Draft) cb(cmd string) string { return "d:" + d.ID + ":" + cmd }

// view renders the draft for its current screen.
func (b *Bot) view(d *Draft) (string, *models.InlineKeyboardMarkup) {
	switch d.Screen {
	case scType:
		return b.typeView(d)
	case scPrompt:
		return b.promptView(d)
	case scPick:
		return b.pickView(d)
	case scDate:
		return b.dateView(d)
	case scTime:
		return b.timeView(d)
	default:
		return b.cardView(d)
	}
}

func (b *Bot) money(d *Draft) string {
	if d.Cents <= 0 {
		return "—"
	}
	cur := "₽"
	switch {
	case d.Src.isAsset():
		cur = b.assetCurrency(d.Src.ID)
	case d.Dst.isAsset():
		cur = b.assetCurrency(d.Dst.ID)
	}
	return parse.FormatHuman(d.Cents) + " " + cur
}

func (b *Bot) cardView(d *Draft) (string, *models.InlineKeyboardMarkup) {
	var s strings.Builder
	head := i18n.T("card.new")
	if d.EditGroup != "" {
		head = i18n.T("card.editing")
	}
	fmt.Fprintf(&s, "<b>%s</b> · %s %s\n\n", head, esc(i18n.T("type."+d.Type)), b.money(d))
	line := func(key, val string) { fmt.Fprintf(&s, "%s: %s\n", i18n.T(key), val) }
	line("f.from", orDash(d.Src.Name))
	line("f.to", orDash(d.Dst.Name))
	line("f.category", orDash(d.Category))
	if d.Type == "withdrawal" {
		line("f.budget", orDash(d.Budget))
	}
	line("f.tags", orDash(strings.Join(d.Tags, ", ")))
	line("f.desc", orDash(d.Desc))
	line("f.notes", orDash(d.Notes))
	line("f.date", esc(fmtWhen(d.When)))
	if n := len(d.Files); n > 0 {
		line("f.files", strconv.Itoa(n))
	}
	if d.Err != "" {
		fmt.Fprintf(&s, "\n⚠️ %s\n", esc(d.Err))
	}

	kb := keyboard{
		{btn(i18n.T("b.type"), d.cb("f:type")), btn(i18n.T("b.amount"), d.cb("f:amount"))},
		{btn(i18n.T("b.from"), d.cb("f:from")), btn(i18n.T("b.to"), d.cb("f:to"))},
		{btn(i18n.T("b.category"), d.cb("f:category"))},
	}
	if d.Type == "withdrawal" {
		kb[2] = append(kb[2], btn(i18n.T("b.budget"), d.cb("f:budget")))
	}
	kb = append(kb,
		keyboardRow(btn(i18n.T("b.tags"), d.cb("f:tags")), btn(i18n.T("b.desc"), d.cb("f:desc"))),
		keyboardRow(btn(i18n.T("b.notes"), d.cb("f:notes")), btn(i18n.T("b.file"), d.cb("f:file"))),
		keyboardRow(btn(i18n.T("b.date"), d.cb("f:date")), btn(i18n.T("b.time"), d.cb("f:time"))),
		keyboardRow(btn(i18n.T("b.save"), d.cb("sv")), btn(i18n.T("b.cancel"), d.cb("cx"))),
	)
	return s.String(), markup(kb)
}

func keyboardRow(bs ...models.InlineKeyboardButton) []models.InlineKeyboardButton { return bs }

// backBtn returns to the card, or skips the step in guided mode.
func (d *Draft) backBtn() models.InlineKeyboardButton {
	if d.Guided {
		return btn(i18n.T("b.skip"), d.cb("back"))
	}
	return btn(i18n.T("b.back"), d.cb("back"))
}

func (b *Bot) typeView(d *Draft) (string, *models.InlineKeyboardMarkup) {
	kb := keyboard{
		{btn(i18n.T("type.withdrawal"), d.cb("ty:withdrawal")), btn(i18n.T("type.deposit"), d.cb("ty:deposit")), btn(i18n.T("type.transfer"), d.cb("ty:transfer"))},
	}
	if !d.Guided {
		kb = append(kb, keyboardRow(d.backBtn()))
	}
	return i18n.T("pick.type"), markup(kb)
}

func (b *Bot) promptView(d *Draft) (string, *models.InlineKeyboardMarkup) {
	key := "prompt." + d.Awaiting
	if d.Hint != "" {
		key = d.Hint
	}
	text := i18n.T(key)
	if d.Err != "" {
		text += "\n\n⚠️ " + esc(d.Err)
	}
	kb := keyboard{}
	switch d.Awaiting {
	case "desc", "notes":
		kb = append(kb, keyboardRow(btn(i18n.T("b.clear"), d.cb("clr"))))
	case "file":
		if len(d.Files) > 0 {
			text += "\n" + i18n.T("f.files") + ": " + strconv.Itoa(len(d.Files))
			kb = append(kb, keyboardRow(btn(i18n.T("b.clear_files"), d.cb("clrf"))))
		}
	}
	kb = append(kb, keyboardRow(d.backBtn()))
	return text, markup(kb)
}

// pickView renders the paginated list picker.
func (b *Bot) pickView(d *Draft) (string, *models.InlineKeyboardMarkup) {
	p := d.Pick
	pages := max(1, (len(p.Items)+pageSize-1)/pageSize)
	p.Page = min(max(p.Page, 0), pages-1)

	var s strings.Builder
	s.WriteString(i18n.T("pick." + p.Field))
	if p.Query != "" {
		fmt.Fprintf(&s, "\n%s: <i>%s</i>", i18n.T("pick.query"), esc(p.Query))
	}
	if p.Field == "tags" && len(d.Tags) > 0 {
		fmt.Fprintf(&s, "\n%s: %s", i18n.T("f.tags"), esc(strings.Join(d.Tags, ", ")))
	}
	if len(p.Items) == 0 {
		s.WriteString("\n\n" + i18n.T("pick.empty"))
	} else {
		s.WriteString("\n\n" + i18n.T("pick.hint"))
	}
	if d.Err != "" {
		s.WriteString("\n\n⚠️ " + esc(d.Err))
	}

	var kb keyboard
	start := p.Page * pageSize
	end := min(start+pageSize, len(p.Items))
	var row []models.InlineKeyboardButton
	for i := start; i < end; i++ {
		label := trunc(p.Items[i].Name, 28)
		if b.pickSelected(d, p.Items[i]) {
			label = "✓ " + label
		}
		row = append(row, btn(label, d.cb("pk:"+strconv.Itoa(i))))
		if len(row) == 2 {
			kb = append(kb, row)
			row = nil
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	if pages > 1 {
		prev, next := p.Page-1, p.Page+1
		if prev < 0 {
			prev = pages - 1
		}
		if next >= pages {
			next = 0
		}
		kb = append(kb, keyboardRow(
			btn("‹", d.cb("pg:"+strconv.Itoa(prev))),
			btn(fmt.Sprintf("%d/%d", p.Page+1, pages), d.cb("noop")),
			btn("›", d.cb("pg:"+strconv.Itoa(next))),
		))
	}
	if q := strings.TrimSpace(p.Query); q != "" && freeTextKind(p.Kind) && !exactItem(p.Items, q) {
		kb = append(kb, keyboardRow(btn("➕ "+trunc(q, 40), d.cb("pn"))))
	}
	last := []models.InlineKeyboardButton{}
	if p.Field == "tags" {
		last = append(last, btn(i18n.T("b.done"), d.cb("back")))
	} else {
		last = append(last, btn(i18n.T("b.clear"), d.cb("clr")), d.backBtn())
	}
	kb = append(kb, last)
	return s.String(), markup(kb)
}

func (b *Bot) pickSelected(d *Draft, it Item) bool {
	switch d.Pick.Field {
	case "from":
		return d.Src.Name == it.Name
	case "to":
		return d.Dst.Name == it.Name
	case "category":
		return d.Category == it.Name
	case "budget":
		return d.Budget == it.Name
	case "tags":
		for _, t := range d.Tags {
			if t == it.Name {
				return true
			}
		}
	}
	return false
}

func freeTextKind(kind string) bool {
	return kind == kExpense || kind == kRevenue || kind == kCategory || kind == kTag
}

func exactItem(items []Item, q string) bool {
	for _, it := range items {
		if parse.Normalize(it.Name) == parse.Normalize(q) {
			return true
		}
	}
	return false
}

// --- date & time ---

func (b *Bot) dateView(d *Draft) (string, *models.InlineKeyboardMarkup) {
	month := b.calMonth(d)
	var s strings.Builder
	fmt.Fprintf(&s, "%s: <b>%s</b>\n\n%s", i18n.T("f.date"), esc(fmtWhen(d.When)), i18n.T("date.hint"))
	if d.Err != "" {
		s.WriteString("\n\n⚠️ " + esc(d.Err))
	}

	kb := keyboard{
		{btn(i18n.T("b.now"), d.cb("dt:now")), btn(i18n.T("b.yesterday"), d.cb("dt:1")), btn(i18n.T("b.2days"), d.cb("dt:2"))},
		{
			btn("‹", d.cb("cal:"+month.AddDate(0, -1, 0).Format("2006-01"))),
			btn(i18n.T("mon."+strconv.Itoa(int(month.Month())))+" "+strconv.Itoa(month.Year()), d.cb("noop")),
			btn("›", d.cb("cal:"+month.AddDate(0, 1, 0).Format("2006-01"))),
		},
	}
	var wd []models.InlineKeyboardButton
	for i := 1; i <= 7; i++ {
		wd = append(wd, btn(i18n.T("wd."+strconv.Itoa(i%7)), d.cb("noop")))
	}
	kb = append(kb, wd)

	today := b.now().In(b.loc)
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, b.loc)
	offset := (int(first.Weekday()) + 6) % 7 // Monday-based
	var row []models.InlineKeyboardButton
	for i := 0; i < offset; i++ {
		row = append(row, btn(" ", d.cb("noop")))
	}
	for day := first; day.Month() == month.Month(); day = day.AddDate(0, 0, 1) {
		label := strconv.Itoa(day.Day())
		switch {
		case sameDay(day, d.When):
			label = "[" + label + "]"
		case sameDay(day, today):
			label = "•" + label
		}
		row = append(row, btn(label, d.cb("day:"+day.Format("2006-01-02"))))
		if len(row) == 7 {
			kb = append(kb, row)
			row = nil
		}
	}
	if len(row) > 0 {
		for len(row) < 7 {
			row = append(row, btn(" ", d.cb("noop")))
		}
		kb = append(kb, row)
	}
	kb = append(kb, keyboardRow(btn(i18n.T("b.time"), d.cb("f:time")), btn(i18n.T("b.back"), d.cb("back"))))
	return s.String(), markup(kb)
}

func sameDay(a, c time.Time) bool {
	ay, am, ad := a.Date()
	cy, cm, cd := c.Date()
	return ay == cy && am == cm && ad == cd
}

// calMonth returns the first day of the month shown in the calendar.
func (b *Bot) calMonth(d *Draft) time.Time {
	if t, err := time.ParseInLocation("2006-01", d.CalMonth, b.loc); err == nil {
		return t
	}
	return time.Date(d.When.Year(), d.When.Month(), 1, 0, 0, 0, 0, b.loc)
}

func (b *Bot) timeView(d *Draft) (string, *models.InlineKeyboardMarkup) {
	var s strings.Builder
	fmt.Fprintf(&s, "%s: <b>%s</b>\n\n%s", i18n.T("f.time"), d.When.Format("15:04"), i18n.T("time.hint"))
	if d.Err != "" {
		s.WriteString("\n\n⚠️ " + esc(d.Err))
	}
	kb := keyboard{
		{btn(i18n.T("b.morning"), d.cb("tp:09:00")), btn(i18n.T("b.noon"), d.cb("tp:13:00")), btn(i18n.T("b.evening"), d.cb("tp:19:00"))},
		{btn("−1 "+i18n.T("u.h"), d.cb("ts:-60")), btn("−15 "+i18n.T("u.m"), d.cb("ts:-15")), btn("+15 "+i18n.T("u.m"), d.cb("ts:15")), btn("+1 "+i18n.T("u.h"), d.cb("ts:60"))},
		{btn(i18n.T("b.date"), d.cb("f:date")), btn(i18n.T("b.done"), d.cb("back"))},
	}
	return s.String(), markup(kb)
}
