//go:build !windows

package update

// DefaultMarkerPath returns "" on non-Windows — self-update is
// Windows-only (see this package's doc comment), so there's never a marker
// to place or look for.
func DefaultMarkerPath() string { return "" }
