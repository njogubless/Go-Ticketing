package realtime

import (
	"io"
	"log/slog"
)

// discardLogger keeps test output readable — the hub logs on every
// disconnect, and those lines are noise when the assertion is about routing.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
