package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDrafts(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	base := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return base }

	if _, err := s.Draft(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := s.PutDraft(ctx, Draft{ID: "a", UserID: 1, Data: []byte(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AwaitingDraft(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no awaiting expected, got %v", err)
	}
	s.now = func() time.Time { return base.Add(time.Minute) }
	if err := s.PutDraft(ctx, Draft{ID: "b", UserID: 1, Awaiting: "amount", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// Update replaces data and makes it the newest.
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := s.PutDraft(ctx, Draft{ID: "a", UserID: 1, Awaiting: "desc", Data: []byte(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}
	d, err := s.AwaitingDraft(ctx, 1)
	if err != nil || d.ID != "a" || d.Awaiting != "desc" || string(d.Data) != `{"n":2}` {
		t.Fatalf("awaiting = %+v, %v", d, err)
	}
	if _, err := s.AwaitingDraft(ctx, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user: %v", err)
	}

	s.now = func() time.Time { return base.Add(48 * time.Hour) }
	if err := s.PutDraft(ctx, Draft{ID: "c", UserID: 1, Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.PurgeDrafts(ctx, base.Add(24*time.Hour))
	if err != nil || len(ids) != 2 {
		t.Fatalf("purged %v, %v", ids, err)
	}
	if err := s.DeleteDraft(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Draft(ctx, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted draft still there: %v", err)
	}
}

func TestUsageAndCache(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, n := range []string{"Еда", "Еда", "Такси", ""} {
		if err := s.Bump(ctx, "category", n); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.Usage(ctx, "category")
	if err != nil || len(u) != 2 || u["Еда"] != 2 || u["Такси"] != 1 {
		t.Fatalf("usage = %v, %v", u, err)
	}
	if u, _ := s.Usage(ctx, "tag"); len(u) != 0 {
		t.Fatalf("tag usage = %v", u)
	}

	if _, _, err := s.Cache(ctx, "accounts"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.PutCache(ctx, "accounts", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCache(ctx, "accounts", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	data, at, err := s.Cache(ctx, "accounts")
	if err != nil || string(data) != "v2" || time.Since(at) > time.Minute {
		t.Fatalf("cache = %q %v %v", data, at, err)
	}
}

func TestCreated(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	t0 := time.Unix(2_000_000, 0)
	for i, g := range []string{"1", "2"} {
		if err := s.PutCreated(ctx, Created{GroupID: g, UserID: 7, ChatID: 7, MessageID: 100 + i, Summary: "s" + g, At: t0.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.LastCreated(ctx, 7)
	if err != nil || last.GroupID != "2" || last.Summary != "s2" {
		t.Fatalf("last = %+v, %v", last, err)
	}
	g, err := s.GroupByCard(ctx, 7, 100)
	if err != nil || g != "1" {
		t.Fatalf("by card = %q, %v", g, err)
	}
	// A group can be shown on several messages; the original card keeps working.
	if err := s.PutCreated(ctx, Created{GroupID: "1", UserID: 7, ChatID: 7, MessageID: 555, Summary: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCard(ctx, 7, 600, "1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []int{100, 555, 600} {
		if g, err := s.GroupByCard(ctx, 7, m); err != nil || g != "1" {
			t.Fatalf("card %d = %q, %v", m, g, err)
		}
	}
	got, _ := s.LastCreated(ctx, 7)
	if got.GroupID != "2" {
		t.Fatalf("update must keep the original timestamp, last = %+v", got)
	}
	if err := s.DeleteCreated(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	last, _ = s.LastCreated(ctx, 7)
	if last.GroupID != "1" {
		t.Fatalf("last after delete = %+v", last)
	}
	if err := s.DeleteCreated(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GroupByCard(ctx, 7, 555); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cards must go with the transaction: %v", err)
	}
	if _, err := s.LastCreated(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
