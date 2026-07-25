//go:build !windows && !linux

package logging

import (
	"log/slog"
	"os"
)

type otherHandler struct{ slog.Handler }

func (otherHandler) Close() error { return nil }

// NewPlatformHandler is only meaningfully implemented for windows and
// linux — the two shipping targets (§1). This stderr fallback exists
// purely so the module type-checks and runs natively on a developer's own
// machine (e.g. macOS) without needing a GOOS override for every command.
func NewPlatformHandler(_ string) (PlatformHandler, error) {
	return otherHandler{slog.NewTextHandler(os.Stderr, nil)}, nil
}
