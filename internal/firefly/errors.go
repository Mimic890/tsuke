package firefly

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// APIError is a non-2xx answer from Firefly III.
type APIError struct {
	Status  int
	Message string
	Fields  map[string][]string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("firefly: HTTP %d: %s", e.Status, e.Human())
}

// Human renders validation errors as readable lines without API paths.
func (e *APIError) Human() string {
	if len(e.Fields) == 0 {
		return e.Message
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, strings.Join(e.Fields[k], "; "))
	}
	return strings.Join(parts, "\n")
}

// IsDuplicate reports a duplicate-hash rejection.
func (e *APIError) IsDuplicate() bool {
	if e.Status != 422 {
		return false
	}
	all := strings.ToLower(e.Message + " " + e.Human())
	return strings.Contains(all, "duplicate")
}

// ErrNotFound is returned for HTTP 404.
var ErrNotFound = errors.New("firefly: not found")
