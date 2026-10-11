// Package firefly is a small client for the Firefly III v1 REST API.
package firefly

import (
	"time"
)

// Account is an asset, expense or revenue account.
type Account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"` // asset | expense | revenue
	Balance  string `json:"balance,omitempty"`
	Currency string `json:"currency,omitempty"`
	Active   bool   `json:"active"`
}

// Named is a category, budget or tag.
type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Tx is a single-split transaction, used both to create and to read.
type Tx struct {
	GroupID     string    `json:"group_id,omitempty"`
	JournalID   string    `json:"journal_id,omitempty"`
	Type        string    `json:"type"`
	Date        time.Time `json:"date"`
	Amount      string    `json:"amount"` // decimal, e.g. "350.00"
	Currency    string    `json:"currency,omitempty"`
	Description string    `json:"description"`
	SourceID    string    `json:"source_id,omitempty"`
	SourceName  string    `json:"source_name,omitempty"`
	DestID      string    `json:"dest_id,omitempty"`
	DestName    string    `json:"dest_name,omitempty"`
	Category    string    `json:"category,omitempty"`
	BudgetID    string    `json:"budget_id,omitempty"`
	Budget      string    `json:"budget,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	ExternalID  string    `json:"external_id,omitempty"`
}
