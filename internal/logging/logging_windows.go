package logging

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sys/windows/svc/eventlog"
)

// eventLogHandler adapts a Windows Event Log source to slog.Handler.
// eid is fixed at 1 for every record — this binary has no compiled
// message-table DLL, so it registers its source via
// eventlog.InstallAsEventCreate (see cmd/fleetconnect/service_windows.go)
// and relies on EventCreate.exe's generic message file, the same
// no-message-DLL pattern most small Go services use.
type eventLogHandler struct {
	log   *eventlog.Log
	attrs []slog.Attr
}

// NewPlatformHandler opens (registering if necessary) the named Event Log
// source. Call Close when the process exits.
func NewPlatformHandler(source string) (PlatformHandler, error) {
	l, err := eventlog.Open(source)
	if err != nil {
		return nil, fmt.Errorf("open event log source %q: %w", source, err)
	}
	return &eventLogHandler{log: l}, nil
}

func (h *eventLogHandler) Close() error {
	return h.log.Close()
}

func (h *eventLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *eventLogHandler) Handle(_ context.Context, r slog.Record) error {
	msg := r.Message
	for _, a := range h.attrs {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})

	const eid = 1
	switch {
	case r.Level >= slog.LevelError:
		return h.log.Error(eid, msg)
	case r.Level >= slog.LevelWarn:
		return h.log.Warning(eid, msg)
	default:
		return h.log.Info(eid, msg)
	}
}

func (h *eventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &eventLogHandler{log: h.log, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h *eventLogHandler) WithGroup(_ string) slog.Handler {
	return h // groups aren't meaningful for a flat Event Log message string
}
