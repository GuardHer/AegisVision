package logger

import (
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// Options controls structured logger initialization.
type Options struct {
	Service    string
	InstanceID string
	Level      string
	Format     string // "json" (default) or "text"
	Writer     io.Writer
}

// New creates a structured slog.Logger with fixed service and instance_id attributes.
func New(opts Options) *slog.Logger {
	w := opts.Writer
	if w == nil {
		w = os.Stdout
	}

	level := parseLevel(opts.Level)
	handlerOpts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		handler = slog.NewTextHandler(w, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(w, handlerOpts)
	}

	l := slog.New(handler)
	if opts.Service != "" {
		l = l.With(slog.String("service", opts.Service))
	}
	if opts.InstanceID != "" {
		l = l.With(slog.String("instance_id", opts.InstanceID))
	}
	return l
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// RedactURL masks userinfo passwords in URLs (e.g., PostgreSQL DSN, AMQP URL, RTSP URL)
// before writing them to logs.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPass := u.User.Password(); hasPass {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

// RedactSecret returns a fixed masked placeholder for non-empty secret strings.
func RedactSecret(secret string) string {
	if secret == "" {
		return ""
	}
	return "***"
}
