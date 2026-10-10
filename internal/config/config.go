// Package config loads the application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration of tsuke.
type Config struct {
	TelegramToken  string
	FireflyURL     string
	FireflyPublic  string // base URL used for links in messages
	FireflyToken   string
	AllowedUserIDs map[int64]struct{}
	Location       *time.Location
	DefaultSource  string
	DataDir        string
	LogLevel       slog.Level
	CacheRefresh   time.Duration
	WebhookURL     string // empty => long polling
	WebhookSecret  string
	WebhookListen  string
	WebhookPath    string
}

// Load reads and validates the configuration from environment variables.
func Load() (*Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom is Load with an injectable environment lookup.
func LoadFrom(get func(string) string) (*Config, error) {
	c := &Config{
		TelegramToken: strings.TrimSpace(get("TELEGRAM_BOT_TOKEN")),
		FireflyURL:    strings.TrimRight(strings.TrimSpace(get("FIREFLY_URL")), "/"),
		FireflyToken:  strings.TrimSpace(get("FIREFLY_TOKEN")),
		DefaultSource: strings.TrimSpace(get("DEFAULT_SOURCE_ACCOUNT")),
		DataDir:       orDefault(get("DATA_DIR"), "/data"),
		WebhookURL:    strings.TrimRight(strings.TrimSpace(get("WEBHOOK_URL")), "/"),
		WebhookSecret: strings.TrimSpace(get("WEBHOOK_SECRET")),
		WebhookListen: orDefault(get("WEBHOOK_LISTEN"), ":8080"),
		WebhookPath:   orDefault(get("WEBHOOK_PATH"), "/telegram/webhook"),
	}
	c.FireflyPublic = strings.TrimRight(strings.TrimSpace(get("FIREFLY_PUBLIC_URL")), "/")
	if c.FireflyPublic == "" {
		c.FireflyPublic = c.FireflyURL
	}

	var missing []string
	for k, v := range map[string]string{
		"TELEGRAM_BOT_TOKEN": c.TelegramToken,
		"FIREFLY_URL":        c.FireflyURL,
		"FIREFLY_TOKEN":      c.FireflyToken,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env: %s", strings.Join(sortStrings(missing), ", "))
	}

	ids, err := parseIDs(get("ALLOWED_USER_IDS"))
	if err != nil {
		return nil, err
	}
	c.AllowedUserIDs = ids

	loc, err := time.LoadLocation(orDefault(get("TZ"), "Europe/Moscow"))
	if err != nil {
		return nil, fmt.Errorf("invalid TZ: %w", err)
	}
	c.Location = loc

	if err := c.LogLevel.UnmarshalText([]byte(orDefault(get("LOG_LEVEL"), "info"))); err != nil {
		return nil, fmt.Errorf("invalid LOG_LEVEL: %w", err)
	}

	hours := 6.0
	if v := strings.TrimSpace(get("CACHE_REFRESH_HOURS")); v != "" {
		hours, err = strconv.ParseFloat(v, 64)
		if err != nil || hours <= 0 {
			return nil, errors.New("invalid CACHE_REFRESH_HOURS")
		}
	}
	c.CacheRefresh = time.Duration(hours * float64(time.Hour))
	return c, nil
}

func parseIDs(s string) (map[int64]struct{}, error) {
	out := map[int64]struct{}{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid ALLOWED_USER_IDS entry %q", p)
		}
		out[id] = struct{}{}
	}
	if len(out) == 0 {
		return nil, errors.New("ALLOWED_USER_IDS must contain at least one Telegram user id")
	}
	return out, nil
}

func orDefault(v, d string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return d
}

func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}
