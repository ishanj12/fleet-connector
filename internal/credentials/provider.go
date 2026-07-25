package credentials

import (
	"context"

	"fleet-connector/internal/config"
)

// Credential is what a Provider resolves a config.CredentialRef to.
type Credential struct {
	AuthToken string
}

// Provider is invoked by the tunnel manager at connect-time and can be
// re-invoked mid-run (rotation, post-auth-failure retry) without reloading
// config.
type Provider interface {
	Name() string
	Resolve(ctx context.Context, ref config.CredentialRef) (Credential, error)
}
