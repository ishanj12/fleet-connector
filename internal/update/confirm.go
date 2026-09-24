package update

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// attemptRecord is what RecordAttempt persists and ReportPreviousAttempt
// reads back — just enough to answer "did the version actually change" and
// let an operator correlate a log line with the update attempt that caused
// it, not a general-purpose audit log.
type attemptRecord struct {
	PreviousVersion string    `json:"previous_version"`
	SourceURL       string    `json:"source_url"`
	AttemptedAt     time.Time `json:"attempted_at"`
}

// RecordAttempt persists a marker at markerPath noting that an installer
// launch just succeeded while this process was running currentVersion —
// see ReportPreviousAttempt, which the next process startup uses to tell
// whether that launch actually replaced the running binary. Apply calls
// this best-effort, after the installer is already launched: a failure to
// write the marker is worth logging but is never itself an update failure.
func RecordAttempt(markerPath, sourceURL, currentVersion string) error {
	rec := attemptRecord{
		PreviousVersion: currentVersion,
		SourceURL:       sourceURL,
		AttemptedAt:     time.Now(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal update marker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		return fmt.Errorf("create update marker directory: %w", err)
	}
	if err := os.WriteFile(markerPath, data, 0o600); err != nil {
		return fmt.Errorf("write update marker %q: %w", markerPath, err)
	}
	return nil
}

// ReportPreviousAttempt checks markerPath for a marker a previous process
// left via RecordAttempt, logs whether the update it recorded actually
// took effect (currentVersion differs from what was running immediately
// before that update was launched), and removes the marker either way.
// This is a one-shot check — call it once, early at startup (see
// cmd/fleetconnect) — so an ordinary restart unrelated to any update is
// never misread as one. A missing or unreadable marker is the common case
// (no update pending) and is silently treated as nothing to report.
func ReportPreviousAttempt(markerPath, currentVersion string, log *slog.Logger) {
	if markerPath == "" {
		return
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return
	}
	defer os.Remove(markerPath)

	var rec attemptRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		log.Warn("found an update marker but couldn't parse it, discarding", "error", err)
		return
	}

	if currentVersion != "" && currentVersion != rec.PreviousVersion {
		log.Info("self-update succeeded", "from", rec.PreviousVersion, "to", currentVersion, "source", rec.SourceURL, "attempted_at", rec.AttemptedAt)
		return
	}
	log.Warn("self-update does not appear to have taken effect — still running the version that was running before the update was launched",
		"version", rec.PreviousVersion, "source", rec.SourceURL, "attempted_at", rec.AttemptedAt)
}
