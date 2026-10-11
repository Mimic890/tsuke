package bot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/parse"
)

func (b *Bot) onCommand(ctx context.Context, m *models.Message) {
	fields := strings.Fields(m.Text)
	cmd := strings.TrimPrefix(fields[0], "/")
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	args := fields[1:]
	chat := m.Chat.ID

	switch strings.ToLower(cmd) {
	case "start":
		b.reply(ctx, chat, i18n.T("start"))
	case "help":
		b.reply(ctx, chat, i18n.T("help"))
	case "new":
		b.cmdNew(ctx, m)
	case "last":
		b.cmdLast(ctx, m, args)
	case "undo":
		b.cmdUndo(ctx, m)
	case "balance":
		b.cmdBalance(ctx, chat)
	case "today":
		b.cmdReport(ctx, chat, "today")
	case "week":
		b.cmdReport(ctx, chat, "week")
	case "month":
		b.cmdReport(ctx, chat, "month")
	case "refresh":
		b.cmdRefresh(ctx, chat)
	case "cancel":
		b.cmdCancel(ctx, m)
	default:
		b.reply(ctx, chat, i18n.T("cmd.unknown"))
	}
}

// cmdNew starts the guided flow.
func (b *Bot) cmdNew(ctx context.Context, m *models.Message) {
	d := b.newDraft(m.From.ID, m.Chat.ID)
	d.Guided = true
	d.Src = b.defaultAsset(ctx)
	b.advance(ctx, d)
}

func (b *Bot) cmdCancel(ctx context.Context, m *models.Message) {
	sd, err := b.st.LatestDraft(ctx, m.From.ID)
	if err != nil {
		b.reply(ctx, m.Chat.ID, i18n.T("cancel.none"))
		return
	}
	d, err := decodeDraft(sd)
	if err != nil {
		b.logErr("decode draft", err)
		return
	}
	b.cancelDraft(ctx, d)
	b.deleteMsg(ctx, m.Chat.ID, m.ID)
}

func (b *Bot) cmdRefresh(ctx context.Context, chat int64) {
	if err := b.RefreshRefs(ctx); err != nil {
		b.reply(ctx, chat, i18n.T("refresh.partial"))
		return
	}
	b.refs.mu.RLock()
	n := len(b.refs.Assets) + len(b.refs.Expenses) + len(b.refs.Revenues)
	c := len(b.refs.Categories)
	b.refs.mu.RUnlock()
	b.reply(ctx, chat, i18n.T("refresh.ok", n, c))
}

// cmdLast lists the latest transactions with buttons that open their cards.
func (b *Bot) cmdLast(ctx context.Context, m *models.Message, args []string) {
	n := 5
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = min(v, 10)
		}
	}
	txs, err := b.ff.ListTransactions(ctx, firefly.ListQuery{Limit: n})
	if err != nil {
		b.logErr("list transactions", err)
		b.reply(ctx, m.Chat.ID, i18n.T("err.firefly_down"))
		return
	}
	if len(txs) == 0 {
		b.reply(ctx, m.Chat.ID, i18n.T("last.empty"))
		return
	}
	var s strings.Builder
	var row []models.InlineKeyboardButton
	for i := range txs {
		tx := &txs[i]
		fmt.Fprintf(&s, "%d. %s · %s · %s\n", i+1, tx.Date.In(b.loc).Format("02.01 15:04"), signed(tx), esc(trunc(tx.Description, 40)))
		row = append(row, btn(strconv.Itoa(i+1), "s:"+tx.GroupID+":op"))
	}
	_, _ = b.send(ctx, m.Chat.ID, s.String(), markup(keyboard{row}))
}

func signed(tx *firefly.Tx) string {
	sign := ""
	switch tx.Type {
	case "withdrawal":
		sign = "−"
	case "deposit":
		sign = "+"
	}
	return sign + txMoney(tx)
}

func (b *Bot) cmdBalance(ctx context.Context, chat int64) {
	accs, err := b.ff.Accounts(ctx, kAsset)
	if err != nil {
		b.logErr("balances", err)
		b.reply(ctx, chat, i18n.T("err.firefly_down"))
		return
	}
	var s strings.Builder
	totals := map[string]int64{}
	var order []string
	for _, a := range accs {
		if !a.Active {
			continue
		}
		c := parse.APIToCents(a.Balance)
		cur := a.Currency
		if cur == "" {
			cur = "₽"
		}
		fmt.Fprintf(&s, "%s: <b>%s</b> %s\n", esc(a.Name), parse.FormatHuman(c), esc(cur))
		if _, ok := totals[cur]; !ok {
			order = append(order, cur)
		}
		totals[cur] += c
	}
	if s.Len() == 0 {
		b.reply(ctx, chat, i18n.T("balance.empty"))
		return
	}
	if len(order) > 0 && len(accs) > 1 {
		s.WriteString("\n" + i18n.T("balance.total") + ": ")
		for i, cur := range order {
			if i > 0 {
				s.WriteString(" · ")
			}
			fmt.Fprintf(&s, "<b>%s</b> %s", parse.FormatHuman(totals[cur]), esc(cur))
		}
	}
	b.reply(ctx, chat, s.String())
}

// periodStart returns the first day (local midnight) of a report period.
func periodStart(period string, now time.Time) time.Time {
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
	switch period {
	case "week":
		return today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	case "month":
		return time.Date(y, mo, 1, 0, 0, 0, 0, now.Location())
	default:
		return today
	}
}

type catTotal struct {
	name  string
	cents int64
}

// spendByCategory sums withdrawals per currency and category.
func spendByCategory(txs []firefly.Tx) (map[string][]catTotal, []string) {
	per := map[string]map[string]int64{}
	var curs []string
	for i := range txs {
		tx := &txs[i]
		if tx.Type != "withdrawal" {
			continue
		}
		cur := tx.Currency
		if cur == "" {
			cur = "₽"
		}
		if per[cur] == nil {
			per[cur] = map[string]int64{}
			curs = append(curs, cur)
		}
		cat := tx.Category
		if cat == "" {
			cat = i18n.T("report.no_category")
		}
		per[cur][cat] += parse.APIToCents(tx.Amount)
	}
	out := map[string][]catTotal{}
	for cur, m := range per {
		for name, c := range m {
			out[cur] = append(out[cur], catTotal{name, c})
		}
		sort.Slice(out[cur], func(i, j int) bool {
			if out[cur][i].cents != out[cur][j].cents {
				return out[cur][i].cents > out[cur][j].cents
			}
			return out[cur][i].name < out[cur][j].name
		})
	}
	return out, curs
}

func (b *Bot) cmdReport(ctx context.Context, chat int64, period string) {
	now := b.now().In(b.loc)
	start := periodStart(period, now)
	txs, err := b.ff.ListTransactions(ctx, firefly.ListQuery{Start: start, End: now, Type: "withdrawal"})
	if err != nil {
		b.logErr("report", err)
		b.reply(ctx, chat, i18n.T("err.firefly_down"))
		return
	}
	per, curs := spendByCategory(txs)
	if len(curs) == 0 {
		b.reply(ctx, chat, i18n.T("report.empty", i18n.T("period."+period)))
		return
	}
	var s strings.Builder
	fmt.Fprintf(&s, "<b>%s</b> (%s – %s)\n", i18n.T("period."+period), start.Format("02.01"), now.Format("02.01"))
	for _, cur := range curs {
		var total int64
		s.WriteString("\n")
		for _, ct := range per[cur] {
			total += ct.cents
			fmt.Fprintf(&s, "%s: %s %s\n", esc(ct.name), parse.FormatHuman(ct.cents), esc(cur))
		}
		fmt.Fprintf(&s, "<b>%s: %s %s</b>\n", i18n.T("report.total"), parse.FormatHuman(total), esc(cur))
	}
	b.reply(ctx, chat, s.String())
}
