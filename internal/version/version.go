// Package version holds the build identifier internal/update's self-update
// confirmation compares across restarts (see RecordAttempt/
// ReportPreviousAttempt): "did the version running after the installer
// launched actually differ from the version that launched it."
//
// Empty by default. An adopting customer wires this at build time, e.g.:
//
//	go build -ldflags "-X fleet-connector/internal/version.Version=1.2.3"
//
// Left unset, every build reports the same "" version, so that comparison
// can never observe a change — internal/update treats an empty Version as
// "confirmation not configured" and skips it entirely rather than reporting
// a false negative on every successful update.
package version

var Version string
