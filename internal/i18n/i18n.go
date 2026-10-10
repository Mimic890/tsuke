// Package i18n is the single place for user-facing text.
// Add another language by registering one more table in tables.
package i18n

import (
	"fmt"
	"sync"
)

var (
	mu      sync.RWMutex
	current = "ru"
	tables  = map[string]map[string]string{"ru": ru}
)

// Set selects the active language; unknown codes are ignored.
func Set(lang string) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := tables[lang]; ok {
		current = lang
	}
}

// T returns the text for key, formatted with args. A missing key is returned as is,
// so gaps are visible instead of silently empty.
func T(key string, args ...any) string {
	mu.RLock()
	s, ok := tables[current][key]
	mu.RUnlock()
	if !ok {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Has reports whether the active language defines key.
func Has(key string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := tables[current][key]
	return ok
}
