//go:build !windows

package update

import "errors"

// launchInstaller has no non-Windows implementation — see verify_other.go.
func launchInstaller(path string) error {
	return errors.New("update: self-update is only supported on Windows")
}
