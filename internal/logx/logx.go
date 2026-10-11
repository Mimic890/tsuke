// Package logx builds the process logger and keeps secrets out of it.
package logx

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON-less text logger that masks every secret in messages and attributes.
func New(w io.Writer, level slog.Level, secrets ...string) *slog.Logger {
	var clean []string
	for _, s := range secrets {
		if len(s) >= 6 {
			clean = append(clean, s)
		}
	}
	redact := func(s string) string {
		for _, sec := range clean {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
		return s
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Value.Kind() {
			case slog.KindString:
				a.Value = slog.StringValue(redact(a.Value.String()))
			case slog.KindAny:
				if err, ok := a.Value.Any().(error); ok {
					a.Value = slog.StringValue(redact(err.Error()))
				}
			default:
			}
			return a
		},
	}))
}
