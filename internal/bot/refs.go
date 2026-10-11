package bot

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/parse"
	"github.com/Mimic890/tsuke/internal/store"
)

// Entity kinds used for pickers, caching and usage counters.
const (
	kAsset    = "asset"
	kExpense  = "expense"
	kRevenue  = "revenue"
	kCategory = "category"
	kBudget   = "budget"
	kTag      = "tag"
)

// Item is a selectable entity.
type Item struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// refs is the cached snapshot of Firefly reference data.
type refs struct {
	mu         sync.RWMutex
	Assets     []firefly.Account `json:"assets"`
	Expenses   []firefly.Account `json:"expenses"`
	Revenues   []firefly.Account `json:"revenues"`
	Categories []firefly.Named   `json:"categories"`
	Budgets    []firefly.Named   `json:"budgets"`
	Tags       []firefly.Named   `json:"tags"`
	Updated    time.Time         `json:"updated"`
}

const refsCacheKey = "refs"

// loadRefsFromStore restores the last snapshot so the bot works while Firefly is down.
func (b *Bot) loadRefsFromStore(ctx context.Context) {
	data, _, err := b.st.Cache(ctx, refsCacheKey)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.logErr("load refs cache", err)
		}
		return
	}
	b.refs.mu.Lock()
	defer b.refs.mu.Unlock()
	if err := json.Unmarshal(data, b.refs); err != nil {
		b.logErr("decode refs cache", err)
	}
}

// RefreshRefs reloads accounts, categories, budgets and tags from Firefly.
// Parts that fail keep their previous values.
func (b *Bot) RefreshRefs(ctx context.Context) error {
	var errs []error
	fetch := func(name string, f func() error) {
		if err := f(); err != nil {
			errs = append(errs, err)
			b.logErr("refresh "+name, err)
		}
	}
	next := &refs{}
	b.refs.mu.RLock()
	next.Assets, next.Expenses, next.Revenues = b.refs.Assets, b.refs.Expenses, b.refs.Revenues
	next.Categories, next.Budgets, next.Tags = b.refs.Categories, b.refs.Budgets, b.refs.Tags
	b.refs.mu.RUnlock()

	fetch("assets", func() (err error) { next.Assets, err = b.ff.Accounts(ctx, kAsset); return })
	fetch("expenses", func() (err error) { next.Expenses, err = b.ff.Accounts(ctx, kExpense); return })
	fetch("revenues", func() (err error) { next.Revenues, err = b.ff.Accounts(ctx, kRevenue); return })
	fetch("categories", func() (err error) { next.Categories, err = b.ff.Categories(ctx); return })
	fetch("budgets", func() (err error) { next.Budgets, err = b.ff.Budgets(ctx); return })
	fetch("tags", func() (err error) { next.Tags, err = b.ff.Tags(ctx); return })
	next.Updated = b.now()

	b.refs.mu.Lock()
	b.refs.Assets, b.refs.Expenses, b.refs.Revenues = next.Assets, next.Expenses, next.Revenues
	b.refs.Categories, b.refs.Budgets, b.refs.Tags = next.Categories, next.Budgets, next.Tags
	b.refs.Updated = next.Updated
	data, merr := json.Marshal(b.refs)
	b.refs.mu.Unlock()
	if merr == nil {
		if err := b.st.PutCache(ctx, refsCacheKey, data); err != nil {
			b.logErr("store refs cache", err)
		}
	}
	return errors.Join(errs...)
}

// items returns the entities of a kind, most used first.
func (b *Bot) items(ctx context.Context, kind string) []Item {
	b.refs.mu.RLock()
	var out []Item
	switch kind {
	case kAsset:
		for _, a := range b.refs.Assets {
			if a.Active {
				out = append(out, Item{ID: a.ID, Name: a.Name})
			}
		}
	case kExpense:
		for _, a := range b.refs.Expenses {
			out = append(out, Item{ID: a.ID, Name: a.Name})
		}
	case kRevenue:
		for _, a := range b.refs.Revenues {
			out = append(out, Item{ID: a.ID, Name: a.Name})
		}
	case kCategory:
		for _, n := range b.refs.Categories {
			out = append(out, Item(n))
		}
	case kBudget:
		for _, n := range b.refs.Budgets {
			out = append(out, Item(n))
		}
	case kTag:
		for _, n := range b.refs.Tags {
			out = append(out, Item(n))
		}
	}
	b.refs.mu.RUnlock()

	usage, err := b.st.Usage(ctx, kind)
	if err != nil {
		b.logErr("read usage", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ui, uj := usage[out[i].Name], usage[out[j].Name]; ui != uj {
			return ui > uj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// search filters items by a typed query, best matches first.
func search(items []Item, q string) []Item {
	if strings.TrimSpace(q) == "" {
		return items
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	var out []Item
	for _, i := range parse.Rank(q, names) {
		out = append(out, items[i])
	}
	return out
}

// find returns the best fuzzy match for name among items.
func find(items []Item, name string) (Item, bool) {
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	if i := parse.Best(name, names); i >= 0 {
		return items[i], true
	}
	return Item{}, false
}

// defaultAsset resolves DEFAULT_SOURCE_ACCOUNT (name or id); without it, the most used asset account.
func (b *Bot) defaultAsset(ctx context.Context) Acc {
	items := b.items(ctx, kAsset)
	if len(items) == 0 {
		return Acc{}
	}
	if want := b.cfg.DefaultSource; want != "" {
		for _, it := range items {
			if it.ID == want {
				return Acc(it)
			}
		}
		if it, ok := find(items, want); ok {
			return Acc(it)
		}
	}
	return Acc{ID: items[0].ID, Name: items[0].Name}
}

// assetCurrency returns the currency symbol of an asset account, "₽" when unknown.
func (b *Bot) assetCurrency(id string) string {
	b.refs.mu.RLock()
	defer b.refs.mu.RUnlock()
	for _, a := range b.refs.Assets {
		if a.ID == id && a.Currency != "" {
			return a.Currency
		}
	}
	return "₽"
}
