package firefly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to the Firefly III API.
type Client struct {
	base    string
	token   string
	http    *http.Client
	log     *slog.Logger
	retries int
	backoff time.Duration
}

// New creates a client. base is the Firefly III root URL without a trailing slash.
func New(base, token string, log *slog.Logger) *Client {
	return &Client{
		base:    strings.TrimRight(base, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		log:     log,
		retries: 3,
		backoff: 500 * time.Millisecond,
	}
}

// SetBackoff changes the first retry delay (tests use a tiny value).
func (c *Client) SetBackoff(d time.Duration) { c.backoff = d }

// do performs one request without retries.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.api+json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("firefly: %s %s: %w", method, path, stripURL(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("firefly: read %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return parseAPIError(resp.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("firefly: decode %s: %w", path, err)
		}
	}
	return nil
}

func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func parseAPIError(status int, raw []byte) *APIError {
	e := &APIError{Status: status}
	var body struct {
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
		Error   string              `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil {
		e.Message = body.Message
		if e.Message == "" {
			e.Message = body.Error
		}
		e.Fields = body.Errors
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// transient reports whether the failure may be retried.
func transient(err error) bool {
	if err == nil {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status >= 500
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "connection")
}

func (c *Client) sleep(ctx context.Context, attempt int) error {
	t := time.NewTimer(c.backoff << attempt)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get performs an idempotent request with retries.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	var err error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if serr := c.sleep(ctx, attempt-1); serr != nil {
				return serr
			}
		}
		if err = c.do(ctx, http.MethodGet, path, q, nil, "", out); !transient(err) {
			return err
		}
		c.log.Debug("retrying firefly request", "path", path, "attempt", attempt+1, "err", err)
	}
	return err
}

// Ping verifies connectivity and the token.
func (c *Client) Ping(ctx context.Context) error {
	return c.get(ctx, "/api/v1/about", nil, nil)
}

// page is the common list envelope.
type page struct {
	Meta struct {
		Pagination struct {
			TotalPages int `json:"total_pages"`
		} `json:"pagination"`
	} `json:"meta"`
}

// paginate walks a list endpoint, calling each for every page body.
func (c *Client) paginate(ctx context.Context, path string, q url.Values, each func(raw json.RawMessage) error) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("limit", "100")
	for p := 1; p <= 200; p++ {
		q.Set("page", strconv.Itoa(p))
		var raw json.RawMessage
		if err := c.get(ctx, path, q, &raw); err != nil {
			return err
		}
		if err := each(raw); err != nil {
			return err
		}
		var pg page
		_ = json.Unmarshal(raw, &pg)
		if p >= pg.Meta.Pagination.TotalPages {
			return nil
		}
	}
	return nil
}

// Accounts lists accounts of a type: "asset", "expense" or "revenue".
func (c *Client) Accounts(ctx context.Context, typ string) ([]Account, error) {
	var out []Account
	err := c.paginate(ctx, "/api/v1/accounts", url.Values{"type": {typ}}, func(raw json.RawMessage) error {
		var body struct {
			Data []struct {
				ID         string `json:"id"`
				Attributes struct {
					Name           string `json:"name"`
					Active         *bool  `json:"active"`
					CurrentBalance string `json:"current_balance"`
					CurrencyCode   string `json:"currency_code"`
					CurrencySymbol string `json:"currency_symbol"`
				} `json:"attributes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return err
		}
		for _, d := range body.Data {
			a := d.Attributes
			cur := a.CurrencySymbol
			if cur == "" {
				cur = a.CurrencyCode
			}
			out = append(out, Account{
				ID: d.ID, Name: a.Name, Type: typ, Balance: a.CurrentBalance,
				Currency: cur, Active: a.Active == nil || *a.Active,
			})
		}
		return nil
	})
	return out, err
}

// Categories lists all categories.
func (c *Client) Categories(ctx context.Context) ([]Named, error) {
	return c.named(ctx, "/api/v1/categories", "name")
}

// Budgets lists all budgets.
func (c *Client) Budgets(ctx context.Context) ([]Named, error) {
	return c.named(ctx, "/api/v1/budgets", "name")
}

// Tags lists all tags.
func (c *Client) Tags(ctx context.Context) ([]Named, error) {
	return c.named(ctx, "/api/v1/tags", "tag")
}

func (c *Client) named(ctx context.Context, path, field string) ([]Named, error) {
	var out []Named
	err := c.paginate(ctx, path, nil, func(raw json.RawMessage) error {
		var body struct {
			Data []struct {
				ID         string                     `json:"id"`
				Attributes map[string]json.RawMessage `json:"attributes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return err
		}
		for _, d := range body.Data {
			var name string
			_ = json.Unmarshal(d.Attributes[field], &name)
			if name != "" {
				out = append(out, Named{ID: d.ID, Name: name})
			}
		}
		return nil
	})
	return out, err
}
