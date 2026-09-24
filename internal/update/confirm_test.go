package update

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportPreviousAttemptLogsSuccessWhenVersionChanged(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "update-marker.json")
	if err := RecordAttempt(markerPath, "http://example.invalid/installer.msi", "1.0.0"); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ReportPreviousAttempt(markerPath, "1.1.0", log)

	if !strings.Contains(buf.String(), "self-update succeeded") {
		t.Errorf("expected a success log line, got: %s", buf.String())
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("expected marker to be removed after reporting, stat err: %v", err)
	}
}

func TestReportPreviousAttemptWarnsWhenVersionUnchanged(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "update-marker.json")
	if err := RecordAttempt(markerPath, "http://example.invalid/installer.msi", "1.0.0"); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ReportPreviousAttempt(markerPath, "1.0.0", log)

	if !strings.Contains(buf.String(), "does not appear to have taken effect") {
		t.Errorf("expected a failure-to-take-effect log line, got: %s", buf.String())
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("expected marker to be removed after reporting, stat err: %v", err)
	}
}

func TestReportPreviousAttemptNoopWithoutMarker(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "update-marker.json")

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ReportPreviousAttempt(markerPath, "1.0.0", log)

	if buf.Len() != 0 {
		t.Errorf("expected no log output when no marker exists, got: %s", buf.String())
	}
}

func TestReportPreviousAttemptNoopWithEmptyPath(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ReportPreviousAttempt("", "1.0.0", log)

	if buf.Len() != 0 {
		t.Errorf("expected no log output with an empty marker path, got: %s", buf.String())
	}
}
