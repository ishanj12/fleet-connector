//go:build windows

package update

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// verifyTimeout bounds the PowerShell call — Get-AuthenticodeSignature is
// normally fast (local file, no network), so a hang here almost certainly
// means something is wrong, not that it needs more time.
const verifyTimeout = 30 * time.Second

// verifySignature shells out to PowerShell's Get-AuthenticodeSignature —
// Go's standard library has no Authenticode support, and this is the
// built-in, already-present-on-every-Windows-box way to check one. Prints
// the signature's Status and its signer certificate's Thumbprint on
// separate lines; parseVerifyOutput (update.go) does the actual pass/fail
// logic against expectedThumbprint, kept separate so that logic is
// testable without a real Windows machine. See update.go's VerifyFunc doc
// comment for why checking the thumbprint, not just the status, is what
// carries the whole weight of the update flow's safety.
func verifySignature(path, expectedThumbprint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()

	// SignerCertificate is nil when Status isn't a real signature (e.g.
	// NotSigned), so the script guards against a null-reference error
	// there rather than letting PowerShell itself fail with an unrelated
	// message.
	script := fmt.Sprintf(
		`$sig = Get-AuthenticodeSignature -LiteralPath %s; `+
			`$thumbprint = if ($sig.SignerCertificate) { $sig.SignerCertificate.Thumbprint } else { "" }; `+
			`"$($sig.Status)`+"`n"+`$thumbprint"`,
		psQuote(path),
	)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("running Get-AuthenticodeSignature: %w", err)
	}

	return parseVerifyOutput(string(out), expectedThumbprint)
}

// psQuote wraps path as a single-quoted PowerShell string literal, doubling
// any embedded single quotes (PowerShell's own escaping rule for this case)
// — powershell.exe -Command parses its argument as PowerShell source, so
// this is not the same escaping a POSIX shell would need.
func psQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}
