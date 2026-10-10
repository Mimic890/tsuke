package config

import (
	"log/slog"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func base() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"FIREFLY_URL":        "https://ff.example.com/",
		"FIREFLY_TOKEN":      "tok",
		"ALLOWED_USER_IDS":   "1, 2;3",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := LoadFrom(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if c.FireflyURL != "https://ff.example.com" || c.FireflyPublic != c.FireflyURL {
		t.Errorf("urls = %q %q", c.FireflyURL, c.FireflyPublic)
	}
	if len(c.AllowedUserIDs) != 3 {
		t.Errorf("ids = %v", c.AllowedUserIDs)
	}
	if c.Location.String() != "Europe/Moscow" || c.DataDir != "/data" || c.LogLevel != slog.LevelInfo {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.CacheRefresh != 6*time.Hour || c.WebhookURL != "" {
		t.Errorf("refresh/webhook = %v %q", c.CacheRefresh, c.WebhookURL)
	}
}

func TestLoadOverrides(t *testing.T) {
	m := base()
	m["TZ"] = "Asia/Yekaterinburg"
	m["LOG_LEVEL"] = "debug"
	m["FIREFLY_PUBLIC_URL"] = "https://public.example.com/"
	m["CACHE_REFRESH_HOURS"] = "0.5"
	m["WEBHOOK_URL"] = "https://bot.example.com/"
	c, err := LoadFrom(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.Location.String() != "Asia/Yekaterinburg" || c.LogLevel != slog.LevelDebug ||
		c.FireflyPublic != "https://public.example.com" || c.CacheRefresh != 30*time.Minute ||
		c.WebhookURL != "https://bot.example.com" {
		t.Errorf("overrides wrong: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]func(map[string]string){
		"missing token": func(m map[string]string) { delete(m, "TELEGRAM_BOT_TOKEN") },
		"missing url":   func(m map[string]string) { delete(m, "FIREFLY_URL") },
		"no users":      func(m map[string]string) { m["ALLOWED_USER_IDS"] = "" },
		"bad user":      func(m map[string]string) { m["ALLOWED_USER_IDS"] = "1,abc" },
		"bad tz":        func(m map[string]string) { m["TZ"] = "Mars/Base" },
		"bad level":     func(m map[string]string) { m["LOG_LEVEL"] = "loud" },
		"bad refresh":   func(m map[string]string) { m["CACHE_REFRESH_HOURS"] = "-1" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := base()
			mutate(m)
			if _, err := LoadFrom(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
