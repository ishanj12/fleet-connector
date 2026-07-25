package credentials

import (
	"context"
	"errors"

	"fleet-connector/internal/config"
)

// StaticProvider is the default provider, no I/O — it hands back whatever
// authtoken already landed in Params via gen-config or the wizard.
type StaticProvider struct{}

func (StaticProvider) Name() string { return "static" }

func (StaticProvider) Resolve(_ context.Context, ref config.CredentialRef) (Credential, error) {
	tok := ref.Params["authtoken"]
	if tok == "" {
		return Credential{}, errors.New(`static provider: params["authtoken"] is empty`)
	}
	return Credential{AuthToken: tok}, nil
}
