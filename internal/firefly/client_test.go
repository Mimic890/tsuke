package firefly

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "secret-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.SetBackoff(time.Millisecond)
	return c
}

var msk = time.FixedZone("MSK", 3*3600)

func sampleTx() Tx {
	return Tx{
		Type: "withdrawal", Date: time.Date(2026, 10, 10, 8, 53, 0, 0, msk), Amount: "350.00",
		Description: "кофе", SourceID: "1", DestName: "Кофейня", Category: "Еда",
		Tags: []string{"работа"}, Notes: "fn=1", ExternalID: "ext-1",
	}
}

const createdJSON = `{"data":{"type":"transactions","id":"77","attributes":{"transactions":[{
 "transaction_journal_id":"88","type":"withdrawal","date":"2026-10-10T08:53:00+03:00","amount":"350.000000000000",
 "currency_symbol":"₽","description":"кофе","source_id":"1","source_name":"Т-Банк","destination_id":"9",
 "destination_name":"Кофейня","category_name":"Еда","budget_id":null,"tags":["работа"],"notes":"fn=1","external_id":"ext-1"}]}}}`

func TestCreateTransactionPayload(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/transactions" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(createdJSON))
	}))
	tx, err := c.CreateTransaction(context.Background(), sampleTx())
	if err != nil {
		t.Fatal(err)
	}
	if tx.GroupID != "77" || tx.JournalID != "88" || tx.DestName != "Кофейня" || tx.Category != "Еда" || tx.Currency != "₽" {
		t.Errorf("unexpected tx %+v", tx)
	}
	if !tx.Date.Equal(time.Date(2026, 10, 10, 8, 53, 0, 0, msk)) {
		t.Errorf("date = %v", tx.Date)
	}
	for _, k := range []string{"apply_rules", "fire_webhooks", "error_if_duplicate_hash"} {
		if got[k] != true {
			t.Errorf("%s = %v, want true", k, got[k])
		}
	}
	sp := got["transactions"].([]any)[0].(map[string]any)
	checks := map[string]any{
		"type": "withdrawal", "date": "2026-10-10T08:53:00+03:00", "amount": "350.00",
		"source_id": "1", "destination_name": "Кофейня", "category_name": "Еда", "external_id": "ext-1", "notes": "fn=1",
	}
	for k, v := range checks {
		if sp[k] != v {
			t.Errorf("%s = %v, want %v", k, sp[k], v)
		}
	}
	if _, ok := sp["destination_id"]; ok {
		t.Error("destination_id must be omitted when only a name is known")
	}
}

func TestCreateTransactionRequiresExternalID(t *testing.T) {
	c := newTestClient(t, http.NotFoundHandler())
	tx := sampleTx()
	tx.ExternalID = ""
	if _, err := c.CreateTransaction(context.Background(), tx); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateTransactionRetryNeverDuplicates(t *testing.T) {
	var posts atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/transactions":
			posts.Add(1)
			// Committed on the server, but the client sees a gateway error.
			w.WriteHeader(http.StatusBadGateway)
		case r.URL.Path == "/api/v1/search/transactions":
			if !strings.Contains(r.URL.Query().Get("query"), "ext-1") {
				t.Errorf("query = %q", r.URL.Query().Get("query"))
			}
			_, _ = w.Write([]byte(`{"data":[` + strings.TrimSuffix(strings.TrimPrefix(createdJSON, `{"data":`), `}`) + `]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	tx, err := c.CreateTransaction(context.Background(), sampleTx())
	if err != nil {
		t.Fatal(err)
	}
	if tx.GroupID != "77" {
		t.Errorf("group = %s", tx.GroupID)
	}
	if n := posts.Load(); n != 1 {
		t.Errorf("POST count = %d, want exactly 1", n)
	}
}

func TestCreateTransactionRetriesWhenNotCommitted(t *testing.T) {
	var posts atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/transactions":
			if posts.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(createdJSON))
		case "/api/v1/search/transactions":
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	if _, err := c.CreateTransaction(context.Background(), sampleTx()); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 {
		t.Errorf("POST count = %d, want 2", posts.Load())
	}
}

func TestValidationError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"The given data was invalid.","errors":{"transactions.0.source_id":["Source account is invalid."],"transactions.0.amount":["The amount field must be greater than 0."]}}`))
	}))
	_, err := c.CreateTransaction(context.Background(), sampleTx())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err = %T %v", err, err)
	}
	want := "The amount field must be greater than 0.\nSource account is invalid."
	if ae.Human() != want {
		t.Errorf("Human = %q", ae.Human())
	}
	if ae.IsDuplicate() {
		t.Error("not a duplicate")
	}
}

func TestDuplicateError(t *testing.T) {
	ae := parseAPIError(422, []byte(`{"message":"Duplicate of transaction #12.","errors":{"transactions.0.description":["Duplicate of transaction #12."]}}`))
	if !ae.IsDuplicate() {
		t.Error("expected duplicate")
	}
}

func TestAccountsPagination(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "asset" {
			t.Errorf("type = %q", r.URL.Query().Get("type"))
		}
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"data":[{"id":"1","attributes":{"name":"Т-Банк","active":true,"current_balance":"1500.50","currency_symbol":"₽"}}],"meta":{"pagination":{"total_pages":2}}}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":[{"id":"2","attributes":{"name":"Наличные","active":false}}],"meta":{"pagination":{"total_pages":2}}}`))
		default:
			t.Errorf("page = %q", r.URL.Query().Get("page"))
		}
	}))
	accs, err := c.Accounts(context.Background(), "asset")
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 2 || accs[0].Name != "Т-Банк" || accs[0].Balance != "1500.50" || accs[1].Active {
		t.Errorf("accounts = %+v", accs)
	}
}

func TestTags(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"5","attributes":{"tag":"работа"}}],"meta":{"pagination":{"total_pages":1}}}`))
	}))
	tags, err := c.Tags(context.Background())
	if err != nil || len(tags) != 1 || tags[0].Name != "работа" {
		t.Errorf("tags = %+v, %v", tags, err)
	}
}

func TestGetRetriesTransient(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"version":"6.1"}`))
	}))
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Errorf("calls = %d", n.Load())
	}
}

func TestDeleteTransaction(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/transactions/77" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	}))
	if err := c.DeleteTransaction(context.Background(), "77"); err != nil {
		t.Fatal(err)
	}
	c2 := newTestClient(t, http.NotFoundHandler())
	if err := c2.DeleteTransaction(context.Background(), "1"); err != nil {
		t.Errorf("404 should count as deleted: %v", err)
	}
}

func TestUpdateTransactionClearsFields(t *testing.T) {
	var sp map[string]any
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/transactions/77" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["error_if_duplicate_hash"]; ok {
			t.Error("update must not send error_if_duplicate_hash")
		}
		sp = body["transactions"].([]any)[0].(map[string]any)
		_, _ = w.Write([]byte(createdJSON))
	}))
	tx := sampleTx()
	tx.Category, tx.JournalID = "", "88"
	if _, err := c.UpdateTransaction(context.Background(), "77", tx); err != nil {
		t.Fatal(err)
	}
	if v, ok := sp["category_name"]; !ok || v != nil {
		t.Errorf("category_name = %v (present=%v), want explicit null", v, ok)
	}
	if sp["transaction_journal_id"] != "88" {
		t.Errorf("journal id = %v", sp["transaction_journal_id"])
	}
}

func TestListTransactionsLimit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "2" || q.Get("start") != "2026-10-01" || q.Get("type") != "withdrawal" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.TrimSuffix(strings.TrimPrefix(createdJSON, `{"data":`), `}`) + `]}`))
	}))
	txs, err := c.ListTransactions(context.Background(), ListQuery{Start: time.Date(2026, 10, 1, 0, 0, 0, 0, msk), Type: "withdrawal", Limit: 2})
	if err != nil || len(txs) != 1 || txs[0].GroupID != "77" {
		t.Errorf("txs = %+v, %v", txs, err)
	}
}

func TestCreateAttachment(t *testing.T) {
	var meta map[string]string
	var uploaded []byte
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/attachments":
			_ = json.NewDecoder(r.Body).Decode(&meta)
			_, _ = w.Write([]byte(`{"data":{"id":"5"}}`))
		case "/api/v1/attachments/5/upload":
			uploaded, _ = io.ReadAll(r.Body)
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	if err := c.CreateAttachment(context.Background(), "88", "check.jpg", "check.jpg", []byte("BIN")); err != nil {
		t.Fatal(err)
	}
	if meta["attachable_type"] != "TransactionJournal" || meta["attachable_id"] != "88" || meta["filename"] != "check.jpg" {
		t.Errorf("meta = %v", meta)
	}
	if string(uploaded) != "BIN" {
		t.Errorf("uploaded = %q", uploaded)
	}
}
