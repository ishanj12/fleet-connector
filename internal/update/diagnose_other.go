//go:build !windows

package update

// checkWindowsDefenderDetection has no non-Windows implementation — see
// diagnose_windows.go. Always returns "" ("no extra context available"),
// which is the correct behavior on a platform that never actually calls
// this in the first place (internal/tunnel/rpc.go checks runtime.GOOS
// before ever reaching Apply), not just a stub for compilation.
func checkWindowsDefenderDetection(path string) string {
	return ""
}
