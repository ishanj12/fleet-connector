//go:build windows

package update

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// diagnoseTimeout bounds this best-effort check — it must never
// meaningfully delay reporting the real error back to the caller, so it's
// deliberately short: if Defender's own PowerShell module is slow to
// respond, better to give up and report the original error plainly than to
// hang the whole Apply attempt on a diagnostic.
const diagnoseTimeout = 10 * time.Second

// checkWindowsDefenderDetection asks Windows Defender directly whether it
// has a recent threat detection referencing path, so a launch failure can
// say "Defender blocked this" instead of a generic OS error that looks
// like a real bug. This is best-effort, not a correctness check: any
// failure to query (Defender not present, PowerShell module missing,
// timeout) is swallowed and reported as "" — an empty result here means
// "no extra context available," never "this check itself failed."
func checkWindowsDefenderDetection(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), diagnoseTimeout)
	defer cancel()

	script := fmt.Sprintf(
		`(Get-MpThreatDetection | Where-Object { $_.Resources -like %s } | Select-Object -First 1 -ExpandProperty ThreatName)`,
		psQuote("*"+path+"*"),
	)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
