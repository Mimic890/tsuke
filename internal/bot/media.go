package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/parse"
	"github.com/Mimic890/tsuke/internal/receipt"
	"github.com/Mimic890/tsuke/internal/store"
)

const (
	albumWait   = 1500 * time.Millisecond
	maxFileSize = 20 << 20 // Telegram Bot API download limit
)

// incoming is a file sent to the bot, before it is downloaded.
type incoming struct {
	fileID string
	name   string
}

// album collects the messages of a media group.
type album struct {
	userID  int64
	chatID  int64
	files   []incoming
	caption string
	replyTo int // id of the message replied to, 0 if none
	timer   *time.Timer
}

// onMedia handles a photo or document message, batching albums.
func (b *Bot) onMedia(ctx context.Context, m *models.Message) {
	f, ok := incomingFile(m)
	if !ok {
		b.reply(ctx, m.Chat.ID, i18n.T("media.unsupported"))
		return
	}
	reply := 0
	if m.ReplyToMessage != nil {
		reply = m.ReplyToMessage.ID
	}
	if m.MediaGroupID == "" {
		b.processMedia(ctx, &album{userID: m.From.ID, chatID: m.Chat.ID, files: []incoming{f}, caption: m.Caption, replyTo: reply})
		return
	}
	a, ok := b.albums[m.MediaGroupID]
	if !ok {
		a = &album{userID: m.From.ID, chatID: m.Chat.ID}
		b.albums[m.MediaGroupID] = a
	}
	a.files = append(a.files, f)
	if a.caption == "" {
		a.caption = m.Caption
	}
	if a.replyTo == 0 {
		a.replyTo = reply
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	groupID := m.MediaGroupID
	a.timer = time.AfterFunc(albumWait, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		a, ok := b.albums[groupID]
		if !ok {
			return
		}
		delete(b.albums, groupID)
		defer func() {
			if r := recover(); r != nil {
				b.log.Error("panic in album handler", "panic", fmt.Sprint(r))
			}
		}()
		b.processMedia(context.Background(), a)
	})
}

func incomingFile(m *models.Message) (incoming, bool) {
	if n := len(m.Photo); n > 0 {
		p := m.Photo[n-1]
		return incoming{fileID: p.FileID, name: "photo_" + p.FileUniqueID + ".jpg"}, true
	}
	if d := m.Document; d != nil {
		switch {
		case strings.HasPrefix(d.MimeType, "image/"), d.MimeType == "application/pdf":
		case d.MimeType == "":
			if ext := strings.ToLower(filepath.Ext(d.FileName)); ext != ".pdf" && ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
				return incoming{}, false
			}
		default:
			return incoming{}, false
		}
		name := d.FileName
		if name == "" {
			name = "file_" + d.FileUniqueID
		}
		return incoming{fileID: d.FileID, name: name}, true
	}
	return incoming{}, false
}

// processMedia downloads the files, then either attaches them to a saved
// transaction (reply) or starts a draft prefilled from the receipt QR code.
func (b *Bot) processMedia(ctx context.Context, a *album) {
	// Awaiting an explicit "add file" on an open draft?
	var target *Draft
	if a.replyTo == 0 {
		if d := b.awaitingDraft(ctx, a.userID); d != nil && d.Awaiting == "file" {
			target = d
		}
	}

	draftID := newID()
	if target != nil {
		draftID = target.ID
	}
	files, blobs, err := b.download(ctx, draftID, a.files, len(targetFiles(target)))
	if err != nil || len(files) == 0 {
		if err != nil {
			b.logErr("download file", err)
		}
		b.removeIfNew(draftID, target)
		b.reply(ctx, a.chatID, i18n.T("media.download_failed"))
		return
	}

	if a.replyTo != 0 {
		if group, err := b.st.GroupByCard(ctx, a.chatID, a.replyTo); err == nil {
			b.attachToSaved(ctx, a.chatID, group, files, blobs)
			b.removeFiles(draftID)
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			b.logErr("lookup card", err)
		}
	}

	if target != nil {
		target.Files = append(target.Files, files...)
		b.applyReceipt(target, blobs, target.Cents > 0)
		target.Awaiting, target.Hint = "", ""
		target.Screen = scCard
		b.show(ctx, target)
		return
	}

	d := b.newDraft(a.userID, a.chatID)
	d.ID = draftID
	d.Files = files
	if strings.TrimSpace(a.caption) != "" {
		in := parse.Parse(a.caption, b.now().In(b.loc))
		b.applyInput(ctx, d, in)
	} else {
		d.Src = b.defaultAsset(ctx)
	}
	captionHasAmount := d.Cents > 0
	found := b.applyReceipt(d, blobs, captionHasAmount)
	if d.Cents == 0 {
		d.Screen, d.Awaiting = scPrompt, "amount"
		d.Hint = "prompt.amount_noqr"
		if found {
			d.Hint = ""
		}
	}
	b.show(ctx, d)
}

func targetFiles(d *Draft) []File {
	if d == nil {
		return nil
	}
	return d.Files
}

func (b *Bot) removeIfNew(id string, target *Draft) {
	if target == nil {
		b.removeFiles(id)
	}
}

// applyReceipt looks for a fiscal QR in the files and prefills the draft.
// With keepAmount, an amount already present (from the caption) wins.
func (b *Bot) applyReceipt(d *Draft, blobs [][]byte, keepAmount bool) bool {
	for _, data := range blobs {
		payload, err := receipt.DecodeQR(data)
		if err != nil {
			continue
		}
		f, err := receipt.ParseQR(payload, b.loc)
		if err != nil {
			continue
		}
		d.Fiscal = &f
		if !keepAmount || d.Cents == 0 {
			d.Cents = f.Cents
			d.Type = f.Type()
			if d.Type == "deposit" {
				d.Dst, d.Src = d.Src, Acc{}
			}
		}
		d.When = f.When.In(b.loc)
		line := i18n.T("receipt.ref", f.Ref())
		if !strings.Contains(d.Notes, f.Ref()) {
			d.Notes = strings.TrimSpace(d.Notes + "\n" + line)
		}
		if b.enricher != nil {
			if items, err := b.enricher.Items(f); err != nil {
				b.logErr("receipt enrich", err)
			} else {
				b.log.Debug("receipt items", "count", len(items))
			}
		}
		return true
	}
	return false
}

// download fetches Telegram files into the draft's directory and returns their bytes.
func (b *Bot) download(ctx context.Context, draftID string, in []incoming, existing int) ([]File, [][]byte, error) {
	dir := filepath.Join(b.filesDir, draftID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, err
	}
	var files []File
	var blobs [][]byte
	for i, f := range in {
		data, err := b.fetch(ctx, f.fileID)
		if err != nil {
			return files, blobs, err
		}
		name := safeName(f.name)
		rel := filepath.Join(draftID, fmt.Sprintf("%d_%s", existing+i+1, name))
		if err := os.WriteFile(filepath.Join(b.filesDir, rel), data, 0o600); err != nil {
			return files, blobs, err
		}
		files = append(files, File{Path: rel, Name: name})
		blobs = append(blobs, data)
	}
	return files, blobs, nil
}

func safeName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, n)
	if n == "" || n == "." || n == ".." {
		return "file"
	}
	return n
}

func (b *Bot) fetch(ctx context.Context, fileID string) ([]byte, error) {
	f, err := b.tg.GetFile(ctx, &tgbot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, stripToken(err, b.cfg.TelegramToken)
	}
	if f.FileSize > maxFileSize {
		return nil, errors.New("file too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.tg.FileDownloadLink(f), nil)
	if err != nil {
		return nil, stripToken(err, b.cfg.TelegramToken)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, stripToken(err, b.cfg.TelegramToken)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileSize+1))
	if err != nil {
		return nil, stripToken(err, b.cfg.TelegramToken)
	}
	if len(data) > maxFileSize {
		return nil, errors.New("file too large")
	}
	return data, nil
}

// attachToSaved uploads files to an already saved transaction.
func (b *Bot) attachToSaved(ctx context.Context, chat int64, group string, files []File, blobs [][]byte) {
	tx, err := b.ff.Transaction(ctx, group)
	if err != nil {
		b.logErr("load transaction for attachment", err)
		b.reply(ctx, chat, i18n.T("err.firefly_down"))
		return
	}
	ok, failed := 0, 0
	for i, f := range files {
		if err := b.ff.CreateAttachment(ctx, tx.JournalID, f.Name, f.Name, blobs[i]); err != nil {
			b.logErr("upload attachment", err)
			failed++
			continue
		}
		ok++
	}
	text := i18n.T("saved.attached", ok)
	if failed > 0 {
		text += "\n⚠️ " + i18n.T("saved.attach_failed", failed)
	}
	b.reply(ctx, chat, text)
}

// uploadFiles sends the draft's files to Firefly as attachments of the saved journal.
func (b *Bot) uploadFiles(ctx context.Context, d *Draft, journalID string) (ok, failed int) {
	for _, f := range d.Files {
		data, err := os.ReadFile(filepath.Join(b.filesDir, f.Path))
		if err != nil {
			b.logErr("read draft file", err)
			failed++
			continue
		}
		if err := b.ff.CreateAttachment(ctx, journalID, f.Name, f.Name, data); err != nil {
			b.logErr("upload attachment", err)
			failed++
			continue
		}
		ok++
	}
	return ok, failed
}
