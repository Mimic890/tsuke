package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/parse"
	"github.com/Mimic890/tsuke/internal/store"
)

// onMessage routes an incoming private message from an allowed user.
func (b *Bot) onMessage(ctx context.Context, m *models.Message) {
	switch {
	case strings.HasPrefix(m.Text, "/"):
		b.onCommand(ctx, m)
	case len(m.Photo) > 0 || m.Document != nil:
		b.onMedia(ctx, m)
	case strings.TrimSpace(m.Text) != "":
		b.onText(ctx, m)
	}
}

// awaitingDraft returns the draft that currently expects typed text, if fresh.
func (b *Bot) awaitingDraft(ctx context.Context, userID int64) *Draft {
	sd, err := b.st.AwaitingDraft(ctx, userID)
	if err != nil {
		return nil
	}
	if b.now().Sub(sd.UpdatedAt) > awaitTTL {
		return nil
	}
	d, err := decodeDraft(sd)
	if err != nil {
		b.logErr("decode awaiting draft", err)
		return nil
	}
	return d
}

func (b *Bot) onText(ctx context.Context, m *models.Message) {
	text := strings.TrimSpace(m.Text)
	if d := b.awaitingDraft(ctx, m.From.ID); d != nil && d.Awaiting != "file" {
		if b.handleAwaiting(ctx, d, text) {
			b.deleteMsg(ctx, m.Chat.ID, m.ID)
			return
		}
	}
	b.quickInput(ctx, m, text)
}

// quickInput turns a one-line message into a draft card.
func (b *Bot) quickInput(ctx context.Context, m *models.Message, text string) {
	in := parse.Parse(text, b.now().In(b.loc))
	if in.Cents == 0 {
		b.reply(ctx, m.Chat.ID, i18n.T("quick.no_amount"))
		return
	}
	d := b.newDraft(m.From.ID, m.Chat.ID)
	b.applyInput(ctx, d, in)
	b.show(ctx, d)
}

// applyInput fills a draft from parsed quick input, resolving names against the cache.
func (b *Bot) applyInput(ctx context.Context, d *Draft, in parse.Input) {
	var notes []string
	d.Type = in.Type
	d.Cents = in.Cents
	d.Desc = in.Description
	if in.HasWhen {
		d.When = in.When
	}

	assets := b.items(ctx, kAsset)
	asset := func(name string) (Acc, bool) {
		if it, ok := find(assets, name); ok {
			return Acc(it), true
		}
		return Acc{}, false
	}
	def := b.defaultAsset(ctx)

	switch in.Type {
	case parse.Transfer:
		d.Src, d.Dst = def, Acc{}
		hint := in.From
		if hint == "" {
			hint = in.Account
		}
		if hint != "" {
			if a, ok := asset(hint); ok {
				d.Src = a
			} else {
				notes = append(notes, i18n.T("err.account_not_found", hint))
			}
		}
		if in.To != "" {
			if a, ok := asset(in.To); ok {
				d.Dst = a
			} else {
				notes = append(notes, i18n.T("err.account_not_found", in.To))
			}
		}
	case parse.Deposit:
		d.Dst = def
		if in.Account != "" {
			if a, ok := asset(in.Account); ok {
				d.Dst = a
			} else {
				notes = append(notes, i18n.T("err.account_not_found", in.Account))
			}
		}
	default:
		d.Src = def
		if in.Account != "" {
			if a, ok := asset(in.Account); ok {
				d.Src = a
			} else {
				notes = append(notes, i18n.T("err.account_not_found", in.Account))
			}
		}
	}

	if in.Category != "" {
		d.Category = in.Category
		if it, ok := find(b.items(ctx, kCategory), in.Category); ok {
			d.Category = it.Name
		}
	}
	if len(in.Tags) > 0 {
		known := b.items(ctx, kTag)
		for _, t := range in.Tags {
			if it, ok := find(known, t); ok {
				t = it.Name
			}
			d.Tags = appendUnique(d.Tags, t)
		}
	}
	if in.Type == parse.Transfer && !d.Dst.isAsset() && in.To == "" {
		notes = append(notes, i18n.T("err.no_dest"))
	}
	d.Err = strings.Join(notes, "\n")
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// handleAwaiting consumes typed text for the field the draft is waiting on.
// It returns false when the text should be treated as new quick input instead.
func (b *Bot) handleAwaiting(ctx context.Context, d *Draft, text string) bool {
	switch d.Awaiting {
	case "amount":
		c, _, err := parse.ParseAmount(text)
		if err != nil {
			// "350 кофе" while an old amount prompt is open: start a new draft.
			if parse.Parse(text, b.now().In(b.loc)).Cents > 0 {
				return false
			}
			d.Err = i18n.T("err.bad_amount")
			b.show(ctx, d)
			return true
		}
		d.Cents = c
		b.afterSet(ctx, d)
	case "desc":
		d.Desc = text
		b.afterSet(ctx, d)
	case "notes":
		d.Notes = text
		b.afterSet(ctx, d)
	case "when":
		t, ok := parse.ParseWhenBase(text, b.now().In(b.loc), d.When)
		if !ok {
			d.Err = i18n.T("err.bad_when")
			b.show(ctx, d)
			return true
		}
		d.When, d.Err = t, ""
		b.afterSet(ctx, d)
	case "pick":
		d.Pick.Query, d.Pick.Page, d.Err = text, 0, ""
		d.Pick.Items = search(b.items(ctx, d.Pick.Kind), text)
		b.show(ctx, d)
	default:
		return false
	}
	return true
}

// afterSet leaves the field screen after a value was set.
func (b *Bot) afterSet(ctx context.Context, d *Draft) {
	d.Awaiting, d.Hint, d.Pick, d.Err = "", "", nil, ""
	if d.Guided {
		b.advance(ctx, d)
		return
	}
	d.Screen = scCard
	b.show(ctx, d)
}

var guidedSteps = []string{"type", "amount", "from", "to", "category", "desc"}

// advance opens the next step of the guided /new flow.
func (b *Bot) advance(ctx context.Context, d *Draft) {
	for d.Step < len(guidedSteps) {
		field := guidedSteps[d.Step]
		d.Step++
		if field == "category" && d.Type == "transfer" {
			continue
		}
		b.openField(ctx, d, field)
		return
	}
	d.Guided, d.Screen = false, scCard
	b.show(ctx, d)
}

// openField switches the card to the editor of one field.
func (b *Bot) openField(ctx context.Context, d *Draft, field string) {
	d.Err, d.Hint, d.Awaiting, d.Pick = "", "", "", nil
	switch field {
	case "type":
		d.Screen = scType
	case "amount", "desc", "notes", "file":
		d.Screen, d.Awaiting = scPrompt, field
	case "from", "to", "category", "budget", "tags":
		b.openPicker(ctx, d, field)
		return
	case "date":
		d.Screen, d.Awaiting, d.CalMonth = scDate, "when", ""
	case "time":
		d.Screen, d.Awaiting = scTime, "when"
	default:
		d.Screen = scCard
	}
	b.show(ctx, d)
}

func pickKind(d *Draft, field string) string {
	switch field {
	case "from":
		if d.Type == "deposit" {
			return kRevenue
		}
		return kAsset
	case "to":
		if d.Type == "withdrawal" {
			return kExpense
		}
		return kAsset
	case "category":
		return kCategory
	case "budget":
		return kBudget
	default:
		return kTag
	}
}

func (b *Bot) openPicker(ctx context.Context, d *Draft, field string) {
	kind := pickKind(d, field)
	d.Screen, d.Awaiting = scPick, "pick"
	d.Pick = &Picker{Field: field, Kind: kind, Items: b.items(ctx, kind)}
	b.show(ctx, d)
}

// onCallback routes inline button presses.
func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	msg := q.Message.Message
	if msg == nil {
		b.answer(ctx, q.ID, "", false)
		return
	}
	parts := strings.SplitN(q.Data, ":", 3)
	if len(parts) < 2 {
		b.answer(ctx, q.ID, "", false)
		return
	}
	switch parts[0] {
	case "d":
		if len(parts) < 3 {
			b.answer(ctx, q.ID, "", false)
			return
		}
		b.onDraftCallback(ctx, q, msg, parts[1], parts[2])
	case "s":
		if len(parts) < 3 {
			b.answer(ctx, q.ID, "", false)
			return
		}
		b.onSavedCallback(ctx, q, msg, parts[1], parts[2])
	case "u":
		if len(parts) < 3 {
			b.answer(ctx, q.ID, "", false)
			return
		}
		b.onUndoCallback(ctx, q, msg, parts[1], parts[2])
	default:
		b.answer(ctx, q.ID, "", false)
	}
}

func (b *Bot) onDraftCallback(ctx context.Context, q *models.CallbackQuery, msg *models.Message, id, rest string) {
	cmd, arg, _ := strings.Cut(rest, ":")
	if cmd == "noop" {
		b.answer(ctx, q.ID, "", false)
		return
	}
	d, err := b.loadDraft(ctx, id)
	if err != nil || d.UserID != q.From.ID {
		b.answer(ctx, q.ID, i18n.T("err.draft_gone"), true)
		if errors.Is(err, errDraftGone) {
			_ = b.edit(ctx, msg.Chat.ID, msg.ID, i18n.T("err.draft_gone"), nil)
		}
		return
	}
	d.ChatID, d.MsgID = msg.Chat.ID, msg.ID

	switch cmd {
	case "sv":
		b.answer(ctx, q.ID, "", false)
		b.save(ctx, d)
		return
	case "cx":
		b.answer(ctx, q.ID, "", false)
		b.cancelDraft(ctx, d)
		return
	}
	b.answer(ctx, q.ID, "", false)

	switch cmd {
	case "f":
		b.openField(ctx, d, arg)
	case "back":
		b.afterSet(ctx, d)
	case "ty":
		b.setType(ctx, d, arg)
		b.afterSet(ctx, d)
	case "pg":
		if d.Pick != nil {
			d.Pick.Page, _ = strconv.Atoi(arg)
			b.show(ctx, d)
		}
	case "pk":
		b.pick(ctx, d, arg)
	case "pn":
		b.pickNew(ctx, d)
	case "clr":
		b.clearField(ctx, d)
	case "clrf":
		b.removeFiles(d.ID)
		d.Files = nil
		b.show(ctx, d)
	case "dt", "day", "cal", "tp", "ts":
		b.dateTime(ctx, d, cmd, arg)
	default:
		b.log.Debug("unknown draft callback", "cmd", cmd)
	}
}

func (b *Bot) pick(ctx context.Context, d *Draft, arg string) {
	i, err := strconv.Atoi(arg)
	if d.Pick == nil || err != nil || i < 0 || i >= len(d.Pick.Items) {
		return
	}
	it := d.Pick.Items[i]
	switch d.Pick.Field {
	case "from":
		d.Src = Acc{Name: it.Name}
		if d.Pick.Kind == kAsset {
			d.Src.ID = it.ID
		}
	case "to":
		d.Dst = Acc{Name: it.Name}
		if d.Pick.Kind == kAsset {
			d.Dst.ID = it.ID
		}
	case "category":
		d.Category = it.Name
	case "budget":
		d.Budget, d.BudgetID = it.Name, it.ID
	case "tags":
		d.toggleTag(it.Name)
		b.show(ctx, d)
		return
	}
	b.afterSet(ctx, d)
}

func (d *Draft) toggleTag(name string) {
	for i, t := range d.Tags {
		if t == name {
			d.Tags = append(d.Tags[:i:i], d.Tags[i+1:]...)
			return
		}
	}
	d.Tags = append(d.Tags, name)
}

// pickNew uses the typed search text as a brand-new value (payee, category, tag).
func (b *Bot) pickNew(ctx context.Context, d *Draft) {
	if d.Pick == nil || strings.TrimSpace(d.Pick.Query) == "" || !freeTextKind(d.Pick.Kind) {
		return
	}
	v := strings.TrimSpace(d.Pick.Query)
	switch d.Pick.Field {
	case "from":
		d.Src = Acc{Name: v}
	case "to":
		d.Dst = Acc{Name: v}
	case "category":
		d.Category = v
	case "tags":
		d.Tags = appendUnique(d.Tags, v)
		d.Pick.Query, d.Pick.Page = "", 0
		d.Pick.Items = b.items(ctx, kTag)
		b.show(ctx, d)
		return
	}
	b.afterSet(ctx, d)
}

func (b *Bot) clearField(ctx context.Context, d *Draft) {
	field := d.Awaiting
	if d.Pick != nil {
		field = d.Pick.Field
	}
	switch field {
	case "desc":
		d.Desc = ""
	case "notes":
		d.Notes = ""
	case "from":
		d.Src = Acc{}
	case "to":
		d.Dst = Acc{}
	case "category":
		d.Category = ""
	case "budget":
		d.Budget, d.BudgetID = "", ""
	}
	b.afterSet(ctx, d)
}

// dateTime handles the date and time screens.
func (b *Bot) dateTime(ctx context.Context, d *Draft, cmd, arg string) {
	now := b.now().In(b.loc)
	d.Err = ""
	setDay := func(day time.Time) {
		d.When = time.Date(day.Year(), day.Month(), day.Day(), d.When.Hour(), d.When.Minute(), 0, 0, b.loc)
	}
	switch cmd {
	case "dt":
		switch arg {
		case "now":
			d.When = now.Truncate(time.Minute)
		default:
			n, _ := strconv.Atoi(arg)
			setDay(now.AddDate(0, 0, -n))
		}
		b.afterSet(ctx, d)
	case "day":
		day, err := time.ParseInLocation("2006-01-02", arg, b.loc)
		if err != nil {
			return
		}
		setDay(day)
		b.afterSet(ctx, d)
	case "cal":
		if _, err := time.Parse("2006-01", arg); err == nil {
			d.CalMonth = arg
		}
		b.show(ctx, d)
	case "tp":
		var h, m int
		if _, err := fmtSscanTime(arg, &h, &m); err == nil {
			d.When = time.Date(d.When.Year(), d.When.Month(), d.When.Day(), h, m, 0, 0, b.loc)
		}
		b.show(ctx, d)
	case "ts":
		delta, err := strconv.Atoi(arg)
		if err != nil {
			return
		}
		mins := (d.When.Hour()*60 + d.When.Minute() + delta%1440 + 1440) % 1440
		d.When = time.Date(d.When.Year(), d.When.Month(), d.When.Day(), mins/60, mins%60, 0, 0, b.loc)
		b.show(ctx, d)
	}
}

func fmtSscanTime(s string, h, m *int) (int, error) {
	hs, ms, ok := strings.Cut(s, ":")
	if !ok {
		return 0, errors.New("bad time")
	}
	var err error
	if *h, err = strconv.Atoi(hs); err != nil {
		return 0, err
	}
	if *m, err = strconv.Atoi(ms); err != nil {
		return 0, err
	}
	return 2, nil
}

func (b *Bot) cancelDraft(ctx context.Context, d *Draft) {
	editing := d.EditGroup
	b.deleteDraft(ctx, d)
	if editing != "" {
		b.showSaved(ctx, d.ChatID, d.MsgID, editing, "")
		return
	}
	b.put(ctx, d.ChatID, d.MsgID, i18n.T("draft.cancelled"), nil)
}

// save validates the draft and creates (or updates) the transaction in Firefly.
func (b *Bot) save(ctx context.Context, d *Draft) {
	if p := d.problems(); len(p) > 0 {
		d.Err = strings.Join(p, "\n")
		d.Screen, d.Awaiting, d.Pick = scCard, "", nil
		b.show(ctx, d)
		return
	}
	tx := d.toTx(b.loc)

	var saved *firefly.Tx
	var err error
	if d.EditGroup != "" {
		saved, err = b.ff.UpdateTransaction(ctx, d.EditGroup, tx)
	} else {
		saved, err = b.ff.CreateTransaction(ctx, tx)
	}
	if err != nil {
		d.Err = b.humanError(err)
		d.Screen, d.Awaiting, d.Pick = scCard, "", nil
		b.logErr("save transaction", err)
		b.show(ctx, d)
		return
	}

	uploaded, failed := b.uploadFiles(ctx, d, saved.JournalID)
	b.recordUsage(ctx, d)
	if err := b.st.PutCreated(ctx, store.Created{
		GroupID: saved.GroupID, UserID: d.UserID, ChatID: d.ChatID, MessageID: d.MsgID,
		Summary: b.summary(saved),
	}); err != nil {
		b.logErr("record created", err)
	}
	b.deleteDraft(ctx, d)

	note := ""
	if uploaded > 0 {
		note = i18n.T("saved.attached", uploaded)
	}
	if failed > 0 {
		note += "\n⚠️ " + i18n.T("saved.attach_failed", failed)
	}
	b.showSavedTx(ctx, d.ChatID, d.MsgID, saved, strings.TrimSpace(note))
}

// humanError renders an API or network failure for the card.
func (b *Bot) humanError(err error) string {
	var ae *firefly.APIError
	if errors.As(err, &ae) {
		if ae.IsDuplicate() {
			return i18n.T("err.duplicate") + "\n" + ae.Human()
		}
		return ae.Human()
	}
	return i18n.T("err.firefly_down")
}

func (b *Bot) recordUsage(ctx context.Context, d *Draft) {
	bump := func(kind, name string) {
		if err := b.st.Bump(ctx, kind, name); err != nil {
			b.logErr("bump usage", err)
		}
	}
	switch d.Type {
	case "withdrawal":
		bump(kAsset, d.Src.Name)
		bump(kExpense, d.Dst.Name)
	case "deposit":
		bump(kRevenue, d.Src.Name)
		bump(kAsset, d.Dst.Name)
	default:
		bump(kAsset, d.Src.Name)
		bump(kAsset, d.Dst.Name)
	}
	bump(kCategory, d.Category)
	bump(kBudget, d.Budget)
	for _, t := range d.Tags {
		bump(kTag, t)
	}
}
