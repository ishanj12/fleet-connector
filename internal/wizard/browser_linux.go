package wizard

import "os/exec"

// openBrowser is best-effort: a headless Linux server commonly has no
// display and no browser at all (§6) — xdg-open simply fails there, which
// Serve treats as a warning, not a fatal error. The documented recovery
// for that case is an SSH local port-forward to the logged URL.
func openBrowser(url string) error {
	return exec.Command("xdg-open", url).Start()
}
