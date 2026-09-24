//go:build !windows

package update

import "errors"

// verifySignature has no non-Windows implementation — the self-update
// mechanism assumes an MSI and Windows Authenticode signing (see the
// package doc comment). internal/tunnel/rpc.go checks runtime.GOOS before
// ever calling into this package on a real UpdateAgentMethod RPC, so in
// practice this only exists to keep the module building during
// cross-platform development.
func verifySignature(path, expectedThumbprint string) error {
	return errors.New("update: self-update is only supported on Windows")
}
