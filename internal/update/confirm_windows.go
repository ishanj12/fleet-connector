//go:build windows

package update

import "os"

// DefaultMarkerPath is where RecordAttempt/ReportPreviousAttempt look by
// default — the same fixed ProgramData location as cmd/fleetconnect's own
// config/log paths (see service_windows.go), overridable for the same
// reason those are: tests and non-standard installs.
func DefaultMarkerPath() string {
	if p := os.Getenv("FLEETCONNECT_UPDATE_MARKER"); p != "" {
		return p
	}
	return `C:\ProgramData\FleetConnector\update-marker.json`
}
