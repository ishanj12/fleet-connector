// Package logging builds the shared structured logger used across both
// platforms: a rotating file sink everywhere, fanned out to a
// platform-specific sink (Windows Event Log, or stderr for journald to
// capture on Linux) — see logging_windows.go/logging_linux.go.
package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// PlatformHandler is a slog.Handler that also needs closing before the
// process exits — the Windows Event Log sink holds an OS handle; the
// Linux stderr sink has nothing to close but implements a no-op Close so
// call sites have one uniform shape across platforms.
type PlatformHandler interface {
	slog.Handler
	Close() error
}

// ParseLevel maps config.Config.LogLevel's string values to slog.Level.
// Deliberately only the levels slog itself has (debug/info/warn/error) —
// this governs our own app logging and whatever the SDK emits via
// ngrok.WithLogger, not a port of the real ngrok agent's own log_level
// enum (which also has "crit", a level slog has no equivalent for). Empty
// defaults to Info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn, or error)", s)
	}
}

// New builds a *slog.Logger writing to a rotating file at path plus
// whichever platform handler the caller supplies (built by
// NewPlatformHandler on each platform). It also returns a *slog.LevelVar
// defaulting to Info — the logger is necessarily built before config.yaml
// is read (config.Config.LogLevel included), so the level starts at a
// sane default and callers adjust the returned LevelVar once the real
// value is known (see agent.New). slog.LevelVar's Enabled checks are
// dynamic, so adjusting it after the fact changes the effective level
// immediately, with no logger rebuild needed.
func New(path string, platform slog.Handler) (*slog.Logger, *slog.LevelVar) {
	level := new(slog.LevelVar) // zero value is LevelInfo
	fileHandler := slog.NewJSONHandler(&lumberjack.Logger{
		Filename:   path,
		MaxSize:    10, // megabytes
		MaxBackups: 5,
		MaxAge:     28, // days
	}, &slog.HandlerOptions{Level: level})
	return slog.New(&multiHandler{handlers: []slog.Handler{fileHandler, platform}, level: level}), level
}

// multiHandler fans a record out to every underlying handler, since slog
// has no built-in way to write to more than one sink at once. Level
// filtering is centralized here rather than duplicated per sub-handler —
// PlatformHandler implementations are free to just always return true
// from Enabled and defer the actual policy to this type.
type multiHandler struct {
	handlers []slog.Handler
	level    *slog.LevelVar
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level < m.level.Level() {
		return false
	}
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < m.level.Level() {
		return nil
	}
	var errs []error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers, level: m.level}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers, level: m.level}
}
