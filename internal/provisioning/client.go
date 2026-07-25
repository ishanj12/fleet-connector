// Package provisioning mints per-install ngrok agent authtokens via
// ngrok's Credentials API. It is the only producer of the value that ends
// up in config.CredentialRef.Params["authtoken"] — invoked by gen-config
// or an equivalent ops-run command, never at runtime.
package provisioning

import (
	"context"
	"errors"
	"fmt"

	"github.com/ngrok/ngrok-api-go/v9"
	"github.com/ngrok/ngrok-api-go/v9/credentials"
)

// ProvisionRequest carries the one human-readable label the minted
// credential gets. ACL scoping is deliberately not a field here — it's
// configured directly in ngrok's own dashboard/API against the credential
// this mints, entirely outside this application (see §5).
type ProvisionRequest struct {
	Description string // required — e.g. "install:store-042"
}

// MintCredential calls ngrok's Credentials API to create a new, uniquely
// revocable authtoken credential. apiKey is the ops user's ngrok API key —
// never the agent authtoken, never persisted by this package.
func MintCredential(ctx context.Context, apiKey string, req ProvisionRequest) (authtoken, credentialID string, err error) {
	if req.Description == "" {
		return "", "", errors.New("provisioning: description is required")
	}
	client := credentials.NewClient(ngrok.NewClientConfig(apiKey))

	cred, err := client.Create(ctx, &ngrok.CredentialCreate{
		Description: req.Description,
	})
	if err != nil {
		return "", "", fmt.Errorf("mint credential: %w", err)
	}
	if cred.Token == nil {
		return "", "", errors.New("mint credential: API response had no token")
	}
	return *cred.Token, cred.ID, nil
}
