package logx

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug, "123456:SECRET-TOKEN", "fftoken-abcdef", "x")
	log.Info("calling https://api.telegram.org/bot123456:SECRET-TOKEN/getMe",
		"err", errors.New("Get \"https://x/file/bot123456:SECRET-TOKEN/a.jpg\": EOF"),
		"hdr", "Bearer fftoken-abcdef", "short", "x")
	out := buf.String()
	for _, secret := range []string{"SECRET-TOKEN", "fftoken-abcdef"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked: %s", secret, out)
		}
	}
	if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "short=x") {
		t.Errorf("unexpected output: %s", out)
	}
}
