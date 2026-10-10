package firefly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// jsplit is a transaction split as sent to / received from the API.
type jsplit struct {
	JournalID       string   `json:"transaction_journal_id,omitempty"`
	Type            string   `json:"type"`
	Date            string   `json:"date"`
	Amount          string   `json:"amount"`
	Description     string   `json:"description"`
	CurrencySymbol  string   `json:"currency_symbol,omitempty"`
	CurrencyCode    string   `json:"currency_code,omitempty"`
	SourceID        string   `json:"source_id,omitempty"`
	SourceName      string   `json:"source_name,omitempty"`
	DestinationID   string   `json:"destination_id,omitempty"`
	DestinationName string   `json:"destination_name,omitempty"`
	CategoryName    *string  `json:"category_name"`
	BudgetID        *string  `json:"budget_id"`
	BudgetName      *string  `json:"budget_name,omitempty"`
	Tags            []string `json:"tags"`
	Notes           *string  `json:"notes"`
	ExternalID      *string  `json:"external_id,omitempty"`
}

type groupBody struct {
	ErrorIfDuplicateHash *bool    `json:"error_if_duplicate_hash,omitempty"`
	ApplyRules           bool     `json:"apply_rules"`
	FireWebhooks         bool     `json:"fire_webhooks"`
	GroupTitle           *string  `json:"group_title,omitempty"`
	Transactions         []jsplit `json:"transactions"`
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toSplit(t Tx) jsplit {
	s := jsplit{
		Type:         t.Type,
		Date:         t.Date.Format(time.RFC3339),
		Amount:       t.Amount,
		Description:  t.Description,
		CategoryName: nilIfEmpty(t.Category),
		BudgetID:     nilIfEmpty(t.BudgetID),
		Tags:         t.Tags,
		Notes:        nilIfEmpty(t.Notes),
		ExternalID:   nilIfEmpty(t.ExternalID),
		JournalID:    t.JournalID,
	}
	if s.Tags == nil {
		s.Tags = []string{}
	}
	if t.SourceID != "" {
		s.SourceID = t.SourceID
	} else {
		s.SourceName = t.SourceName
	}
	if t.DestID != "" {
		s.DestinationID = t.DestID
	} else {
		s.DestinationName = t.DestName
	}
	return s
}

func fromGroup(id string, sp jsplit) Tx {
	date, _ := time.Parse(time.RFC3339, sp.Date)
	cur := sp.CurrencySymbol
	if cur == "" {
		cur = sp.CurrencyCode
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	t := Tx{
		GroupID: id, JournalID: sp.JournalID, Type: sp.Type, Date: date, Amount: sp.Amount, Currency: cur,
		Description: sp.Description, SourceID: sp.SourceID, SourceName: sp.SourceName,
		DestID: sp.DestinationID, DestName: sp.DestinationName, Category: deref(sp.CategoryName),
		BudgetID: deref(sp.BudgetID), Budget: deref(sp.BudgetName), Tags: sp.Tags, Notes: deref(sp.Notes),
		ExternalID: deref(sp.ExternalID),
	}
	return t
}

// groupResp parses {"data": {"id": "...", "attributes": {"transactions": [...]}}}.
type groupResp struct {
	ID         string `json:"id"`
	Attributes struct {
		Transactions []jsplit `json:"transactions"`
	} `json:"attributes"`
}

func (g groupResp) tx() (*Tx, error) {
	if len(g.Attributes.Transactions) == 0 {
		return nil, errors.New("firefly: transaction without splits")
	}
	t := fromGroup(g.ID, g.Attributes.Transactions[0])
	return &t, nil
}

// CreateTransaction creates a transaction. It sets apply_rules, fire_webhooks and
// error_if_duplicate_hash. A retry after an ambiguous failure first looks the
// transaction up by its external_id, so it is never created twice.
func (c *Client) CreateTransaction(ctx context.Context, t Tx) (*Tx, error) {
	if t.ExternalID == "" {
		return nil, errors.New("firefly: external_id is required for idempotent creation")
	}
	yes := true
	body, err := json.Marshal(groupBody{
		ErrorIfDuplicateHash: &yes, ApplyRules: true, FireWebhooks: true,
		Transactions: []jsplit{toSplit(t)},
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, attempt-1); err != nil {
				return nil, err
			}
			// The previous attempt may have been committed before the failure.
			if found, ferr := c.FindByExternalID(ctx, t.ExternalID); ferr == nil && found != nil {
				return found, nil
			}
		}
		var out struct {
			Data groupResp `json:"data"`
		}
		err := c.do(ctx, http.MethodPost, "/api/v1/transactions", nil, body, "application/json", &out)
		if err == nil {
			return out.Data.tx()
		}
		lastErr = err
		var ae *APIError
		if attempt > 0 && errors.As(err, &ae) && ae.IsDuplicate() {
			// Our own earlier attempt created it but the lookup missed it.
			c.log.Warn("duplicate on retry; transaction was likely created by a previous attempt")
		}
		if !transient(err) {
			return nil, err
		}
		c.log.Debug("retrying transaction create", "attempt", attempt+1, "err", err)
	}
	return nil, lastErr
}

// FindByExternalID searches for a transaction by external id.
func (c *Client) FindByExternalID(ctx context.Context, ext string) (*Tx, error) {
	var out struct {
		Data []groupResp `json:"data"`
	}
	q := url.Values{"query": {`external_id_is:"` + ext + `"`}, "limit": {"5"}}
	if err := c.get(ctx, "/api/v1/search/transactions", q, &out); err != nil {
		return nil, err
	}
	for _, g := range out.Data {
		for _, sp := range g.Attributes.Transactions {
			if sp.ExternalID != nil && *sp.ExternalID == ext {
				t := fromGroup(g.ID, sp)
				return &t, nil
			}
		}
	}
	return nil, nil
}

// UpdateTransaction replaces the first split of an existing transaction group.
func (c *Client) UpdateTransaction(ctx context.Context, groupID string, t Tx) (*Tx, error) {
	body, err := json.Marshal(groupBody{
		ApplyRules: true, FireWebhooks: true,
		Transactions: []jsplit{toSplit(t)},
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Data groupResp `json:"data"`
	}
	if err := c.do(ctx, http.MethodPut, "/api/v1/transactions/"+url.PathEscape(groupID), nil, body, "application/json", &out); err != nil {
		return nil, err
	}
	return out.Data.tx()
}

// Transaction fetches one transaction group.
func (c *Client) Transaction(ctx context.Context, groupID string) (*Tx, error) {
	var out struct {
		Data groupResp `json:"data"`
	}
	if err := c.get(ctx, "/api/v1/transactions/"+url.PathEscape(groupID), nil, &out); err != nil {
		return nil, err
	}
	return out.Data.tx()
}

// DeleteTransaction deletes a transaction group. A missing one counts as deleted.
func (c *Client) DeleteTransaction(ctx context.Context, groupID string) error {
	var err error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if serr := c.sleep(ctx, attempt-1); serr != nil {
				return serr
			}
		}
		err = c.do(ctx, http.MethodDelete, "/api/v1/transactions/"+url.PathEscape(groupID), nil, nil, "", nil)
		if err == nil || errors.Is(err, ErrNotFound) {
			return nil
		}
		if !transient(err) {
			return err
		}
	}
	return err
}

// ListQuery filters ListTransactions.
type ListQuery struct {
	Start, End time.Time // dates only; zero means unbounded
	Type       string    // withdrawal | deposit | transfer | "" for all
	Limit      int       // >0 returns just the first page of that size
}

// ListTransactions lists transactions, newest first (Firefly's default order).
func (c *Client) ListTransactions(ctx context.Context, lq ListQuery) ([]Tx, error) {
	q := url.Values{}
	if !lq.Start.IsZero() {
		q.Set("start", lq.Start.Format("2006-01-02"))
	}
	if !lq.End.IsZero() {
		q.Set("end", lq.End.Format("2006-01-02"))
	}
	if lq.Type != "" {
		q.Set("type", lq.Type)
	}
	var out []Tx
	collect := func(raw json.RawMessage) error {
		var body struct {
			Data []groupResp `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return err
		}
		for _, g := range body.Data {
			for _, sp := range g.Attributes.Transactions {
				out = append(out, fromGroup(g.ID, sp))
			}
		}
		return nil
	}
	if lq.Limit > 0 {
		q.Set("limit", strconv.Itoa(lq.Limit))
		q.Set("page", "1")
		var raw json.RawMessage
		if err := c.get(ctx, "/api/v1/transactions", q, &raw); err != nil {
			return nil, err
		}
		if err := collect(raw); err != nil {
			return nil, err
		}
		if len(out) > lq.Limit {
			out = out[:lq.Limit]
		}
		return out, nil
	}
	err := c.paginate(ctx, "/api/v1/transactions", q, collect)
	return out, err
}

// CreateAttachment registers an attachment for a journal and uploads its content.
func (c *Client) CreateAttachment(ctx context.Context, journalID, filename, title string, data []byte) error {
	meta, err := json.Marshal(map[string]string{
		"filename":        filename,
		"attachable_type": "TransactionJournal",
		"attachable_id":   journalID,
		"title":           title,
	})
	if err != nil {
		return err
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/attachments", nil, meta, "application/json", &out); err != nil {
		return fmt.Errorf("create attachment: %w", err)
	}
	if out.Data.ID == "" {
		return errors.New("firefly: attachment id missing in response")
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/attachments/"+out.Data.ID+"/upload", nil, data, "application/octet-stream", nil); err != nil {
		return fmt.Errorf("upload attachment: %w", err)
	}
	return nil
}
