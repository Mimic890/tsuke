package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/i18n"
	"github.com/Mimic890/tsuke/internal/parse"
	"github.com/Mimic890/tsuke/internal/receipt"
	"github.com/Mimic890/tsuke/internal/store"
)

// Acc is an account reference. An empty ID means a free-text name
// (payee or revenue source); Firefly creates such accounts on demand.
type Acc struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func (a Acc) isAsset() bool { return a.ID != "" }

// File is a receipt file kept on disk until the draft is saved.
type File struct {
	Path string `json:"path"` // relative to the files directory
	Name string `json:"name"`
}

// Picker is the state of the list picker screen.
type Picker struct {
	Field string `json:"field"` // from | to | category | budget | tags
	Kind  string `json:"kind"`
	Query string `json:"query,omitempty"`
	Page  int    `json:"page"`
	Items []Item `json:"items"` // filtered snapshot; callbacks index into it
}

// Screens of the card message.
const (
	scCard   = "card"
	scType   = "type"
	scPrompt = "prompt"
	scPick   = "pick"
	scDate   = "date"
	scTime   = "time"
)

// Draft is a transaction being entered. It is persisted as JSON.
type Draft struct {
	ID          string          `json:"id"`
	UserID      int64           `json:"user_id"`
	ChatID      int64           `json:"chat_id"`
	MsgID       int             `json:"msg_id"`
	Type        string          `json:"type"`
	Cents       int64           `json:"cents"`
	Desc        string          `json:"desc,omitempty"`
	Src         Acc             `json:"src"`
	Dst         Acc             `json:"dst"`
	Category    string          `json:"category,omitempty"`
	BudgetID    string          `json:"budget_id,omitempty"`
	Budget      string          `json:"budget,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Notes       string          `json:"notes,omitempty"`
	When        time.Time       `json:"when"`
	Files       []File          `json:"files,omitempty"`
	Fiscal      *receipt.Fiscal `json:"fiscal,omitempty"`
	EditGroup   string          `json:"edit_group,omitempty"` // set while editing a saved transaction
	EditJournal string          `json:"edit_journal,omitempty"`
	Guided      bool            `json:"guided,omitempty"`
	Step        int             `json:"step,omitempty"`
	Screen      string          `json:"screen"`
	Awaiting    string          `json:"awaiting,omitempty"` // field expecting typed text
	Hint        string          `json:"hint,omitempty"`     // i18n key shown on the prompt
	Pick        *Picker         `json:"pick,omitempty"`
	CalMonth    string          `json:"cal_month,omitempty"` // 2006-01
	Err         string          `json:"err,omitempty"`
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (b *Bot) newDraft(userID, chatID int64) *Draft {
	return &Draft{
		ID: newID(), UserID: userID, ChatID: chatID, Type: "withdrawal",
		When: b.now().In(b.loc), Screen: scCard,
	}
}

func (b *Bot) saveDraft(ctx context.Context, d *Draft) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return b.st.PutDraft(ctx, store.Draft{ID: d.ID, UserID: d.UserID, Awaiting: d.Awaiting, Data: data})
}

var errDraftGone = errors.New("draft not found")

func (b *Bot) loadDraft(ctx context.Context, id string) (*Draft, error) {
	sd, err := b.st.Draft(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errDraftGone
	}
	if err != nil {
		return nil, err
	}
	return decodeDraft(sd)
}

func decodeDraft(sd store.Draft) (*Draft, error) {
	d := &Draft{}
	if err := json.Unmarshal(sd.Data, d); err != nil {
		return nil, fmt.Errorf("decode draft: %w", err)
	}
	d.Awaiting = sd.Awaiting
	return d, nil
}

func (b *Bot) deleteDraft(ctx context.Context, d *Draft) {
	if err := b.st.DeleteDraft(ctx, d.ID); err != nil {
		b.logErr("delete draft", err)
	}
	b.removeFiles(d.ID)
}

func (b *Bot) removeFiles(draftID string) {
	if err := os.RemoveAll(filepath.Join(b.filesDir, draftID)); err != nil {
		b.logErr("remove draft files", err)
	}
}

// setType switches the type, moving the asset account to the side where the new type needs it.
func (b *Bot) setType(ctx context.Context, d *Draft, t string) {
	srcA, dstA := d.Src.isAsset(), d.Dst.isAsset()
	src, dst := d.Src, d.Dst
	def := func() Acc { return b.defaultAsset(ctx) }
	switch t {
	case "withdrawal":
		switch {
		case srcA:
		case dstA:
			src = dst
		default:
			src = def()
		}
		if dstA {
			dst = Acc{}
		}
	case "deposit":
		switch {
		case dstA:
		case srcA:
			dst = src
		default:
			dst = def()
		}
		if srcA {
			src = Acc{}
		}
	case "transfer":
		switch {
		case srcA:
			if !dstA {
				dst = Acc{}
			}
		case dstA:
			src, dst = dst, Acc{}
		default:
			src, dst = def(), Acc{}
		}
	}
	d.Type, d.Src, d.Dst = t, src, dst
	if t != "withdrawal" {
		d.BudgetID, d.Budget = "", ""
	}
}

// problems lists what prevents saving.
func (d *Draft) problems() []string {
	var p []string
	if d.Cents <= 0 {
		p = append(p, i18n.T("err.no_amount"))
	}
	switch d.Type {
	case "withdrawal":
		if !d.Src.isAsset() {
			p = append(p, i18n.T("err.no_source"))
		}
	case "deposit":
		if !d.Dst.isAsset() {
			p = append(p, i18n.T("err.no_dest"))
		}
	case "transfer":
		switch {
		case !d.Src.isAsset():
			p = append(p, i18n.T("err.no_source"))
		case !d.Dst.isAsset():
			p = append(p, i18n.T("err.no_dest"))
		case d.Src.ID == d.Dst.ID:
			p = append(p, i18n.T("err.same_account"))
		}
	}
	return p
}

// description falls back to something meaningful because Firefly requires one.
func (d *Draft) description() string {
	switch {
	case strings.TrimSpace(d.Desc) != "":
		return strings.TrimSpace(d.Desc)
	case d.Type == "withdrawal" && d.Dst.Name != "":
		return d.Dst.Name
	case d.Category != "":
		return d.Category
	default:
		return i18n.T("type." + d.Type)
	}
}

func accFields(a Acc) (id, name string) { return a.ID, a.Name }

func (d *Draft) toTx(loc *time.Location) firefly.Tx {
	t := firefly.Tx{
		Type: d.Type, Date: d.When.In(loc), Amount: parse.FormatAPI(d.Cents), Description: d.description(),
		Category: d.Category, BudgetID: d.BudgetID, Budget: d.Budget, Tags: d.Tags, Notes: d.Notes,
		JournalID: d.EditJournal,
	}
	t.SourceID, t.SourceName = accFields(d.Src)
	t.DestID, t.DestName = accFields(d.Dst)
	if d.EditGroup == "" {
		t.ExternalID = "tsuke-" + d.ID
	}
	return t
}

// draftFromTx turns a saved transaction into an editable draft.
func (b *Bot) draftFromTx(userID, chatID int64, msgID int, tx *firefly.Tx) *Draft {
	d := b.newDraft(userID, chatID)
	d.MsgID = msgID
	d.Type = tx.Type
	d.Cents = parse.APIToCents(tx.Amount)
	if d.Cents < 0 {
		d.Cents = -d.Cents
	}
	d.Desc = tx.Description
	d.Category, d.Budget, d.BudgetID = tx.Category, tx.Budget, tx.BudgetID
	d.Tags, d.Notes = tx.Tags, tx.Notes
	d.When = tx.Date.In(b.loc)
	d.EditGroup, d.EditJournal = tx.GroupID, tx.JournalID
	// Keep IDs only for asset sides; payees and revenue sources are plain names.
	switch tx.Type {
	case "withdrawal":
		d.Src, d.Dst = Acc{ID: tx.SourceID, Name: tx.SourceName}, Acc{Name: tx.DestName}
	case "deposit":
		d.Src, d.Dst = Acc{Name: tx.SourceName}, Acc{ID: tx.DestID, Name: tx.DestName}
	default:
		d.Src, d.Dst = Acc{ID: tx.SourceID, Name: tx.SourceName}, Acc{ID: tx.DestID, Name: tx.DestName}
	}
	return d
}
