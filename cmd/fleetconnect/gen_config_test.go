package main

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
	"testing"
	"time"

	"fleet-connector/internal/config/local"
)

// writeTempPEMCert generates a real, minimal self-signed cert and writes
// it as a PEM file — config.Validate's ConnectCACertFile check parses
// real x509 PEM data, so --connect-ca-cert-file needs a fixture that's
// actually valid PEM, not a stub path.
func writeTempPEMCert(t *testing.T) string {
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
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create pem file: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode pem: %v", err)
	}
	return path
}

// TestGenConfigConnectionSettings covers the five connection-level flags
// added alongside the wizard's matching "Advanced connection settings"
// section — previously gen-config had no way to set connect_url,
// connect_ca_cert_file, proxy_url, heartbeat_interval, or
// heartbeat_tolerance at all.
func TestGenConfigConnectionSettings(t *testing.T) {
	caCertPath := writeTempPEMCert(t)
	outPath := filepath.Join(t.TempDir(), "config.yaml")

	err := runGenConfig([]string{
		"--path", outPath,
		"--description", "conn-settings-test",
		"--upstream", "localhost:8080",
		"--authtoken", "fake_token_gen_config",
		"--connect-url", "https://connect.example.com:443",
		"--connect-ca-cert-file", caCertPath,
		"--proxy-url", "http://proxy.example.com:8080",
		"--heartbeat-interval", "30s",
		"--heartbeat-tolerance", "1m",
	})
	if err != nil {
		t.Fatalf("runGenConfig: %v", err)
	}

	cfg, err := local.New(outPath).Load(t.Context())
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	if cfg.ConnectURL != "https://connect.example.com:443" {
		t.Errorf("ConnectURL = %q", cfg.ConnectURL)
	}
	if cfg.ConnectCACertFile != caCertPath {
		t.Errorf("ConnectCACertFile = %q, want %q", cfg.ConnectCACertFile, caCertPath)
	}
	if cfg.ProxyURL != "http://proxy.example.com:8080" {
		t.Errorf("ProxyURL = %q", cfg.ProxyURL)
	}
	if cfg.HeartbeatInterval != "30s" {
		t.Errorf("HeartbeatInterval = %q", cfg.HeartbeatInterval)
	}
	if cfg.HeartbeatTolerance != "1m" {
		t.Errorf("HeartbeatTolerance = %q", cfg.HeartbeatTolerance)
	}
}

// TestGenConfigConnectionSettingsOmittedByDefault guards against the
// fields above being set to some non-empty default when the flags are
// simply omitted, which would silently change behavior for every existing
// gen-config invocation that doesn't pass them.
func TestGenConfigConnectionSettingsOmittedByDefault(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "config.yaml")

	err := runGenConfig([]string{
		"--path", outPath,
		"--description", "no-conn-settings",
		"--upstream", "localhost:8080",
		"--authtoken", "fake_token_gen_config",
	})
	if err != nil {
		t.Fatalf("runGenConfig: %v", err)
	}

	cfg, err := local.New(outPath).Load(t.Context())
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	if cfg.ConnectURL != "" || cfg.ConnectCACertFile != "" || cfg.ProxyURL != "" || cfg.HeartbeatInterval != "" || cfg.HeartbeatTolerance != "" {
		t.Errorf("expected all connection-level fields empty by default, got: %+v", cfg)
	}
	if cfg.UpdateSourceURL != "" || cfg.UpdateSignerThumbprint != "" {
		t.Errorf("expected update settings empty by default, got: %+v", cfg)
	}
}

// TestGenConfigUpdateSettings covers --update-source-url and
// --update-signer-thumbprint — previously gen-config had no way to set
// either, meaning the only way to enable the self-update feature was
// hand-editing the generated file afterward.
func TestGenConfigUpdateSettings(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "config.yaml")
	thumbprint := "AABBCCDDEEFF00112233445566778899AABBCCDD"

	err := runGenConfig([]string{
		"--path", outPath,
		"--description", "update-settings-test",
		"--upstream", "localhost:8080",
		"--authtoken", "fake_token_gen_config",
		"--update-source-url", "https://updates.example.com/agent.msi",
		"--update-signer-thumbprint", thumbprint,
	})
	if err != nil {
		t.Fatalf("runGenConfig: %v", err)
	}

	cfg, err := local.New(outPath).Load(t.Context())
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	if cfg.UpdateSourceURL != "https://updates.example.com/agent.msi" {
		t.Errorf("UpdateSourceURL = %q", cfg.UpdateSourceURL)
	}
	if cfg.UpdateSignerThumbprint != thumbprint {
		t.Errorf("UpdateSignerThumbprint = %q, want %q", cfg.UpdateSignerThumbprint, thumbprint)
	}
}

// TestGenConfigUpdateSourceURLRequiresThumbprint confirms gen-config itself
// rejects the same incomplete configuration config.Validate would — setting
// --update-source-url without --update-signer-thumbprint fails at
// generation time instead of producing a config.yaml that only fails later,
// when the agent actually tries to use it.
func TestGenConfigUpdateSourceURLRequiresThumbprint(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "config.yaml")

	err := runGenConfig([]string{
		"--path", outPath,
		"--description", "update-missing-thumbprint",
		"--upstream", "localhost:8080",
		"--authtoken", "fake_token_gen_config",
		"--update-source-url", "https://updates.example.com/agent.msi",
	})
	if err == nil {
		t.Fatal("expected an error when --update-source-url is set without --update-signer-thumbprint")
	}
}
