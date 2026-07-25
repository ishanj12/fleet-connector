package logging

import (
	"log/slog"
	"os"
)

type linuxHandler struct{ slog.Handler }

func (linuxHandler) Close() error { return nil }

// NewPlatformHandler returns a stderr text handler — under systemd,
// stdout/stderr are captured into journald automatically, so there's no
// bespoke API call needed the way Windows' Event Log requires. source is
// unused here, kept only for signature parity with the Windows sink,
// which needs one to open its event log source.
func NewPlatformHandler(_ string) (PlatformHandler, error) {
	return linuxHandler{slog.NewTextHandler(os.Stderr, nil)}, nil
}
