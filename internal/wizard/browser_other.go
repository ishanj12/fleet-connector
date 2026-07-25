//go:build !windows && !linux

package wizard

import "os/exec"

// Only windows and linux are shipping targets (§1); this exists purely so
// the module builds and runs natively on a developer's own machine.
func openBrowser(url string) error {
	return exec.Command("open", url).Start()
}
