// Package update implements the agent's optional self-update flow: when
// ngrok's dashboard/API sends an UpdateAgentMethod RPC (see
// internal/tunnel/rpc.go), Apply downloads a new installer, verifies it,
// and launches it silently. Disabled unless config.Config.UpdateSourceURL
// is set — this reference implementation has no distribution point of its
// own; an adopting customer wires this to wherever they host their own
// signed build.
//
// Windows-only today: the RPC itself arrives on any platform (it's part of
// the SDK's session protocol, not OS-specific), but the actual
// download-verify-relaunch mechanism assumes an MSI and Windows Authenticode
// signing — see verify_windows.go/launch_windows.go and their _other.go
// stubs. internal/tunnel/rpc.go checks runtime.GOOS before ever calling
// Apply, so the _other.go stubs exist only to keep this package building
// during cross-platform development, not as a real code path.
package update

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// VerifyFunc checks that the file at path is validly signed. Apply refuses
// to launch anything that fails this check — it's the one non-negotiable
// safety gate in this whole package. That matters especially because
// UpdateSourceURL may end up being a plain, unauthenticated location in
// practice (an unattended agent can't complete an interactive SSO/MFA login
// the way a human fetching the same file could) — so this check, not who
// can reach the URL, is what actually has to be trusted.
type VerifyFunc func(path string) error

// LaunchFunc runs the verified installer, detached from this process — the
// installer's own upgrade logic (MSI MajorUpgrade) is expected to stop the
// very service running this code partway through, so the launched process
// must survive that rather than being torn down along with it.
type LaunchFunc func(path string) error

// DefaultLaunch is the real, platform-specific implementation (see
// launch_windows.go). New takes it as an explicit parameter rather than
// hardcoding it, the same test-seam pattern as tunnel.AgentFactory — real
// callers pass this, tests pass a fake.
var DefaultLaunch LaunchFunc = launchInstaller

// DefaultVerify returns the real, platform-specific verify function (see
// verify_windows.go), pinned to expectedThumbprint — the certificate
// thumbprint (SHA-1 or SHA-256, as reported by PowerShell's
// Get-AuthenticodeSignature) that must have signed the downloaded file.
// This is a constructor rather than a bare var because the expected
// thumbprint is a config value, not something the package can hardcode —
// unlike DefaultLaunch, which needs no such parameter.
func DefaultVerify(expectedThumbprint string) VerifyFunc {
	return func(path string) error {
		return verifySignature(path, expectedThumbprint)
	}
}

// parseVerifyOutput checks Get-AuthenticodeSignature's result (see
// verify_windows.go, which produces this exact two-line "Status\nThumbprint"
// format) against expectedThumbprint. Split out from the PowerShell
// invocation itself so this logic — confirming the signer is who it's
// supposed to be, not just that *some* certificate signed the file — is
// unit-testable on any platform, not just Windows. Checking Status alone
// isn't a meaningful safety gate on its own: anyone with their own
// legitimately-issued signing certificate would also produce a "Valid"
// status, so the thumbprint comparison is the actual check that matters.
func parseVerifyOutput(output, expectedThumbprint string) error {
	lines := strings.SplitN(strings.TrimSpace(output), "\n", 2)
	status := strings.TrimSpace(lines[0])
	if status != "Valid" {
		return fmt.Errorf("signature status %q, expected %q", status, "Valid")
	}

	var thumbprint string
	if len(lines) > 1 {
		thumbprint = strings.TrimSpace(lines[1])
	}
	if !strings.EqualFold(thumbprint, expectedThumbprint) {
		return fmt.Errorf("signed by unexpected certificate (thumbprint %q, expected %q)", thumbprint, expectedThumbprint)
	}
	return nil
}

// downloadTimeout bounds the whole download — an installer is at most tens
// of MB, so a slow/stalled fetch should fail rather than hang the update
// attempt (and the goroutine running it) indefinitely.
const downloadTimeout = 5 * time.Minute

// Applier downloads, verifies, and launches an update. Not safe for use
// before New.
type Applier struct {
	log        *slog.Logger
	httpClient *http.Client
	verify     VerifyFunc
	launch     LaunchFunc
}

// New builds an Applier. verify/launch are almost always DefaultVerify/
// DefaultLaunch in production; tests substitute fakes to exercise Apply's
// download/verify/launch sequencing without needing a real signed installer
// or a Windows machine.
func New(log *slog.Logger, verify VerifyFunc, launch LaunchFunc) *Applier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Applier{
		log:        log,
		httpClient: &http.Client{Timeout: downloadTimeout},
		verify:     verify,
		launch:     launch,
	}
}

// Apply downloads the installer at sourceURL, verifies it, and launches it.
// The downloaded file is always removed afterward, including on a
// verification failure, so a rejected file never lingers on disk.
func (a *Applier) Apply(ctx context.Context, sourceURL string) error {
	path, err := a.download(ctx, sourceURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", sourceURL, err)
	}
	defer os.Remove(path)

	if err := a.verify(path); err != nil {
		return fmt.Errorf("signature verification failed, refusing to install: %w", err)
	}
	a.log.Info("update signature verified, launching installer")

	if err := a.launch(path); err != nil {
		return fmt.Errorf("launch installer: %w", err)
	}
	return nil
}

// download fetches sourceURL to a temp file and returns its path. The
// caller owns cleanup (Apply always removes it, verified or not).
func (a *Applier) download(ctx context.Context, sourceURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp("", "fleetconnect-update-*.msi")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
