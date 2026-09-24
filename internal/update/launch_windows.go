//go:build windows

package update

import (
	"fmt"
	"os/exec"
)

// launchInstaller runs the verified MSI silently, detached from this
// process. This matters: msiexec's own upgrade logic (product.wxs's
// MajorUpgrade + ServiceControl) is about to stop the very Windows service
// currently running this code, so the installer must survive that rather
// than being torn down as this process's child. Start (not Run), with no
// Wait and an immediate Process.Release, is what actually detaches it —
// exec.Command alone does not.
func launchInstaller(path string) error {
	cmd := exec.Command("msiexec", "/i", path, "/quiet")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting msiexec: %w", err)
	}
	return cmd.Process.Release()
}
