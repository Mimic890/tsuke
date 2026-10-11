package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/parse"
	"github.com/Mimic890/tsuke/internal/store"
)

func (b *Bot) txLink(groupID string) string {
	return fmt.Sprintf("%s/transactions/show/%s", b.cfg.FireflyPublic, groupID)
}

func txMoney(tx *firefly.Tx) string {
	cur := tx.Currency
	if cur == "" {
		cur = "₽"
	}
	c := parse.APIToCents(tx.Amount)
	if c < 0 {
		c = -c
	}
	return parse.FormatHuman(c) + " " + cur
}

// summary is a one-line description stored for history.
func (b *Bot) summary(tx *firefly.Tx) string {
	return fmt.Sprintf("%s · %s · %s", tx.Date.In(b.loc).Format("02.01 15:04"), txMoney(tx), tx.Description)
}

// savedView renders a saved transaction with Edit / Delete buttons.
func (b *Bot) savedView(tx *firefly.Tx, note string) (string, *models.InlineKeyboardMarkup) {
	var s strings.Builder
	fmt.Fprintf(&s, "<b>%s</b> · %s %s\n\n", i18n.T("card.saved"), esc(i18n.T("type."+tx.Type)), txMoney(tx))
	line := func(key, val string) { fmt.Fprintf(&s, "%s: %s\n", i18n.T(key), val) }
	line("f.from", orDash(tx.SourceName))
	line("f.to", orDash(tx.DestName))
	line("f.category", orDash(tx.Category))
	if tx.Budget != "" {
		line("f.budget", orDash(tx.Budget))
	}
	line("f.tags", orDash(strings.Join(tx.Tags, ", ")))
	line("f.desc", orDash(tx.Description))
	if tx.Notes != "" {
		line("f.notes", orDash(tx.Notes))
	}
	line("f.date", esc(fmtWhen(tx.Date.In(b.loc))))
	if note != "" {
		fmt.Fprintf(&s, "\n%s\n", esc(note))
	}
	fmt.Fprintf(&s, "\n<a href=\"%s\">%s</a>", esc(b.txLink(tx.GroupID)), i18n.T("card.open"))

	kb := keyboard{{
		btn(i18n.T("b.edit"), "s:"+tx.GroupID+":ed"),
		btn(i18n.T("b.delete"), "s:"+tx.GroupID+":dl"),
	}}
	return s.String(), markup(kb)
}

// showSavedTx renders a saved transaction into a message.
func (b *Bot) showSavedTx(ctx context.Context, chat int64, msg int, tx *firefly.Tx, note string) {
	text, kb := b.savedView(tx, note)
	id := b.put(ctx, chat, msg, text, kb)
	if id != 0 {
		if err := b.st.PutCard(ctx, chat, id, tx.GroupID); err != nil {
			b.logErr("remember card", err)
		}
	}
}

// showSaved fetches a transaction and renders it.
func (b *Bot) showSaved(ctx context.Context, chat int64, msg int, groupID, note string) {
	tx, err := b.ff.Transaction(ctx, groupID)
	if err != nil {
		b.logErr("load transaction", err)
		key := "err.firefly_down"
		if errors.Is(err, firefly.ErrNotFound) {
			key = "err.tx_gone"
		}
		b.put(ctx, chat, msg, i18n.T(key), nil)
		return
	}
	b.showSavedTx(ctx, chat, msg, tx, note)
}

func (b *Bot) onSavedCallback(ctx context.Context, q *models.CallbackQuery, msg *models.Message, group, cmd string) {
	b.answer(ctx, q.ID, "", false)
	chat := msg.Chat.ID
	switch cmd {
	case "ed":
		tx, err := b.ff.Transaction(ctx, group)
		if err != nil {
			b.logErr("load transaction", err)
			b.reply(ctx, chat, i18n.T("err.firefly_down"))
			return
		}
		d := b.draftFromTx(q.From.ID, chat, msg.ID, tx)
		b.show(ctx, d)
	case "dl":
		kb := keyboard{{
			btn(i18n.T("b.yes_delete"), "s:"+group+":dy"),
			btn(i18n.T("b.no"), "s:"+group+":dn"),
		}}
		b.put(ctx, chat, msg.ID, i18n.T("confirm.delete"), markup(kb))
	case "dn":
		b.showSaved(ctx, chat, msg.ID, group, "")
	case "dy":
		if err := b.ff.DeleteTransaction(ctx, group); err != nil {
			b.logErr("delete transaction", err)
			b.put(ctx, chat, msg.ID, i18n.T("err.firefly_down"), nil)
			return
		}
		if err := b.st.DeleteCreated(ctx, group); err != nil {
			b.logErr("forget created", err)
		}
		b.put(ctx, chat, msg.ID, i18n.T("card.deleted"), nil)
	case "op":
		b.showSaved(ctx, chat, 0, group, "")
	}
}

// cmdUndo offers to delete the last transaction created by the bot.
func (b *Bot) cmdUndo(ctx context.Context, m *models.Message) {
	last, err := b.st.LastCreated(ctx, m.From.ID)
	if errors.Is(err, store.ErrNotFound) {
		b.reply(ctx, m.Chat.ID, i18n.T("undo.none"))
		return
	}
	if err != nil {
		b.logErr("last created", err)
		return
	}
	tx, err := b.ff.Transaction(ctx, last.GroupID)
	switch {
	case errors.Is(err, firefly.ErrNotFound):
		_ = b.st.DeleteCreated(ctx, last.GroupID)
		b.reply(ctx, m.Chat.ID, i18n.T("undo.gone"))
		return
	case err != nil:
		b.logErr("load transaction", err)
		b.reply(ctx, m.Chat.ID, i18n.T("err.firefly_down"))
		return
	}
	kb := keyboard{{
		btn(i18n.T("b.yes_delete"), "u:"+last.GroupID+":y"),
		btn(i18n.T("b.no"), "u:"+last.GroupID+":n"),
	}}
	_, _ = b.send(ctx, m.Chat.ID, i18n.T("undo.confirm", esc(b.summary(tx))), markup(kb))
}

func (b *Bot) onUndoCallback(ctx context.Context, q *models.CallbackQuery, msg *models.Message, group, ans string) {
	b.answer(ctx, q.ID, "", false)
	if ans != "y" {
		b.put(ctx, msg.Chat.ID, msg.ID, i18n.T("draft.cancelled"), nil)
		return
	}
	if err := b.ff.DeleteTransaction(ctx, group); err != nil {
		b.logErr("undo delete", err)
		b.put(ctx, msg.Chat.ID, msg.ID, i18n.T("err.firefly_down"), nil)
		return
	}
	if err := b.st.DeleteCreated(ctx, group); err != nil {
		b.logErr("forget created", err)
	}
	b.put(ctx, msg.Chat.ID, msg.ID, i18n.T("card.deleted"), nil)
}
