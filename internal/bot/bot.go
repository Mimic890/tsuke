// Package bot implements the Telegram side of tsuke: routing, cards and flows.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Mimic890/tsuke/internal/config"
	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/receipt"
	"github.com/Mimic890/tsuke/internal/store"
)

// draftTTL is how long an untouched draft survives.
const draftTTL = 7 * 24 * time.Hour

// awaitTTL is how long a draft keeps swallowing typed text.
const awaitTTL = 15 * time.Minute

// Bot wires Telegram, Firefly III and the local store together.
type Bot struct {
	cfg      *config.Config
	tg       *tgbot.Bot
	ff       *firefly.Client
	st       *store.Store
	log      *slog.Logger
	loc      *time.Location
	now      func() time.Time
	refs     *refs
	filesDir string
	http     *http.Client
	enricher receipt.Enricher // optional receipt line-item source; nil in v1

	mu     sync.Mutex // serializes update handling
	albums map[string]*album
}

// Options configures New.
type Options struct {
	Config  *config.Config
	Firefly *firefly.Client
	Store   *store.Store
	Logger  *slog.Logger
	// TelegramURL overrides the Bot API server (tests).
	TelegramURL string
}

// New creates the bot. It does not start receiving updates.
func New(o Options) (*Bot, error) {
	b := &Bot{
		cfg: o.Config, ff: o.Firefly, st: o.Store, log: o.Logger,
		loc: o.Config.Location, now: time.Now, refs: &refs{},
		filesDir: filepath.Join(o.Config.DataDir, "files"),
		http:     &http.Client{Timeout: 60 * time.Second},
		albums:   map[string]*album{},
	}
	if err := os.MkdirAll(b.filesDir, 0o750); err != nil {
		return nil, err
	}
	opts := []tgbot.Option{
		tgbot.WithDefaultHandler(b.onUpdate),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithErrorsHandler(func(err error) { b.logErr("telegram", err) }),
		tgbot.WithNotAsyncHandlers(),
	}
	if o.TelegramURL != "" {
		opts = append(opts, tgbot.WithServerURL(o.TelegramURL), tgbot.WithSkipGetMe())
	}
	if o.Config.WebhookSecret != "" {
		opts = append(opts, tgbot.WithWebhookSecretToken(o.Config.WebhookSecret))
	}
	tg, err := tgbot.New(o.Config.TelegramToken, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", stripToken(err, o.Config.TelegramToken))
	}
	b.tg = tg
	return b, nil
}

func stripToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}

func (b *Bot) logErr(msg string, err error) {
	b.log.Error(msg, "err", stripToken(err, b.cfg.TelegramToken))
}

// Run starts receiving updates (long polling, or a webhook when configured)
// and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	b.loadRefsFromStore(ctx)
	go b.background(ctx)

	if _, err := b.tg.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: commandList()}); err != nil {
		b.logErr("set commands", err)
	}

	if b.cfg.WebhookURL == "" {
		if _, err := b.tg.DeleteWebhook(ctx, &tgbot.DeleteWebhookParams{}); err != nil {
			b.logErr("delete webhook", err)
		}
		b.log.Info("started", "mode", "long-polling")
		b.tg.Start(ctx)
		return nil
	}

	hook := b.cfg.WebhookURL + b.cfg.WebhookPath
	if _, err := b.tg.SetWebhook(ctx, &tgbot.SetWebhookParams{
		URL: hook, SecretToken: b.cfg.WebhookSecret, AllowedUpdates: []string{"message", "callback_query"},
	}); err != nil {
		return fmt.Errorf("set webhook: %w", stripToken(err, b.cfg.TelegramToken))
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+b.cfg.WebhookPath, b.tg.WebhookHandler())
	srv := &http.Server{Addr: b.cfg.WebhookListen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go b.tg.StartWebhook(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	b.log.Info("started", "mode", "webhook", "listen", b.cfg.WebhookListen, "path", b.cfg.WebhookPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// background refreshes caches periodically and purges stale drafts.
func (b *Bot) background(ctx context.Context) {
	refresh := func() {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := b.RefreshRefs(rctx); err != nil {
			b.log.Warn("cache refresh incomplete", "err", err)
		}
	}
	purge := func() {
		ids, err := b.st.PurgeDrafts(ctx, b.now().Add(-draftTTL))
		if err != nil {
			b.logErr("purge drafts", err)
			return
		}
		for _, id := range ids {
			b.removeFiles(id)
		}
	}
	refresh()
	purge()
	rt := time.NewTicker(b.cfg.CacheRefresh)
	pt := time.NewTicker(6 * time.Hour)
	defer rt.Stop()
	defer pt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.C:
			refresh()
		case <-pt.C:
			purge()
		}
	}
}

func (b *Bot) allowed(id int64) bool {
	_, ok := b.cfg.AllowedUserIDs[id]
	return ok
}

// Handle processes one update. Exported for tests.
func (b *Bot) Handle(ctx context.Context, u *models.Update) { b.onUpdate(ctx, b.tg, u) }

func (b *Bot) onUpdate(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in handler", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case u.CallbackQuery != nil:
		q := u.CallbackQuery
		if !b.allowed(q.From.ID) {
			return // strangers get no reply at all
		}
		b.onCallback(ctx, q)
	case u.Message != nil:
		m := u.Message
		if m.From == nil || !b.allowed(m.From.ID) || m.Chat.Type != models.ChatTypePrivate {
			return
		}
		b.onMessage(ctx, m)
	}
}

// --- Telegram helpers ---

func (b *Bot) send(ctx context.Context, chat int64, text string, kb *models.InlineKeyboardMarkup) (int, error) {
	p := &tgbot.SendMessageParams{
		ChatID: chat, Text: text, ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
	}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	m, err := b.tg.SendMessage(ctx, p)
	if err != nil {
		b.logErr("send message", err)
		return 0, err
	}
	return m.ID, nil
}

func (b *Bot) reply(ctx context.Context, chat int64, text string) {
	_, _ = b.send(ctx, chat, text, nil)
}

func (b *Bot) edit(ctx context.Context, chat int64, msg int, text string, kb *models.InlineKeyboardMarkup) error {
	if kb == nil {
		kb = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	}
	_, err := b.tg.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID: chat, MessageID: msg, Text: text, ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
		ReplyMarkup:        kb,
	})
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	if err != nil {
		b.logErr("edit message", err)
	}
	return err
}

// put shows text in the given message, falling back to a new message when it is gone.
// It returns the id of the message that now holds the text.
func (b *Bot) put(ctx context.Context, chat int64, msg int, text string, kb *models.InlineKeyboardMarkup) int {
	if msg != 0 {
		if err := b.edit(ctx, chat, msg, text, kb); err == nil {
			return msg
		}
	}
	id, _ := b.send(ctx, chat, text, kb)
	return id
}

func (b *Bot) answer(ctx context.Context, id, text string, alert bool) {
	if _, err := b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, ShowAlert: alert}); err != nil {
		b.logErr("answer callback", err)
	}
}

func (b *Bot) deleteMsg(ctx context.Context, chat int64, msg int) {
	if _, err := b.tg.DeleteMessage(ctx, &tgbot.DeleteMessageParams{ChatID: chat, MessageID: msg}); err != nil {
		b.log.Debug("delete message", "err", stripToken(err, b.cfg.TelegramToken))
	}
}

// show persists the draft and renders its current screen into its card message.
func (b *Bot) show(ctx context.Context, d *Draft) {
	if err := b.saveDraft(ctx, d); err != nil {
		b.logErr("save draft", err)
	}
	text, kb := b.view(d)
	id := b.put(ctx, d.ChatID, d.MsgID, text, kb)
	if id != 0 && id != d.MsgID {
		d.MsgID = id
		if err := b.saveDraft(ctx, d); err != nil {
			b.logErr("save draft", err)
		}
	}
}

func commandList() []models.BotCommand {
	var out []models.BotCommand
	for _, c := range []string{"new", "last", "undo", "balance", "today", "week", "month", "refresh", "cancel", "help"} {
		out = append(out, models.BotCommand{Command: c, Description: i18n.T("cmd." + c)})
	}
	return out
}
