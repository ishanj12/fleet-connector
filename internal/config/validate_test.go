package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTempPEMFile generates a real, minimal self-signed certificate and
// writes just the cert half as a PEM file — Validate's PEM-checking paths
// (connect_ca_cert_file, upstream.tls_verify_cas,
// agent_tls_termination.mutual_tls_certificate_authorities) all go through
// real x509.CertPool.AppendCertsFromPEM parsing, so a fixture needs to be
// genuinely valid PEM, not a stub.
func writeTempPEMFile(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fleet-connector-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	return path
}

func writeTempBogusFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bogus.pem")
	if err := os.WriteFile(path, []byte("not a pem file"), 0o600); err != nil {
		t.Fatalf("write bogus file: %v", err)
	}
	return path
}

func validConfig() Config {
	return Config{
		SchemaVersion: CurrentSchemaVersion,
		Credential:    CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "tok"}},
		Endpoints: []Endpoint{
			{Name: "pos-1", Upstream: Upstream{URL: "localhost:8080"}},
		},
	}
}

func TestValidateValidConfigPasses(t *testing.T) {
	if err := Validate(validConfig()); err != nil {
		t.Errorf("expected valid config to pass, got: %v", err)
	}
}

func TestValidateValidConfigWithNoEndpointsPasses(t *testing.T) {
	cfg := validConfig()
	cfg.Endpoints = nil
	if err := Validate(cfg); err != nil {
		t.Errorf("zero endpoints should be valid (matches the SDK), got: %v", err)
	}
}

func TestValidateSchemaVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		wantErr bool
	}{
		{"current version passes", CurrentSchemaVersion, false},
		{"zero value (unset field) rejected", 0, true},
		{"unrecognized future version rejected", CurrentSchemaVersion + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.SchemaVersion = tc.version
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("SchemaVersion=%d: err=%v, wantErr=%v", tc.version, err, tc.wantErr)
			}
		})
	}
}

func TestValidateCredentialProviderRequired(t *testing.T) {
	cfg := validConfig()
	cfg.Credential.Provider = ""
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for empty credential.provider")
	}
}

func TestValidateLogLevel(t *testing.T) {
	for _, tc := range []struct {
		level   string
		wantErr bool
	}{
		{"", false},
		{"debug", false},
		{"info", false},
		{"warn", false},
		{"error", false},
		{"crit", true}, // real agent's own enum has this, slog doesn't
		{"bogus", true},
	} {
		t.Run(tc.level, func(t *testing.T) {
			cfg := validConfig()
			cfg.LogLevel = tc.level
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("log_level=%q: err=%v, wantErr=%v", tc.level, err, tc.wantErr)
			}
		})
	}
}

func TestValidateEndpointUpstreamURLRequired(t *testing.T) {
	cfg := validConfig()
	cfg.Endpoints[0].Upstream.URL = ""
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for empty upstream.url")
	}
}

func TestValidateEndpointProxyProtocol(t *testing.T) {
	for _, tc := range []struct {
		value   string
		wantErr bool
	}{
		{"", false},
		{"1", false},
		{"2", false},
		{"3", true},
		{"bogus", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoints[0].Upstream.ProxyProtocol = tc.value
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("proxy_protocol=%q: err=%v, wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestValidateEndpointNameUniqueness(t *testing.T) {
	cfg := validConfig()
	cfg.Endpoints = []Endpoint{
		{Name: "dup", Upstream: Upstream{URL: "localhost:8080"}},
		{Name: "dup", Upstream: Upstream{URL: "localhost:8081"}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for duplicate non-blank endpoint names")
	}
}

func TestValidateEndpointBlankNamesDoNotCollide(t *testing.T) {
	cfg := validConfig()
	cfg.Endpoints = []Endpoint{
		{Upstream: Upstream{URL: "localhost:8080"}},
		{Upstream: Upstream{URL: "localhost:8081"}},
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("two blank-name endpoints should not collide, got: %v", err)
	}
}

func TestValidateEndpointURLScheme(t *testing.T) {
	for _, tc := range []struct {
		url     string
		wantErr bool
	}{
		{"", false},
		{"https://example.ngrok.app", false},
		{"http://example.ngrok.app", false},
		{"tcp://1.tcp.ngrok.io:12345", false},
		{"tls://example.ngrok.app", false},
		{"ftp://example.ngrok.app", true},
		{":not a url:", true},
	} {
		t.Run(tc.url, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoints[0].URL = tc.url
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("url=%q: err=%v, wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestValidateAgentTLSTerminationRequiresBothCertAndKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		t       *AgentTLSTermination
		wantErr bool
	}{
		{"nil is fine", nil, false},
		{"both set", &AgentTLSTermination{ServerCertificate: "cert.pem", ServerPrivateKey: "key.pem"}, false},
		{"only cert", &AgentTLSTermination{ServerCertificate: "cert.pem"}, true},
		{"only key", &AgentTLSTermination{ServerPrivateKey: "key.pem"}, true},
		{"neither", &AgentTLSTermination{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoints[0].AgentTLSTermination = tc.t
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestValidateAgentTLSTerminationMutualTLSCAsMustBeValidPEM(t *testing.T) {
	goodPEM := writeTempPEMFile(t)
	bogus := writeTempBogusFile(t)

	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid PEM file", goodPEM, false},
		{"nonexistent path", "/nonexistent/ca.pem", true},
		{"not a PEM file", bogus, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoints[0].AgentTLSTermination = &AgentTLSTermination{
				ServerCertificate:               "cert.pem",
				ServerPrivateKey:                "key.pem",
				MutualTLSCertificateAuthorities: tc.path,
			}
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "mutual_tls_certificate_authorities") {
				t.Errorf("error should mention mutual_tls_certificate_authorities, got: %v", err)
			}
		})
	}
}

func TestValidateUpstreamTLSVerifyCAsMustBeValidPEM(t *testing.T) {
	goodPEM := writeTempPEMFile(t)
	bogus := writeTempBogusFile(t)

	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid PEM file", goodPEM, false},
		{"nonexistent path", "/nonexistent/ca.pem", true},
		{"not a PEM file", bogus, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoints[0].Upstream.TLSVerifyCAs = tc.path
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "tls_verify_cas") {
				t.Errorf("error should mention tls_verify_cas, got: %v", err)
			}
		})
	}
}

func TestValidateProxyURLMustParse(t *testing.T) {
	cfg := validConfig()
	cfg.ProxyURL = "http://proxy.internal:3128"
	if err := Validate(cfg); err != nil {
		t.Errorf("well-formed proxy_url should pass, got: %v", err)
	}

	cfg.ProxyURL = ":not a url:"
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for a malformed proxy_url")
	}
}

func TestValidateHeartbeatDurations(t *testing.T) {
	for _, tc := range []struct {
		field   string
		value   string
		wantErr bool
	}{
		{"interval", "30s", false},
		{"interval", "not-a-duration", true},
		{"tolerance", "5m", false},
		{"tolerance", "not-a-duration", true},
	} {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			cfg := validConfig()
			if tc.field == "interval" {
				cfg.HeartbeatInterval = tc.value
			} else {
				cfg.HeartbeatTolerance = tc.value
			}
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("%s=%q: err=%v, wantErr=%v", tc.field, tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestValidateConnectCACertFileMustBeValidPEM(t *testing.T) {
	goodPEM := writeTempPEMFile(t)
	bogus := writeTempBogusFile(t)

	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid PEM file", goodPEM, false},
		{"nonexistent path", "/nonexistent/ca.pem", true},
		{"not a PEM file", bogus, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.ConnectCACertFile = tc.path
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "connect_ca_cert_file") {
				t.Errorf("error should mention connect_ca_cert_file, got: %v", err)
			}
		})
	}
}

func TestValidateUpdateSourceURLRequiresSignerThumbprint(t *testing.T) {
	sha1Thumbprint := strings.Repeat("a", 40)

	cfg := validConfig()
	cfg.UpdateSourceURL = "https://updates.example.com/agent.msi"
	// UpdateSignerThumbprint deliberately left unset.
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error when update_source_url is set without update_signer_thumbprint")
	} else if !strings.Contains(err.Error(), "update_signer_thumbprint") {
		t.Errorf("error should mention update_signer_thumbprint, got: %v", err)
	}

	cfg.UpdateSignerThumbprint = sha1Thumbprint
	if err := Validate(cfg); err != nil {
		t.Errorf("update_source_url with a valid update_signer_thumbprint should pass, got: %v", err)
	}
}

func TestValidateUpdateSourceURLMustParse(t *testing.T) {
	cfg := validConfig()
	cfg.UpdateSourceURL = ":not a url:"
	cfg.UpdateSignerThumbprint = strings.Repeat("a", 40)
	if err := Validate(cfg); err == nil {
		t.Fatal("expected an error for a malformed update_source_url")
	}
}

func TestValidateUpdateSignerThumbprintFormat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"40-char hex (SHA-1)", strings.Repeat("a", 40), false},
		{"64-char hex (SHA-256)", strings.Repeat("a", 64), false},
		{"uppercase hex", strings.Repeat("A", 40), false},
		{"too short", strings.Repeat("a", 39), true},
		{"too long", strings.Repeat("a", 41), true},
		{"non-hex characters", strings.Repeat("z", 40), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.UpdateSourceURL = "https://updates.example.com/agent.msi"
			cfg.UpdateSignerThumbprint = tc.value
			err := Validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("thumbprint %q: err=%v, wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}
