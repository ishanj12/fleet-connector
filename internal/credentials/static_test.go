package credentials

import (
	"context"
	"testing"

	"fleet-connector/internal/config"
)

func TestStaticProviderName(t *testing.T) {
	if got := (StaticProvider{}).Name(); got != "static" {
		t.Errorf("Name() = %q, want %q", got, "static")
	}
}

func TestStaticProviderResolve(t *testing.T) {
	for _, tc := range []struct {
		name    string
		params  map[string]string
		want    string
		wantErr bool
	}{
		{"authtoken present", map[string]string{"authtoken": "secret-tok"}, "secret-tok", false},
		{"authtoken empty string", map[string]string{"authtoken": ""}, "", true},
		{"authtoken key missing entirely", map[string]string{}, "", true},
		{"nil params map", nil, "", true},
		{"other params present but no authtoken", map[string]string{"foo": "bar"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cred, err := (StaticProvider{}).Resolve(context.Background(), config.CredentialRef{Provider: "static", Params: tc.params})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Resolve: err=%v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && cred.AuthToken != tc.want {
				t.Errorf("AuthToken = %q, want %q", cred.AuthToken, tc.want)
			}
		})
	}
}
