// Package logging configures the process logger.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New builds a slog logger.
//
// JSON in production because logs are consumed by machines there; text in
// development because they are consumed by a person reading a terminal.
// Choosing one format for both means either developers squint at JSON or
// production logs are unparseable — neither is necessary.
func New(level, format string) *slog.Logger {
	options := &slog.HandlerOptions{
		Level: parseLevel(level),
		// Source location on warnings and above only: it is genuinely useful
		// when something is wrong and pure noise on every request line.
		AddSource: parseLevel(level) <= slog.LevelDebug,
		ReplaceAttr: redactSensitive,
	}

	var handler slog.Handler
	if strings.EqualFold(format, "json") {
		handler = slog.NewJSONHandler(os.Stdout, options)
	} else {
		handler = slog.NewTextHandler(os.Stdout, options)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
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

// sensitiveKeys are redacted wherever they appear in a log record.
//
// This is a safety net, not the primary control — nothing in this codebase
// deliberately logs a password. But logs get copied into tickets, pasted into
// chat and shipped to third-party aggregators, and a single careless
// slog.Any("request", body) is all it takes. Redacting by key name costs one
// map lookup per attribute and removes the whole category.
var sensitiveKeys = map[string]bool{
	"password": true, "password_hash": true, "token": true, "access_token": true,
	"refresh_token": true, "authorization": true, "secret": true, "jwt_secret": true,
	"api_key": true, "cookie": true, "set-cookie": true, "ticket": true,
}

func redactSensitive(groups []string, attr slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(attr.Key)] {
		return slog.String(attr.Key, "[REDACTED]")
	}
	return attr
}
