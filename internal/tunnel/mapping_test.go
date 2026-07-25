package tunnel

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

	"fleet-connector/internal/config"
)

// writeTempCertKeyPair generates a real, minimal self-signed cert/key pair
// and writes both as PEM files in t.TempDir() — mapping.Build's TLS-related
// paths (agent_tls_termination, upstream.tls_verify_cas,
// mutual_tls_certificate_authorities) all go through real crypto/tls and
// crypto/x509 parsing, so a fixture needs to be real PEM, not a stub.
func writeTempCertKeyPair(t *testing.T) (certPath, keyPath string) {
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
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestBuildMinimalEndpointHasNoOptionalOptions(t *testing.T) {
	upstream, opts, err := Build(config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if upstream == nil {
		t.Fatal("Build returned a nil upstream")
	}
	if len(opts) != 0 {
		t.Errorf("expected no EndpointOptions for a bare endpoint, got %d", len(opts))
	}
}

func TestBuildAllSimpleFieldsProduceOneOptionEach(t *testing.T) {
	ep := config.Endpoint{
		Upstream:       config.Upstream{URL: "localhost:8080"},
		URL:            "https://example.ngrok.app",
		Name:           "pos-1",
		Description:    "POS terminal",
		Metadata:       "site=042",
		TrafficPolicy:  "on_http_request: []",
		Bindings:       []string{"internal"},
		PoolingEnabled: true,
	}
	_, opts, err := Build(ep)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// URL, Name, Description, Metadata, TrafficPolicy, Bindings,
	// PoolingEnabled — seven independent optional fields, each
	// contributing exactly one EndpointOption when set.
	if len(opts) != 7 {
		t.Errorf("expected 7 EndpointOptions with all simple fields set, got %d", len(opts))
	}
}

func TestBuildAgentTLSTerminationSuccess(t *testing.T) {
	certPath, keyPath := writeTempCertKeyPair(t)
	ep := config.Endpoint{
		Upstream: config.Upstream{URL: "localhost:8080"},
		AgentTLSTermination: &config.AgentTLSTermination{
			ServerCertificate: certPath,
			ServerPrivateKey:  keyPath,
		},
	}
	_, opts, err := Build(ep)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(opts) != 1 {
		t.Errorf("expected exactly 1 EndpointOption (AgentTLSTermination), got %d", len(opts))
	}
}

func TestBuildAgentTLSTerminationWithMutualTLS(t *testing.T) {
	certPath, keyPath := writeTempCertKeyPair(t)
	caCertPath, _ := writeTempCertKeyPair(t) // reuse the cert half as a stand-in CA bundle
	ep := config.Endpoint{
		Upstream: config.Upstream{URL: "localhost:8080"},
		AgentTLSTermination: &config.AgentTLSTermination{
			ServerCertificate:               certPath,
			ServerPrivateKey:                keyPath,
			MutualTLSCertificateAuthorities: caCertPath,
		},
	}
	if _, _, err := Build(ep); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestBuildAgentTLSTerminationBadCertPathErrors(t *testing.T) {
	ep := config.Endpoint{
		Upstream: config.Upstream{URL: "localhost:8080"},
		AgentTLSTermination: &config.AgentTLSTermination{
			ServerCertificate: "/nonexistent/cert.pem",
			ServerPrivateKey:  "/nonexistent/key.pem",
		},
	}
	_, _, err := Build(ep)
	if err == nil {
		t.Fatal("expected an error for a nonexistent agent TLS cert/key path")
	}
	if !strings.Contains(err.Error(), "agent_tls_termination") {
		t.Errorf("error should mention agent_tls_termination for context, got: %v", err)
	}
}

func TestBuildAgentTLSTerminationBadMutualTLSCAsErrors(t *testing.T) {
	certPath, keyPath := writeTempCertKeyPair(t)
	ep := config.Endpoint{
		Upstream: config.Upstream{URL: "localhost:8080"},
		AgentTLSTermination: &config.AgentTLSTermination{
			ServerCertificate:               certPath,
			ServerPrivateKey:                keyPath,
			MutualTLSCertificateAuthorities: "/nonexistent/ca.pem",
		},
	}
	_, _, err := Build(ep)
	if err == nil {
		t.Fatal("expected an error for a nonexistent mutual_tls_certificate_authorities path")
	}
	if !strings.Contains(err.Error(), "mutual_tls_certificate_authorities") {
		t.Errorf("error should mention mutual_tls_certificate_authorities for context, got: %v", err)
	}
}

func TestBuildUpstreamTLSVerifyWithCAs(t *testing.T) {
	caCertPath, _ := writeTempCertKeyPair(t)
	ep := config.Endpoint{
		Upstream: config.Upstream{
			URL:          "localhost:8080",
			TLSVerify:    true,
			TLSVerifyCAs: caCertPath,
		},
	}
	// TLSVerify/TLSVerifyCAs contribute an UpstreamOption (folded into the
	// returned *ngrok.Upstream itself), not an EndpointOption — same
	// category as proxy_protocol below — so opts stays empty; the
	// meaningful assertion here is just that loading the real CA file
	// succeeds without error.
	if _, _, err := Build(ep); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestBuildUpstreamTLSVerifyCAsBadPathErrors(t *testing.T) {
	ep := config.Endpoint{
		Upstream: config.Upstream{
			URL:          "localhost:8080",
			TLSVerify:    true,
			TLSVerifyCAs: "/nonexistent/ca.pem",
		},
	}
	_, _, err := Build(ep)
	if err == nil {
		t.Fatal("expected an error for a nonexistent upstream.tls_verify_cas path")
	}
	if !strings.Contains(err.Error(), "tls_verify_cas") {
		t.Errorf("error should mention tls_verify_cas for context, got: %v", err)
	}
}

func TestBuildUpstreamProxyProtocolOnlyRecognizesValidValues(t *testing.T) {
	for _, tc := range []struct {
		value    string
		wantOpts int
	}{
		{"1", 0}, // ProxyProtocol itself contributes an UpstreamOption, not an EndpointOption — opts stays 0
		{"2", 0},
		{"", 0},
		{"bogus", 0}, // an invalid value is silently ignored by proxyProtoVersions' lookup, not an error — Validate() is what rejects this earlier in the pipeline
	} {
		ep := config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080", ProxyProtocol: tc.value}}
		_, opts, err := Build(ep)
		if err != nil {
			t.Fatalf("proxy_protocol=%q: Build: %v", tc.value, err)
		}
		if len(opts) != tc.wantOpts {
			t.Errorf("proxy_protocol=%q: got %d EndpointOptions, want %d", tc.value, len(opts), tc.wantOpts)
		}
	}
}

// TestNormalizeUpstreamAddr guards against a real parsing gap confirmed
// directly against a live ngrok endpoint: net/url.Parse doesn't treat a
// bare port or a schemeless "host:port" as a hierarchical host:port at
// all ("localhost:8080" parses with Scheme="localhost", Opaque="8080";
// "8080" alone parses as a relative Path), so Hostname()/Port() come back
// empty and the SDK silently dials "localhost:80" instead of the
// configured port. See normalizeUpstreamAddr's doc comment — the default
// scheme it picks for a schemeless address also depends on the
// endpoint's own public scheme, since the SDK's forwarder picks its
// entire forwarding strategy (HTTP reverse-proxy vs. raw byte stream)
// off the upstream URL's scheme alone.
func TestNormalizeUpstreamAddr(t *testing.T) {
	for _, tc := range []struct {
		name        string
		addr        string
		endpointURL string
		want        string
	}{
		{"bare port, default (https) endpoint", "8080", "", "http://localhost:8080"},
		{"bare host:port, default (https) endpoint", "localhost:8080", "", "http://localhost:8080"},
		{"bare host:port, non-localhost", "myhost:9000", "", "http://myhost:9000"},
		{"bare host:port, explicit https endpoint", "localhost:8080", "https://example.ngrok.app", "http://localhost:8080"},
		{"bare host:port, tcp endpoint (e.g. RDP)", "localhost:3389", "tcp://1.tcp.ngrok.io:12345", "tcp://localhost:3389"},
		{"bare host:port, tls endpoint", "localhost:5432", "tls://example.ngrok.app", "tcp://localhost:5432"},
		{"already has http scheme", "http://localhost:8080", "", "http://localhost:8080"},
		{"already has https scheme", "https://example.com", "", "https://example.com"},
		{"already has tcp scheme, even under a tcp endpoint", "tcp://localhost:5432", "tcp://1.tcp.ngrok.io:12345", "tcp://localhost:5432"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeUpstreamAddr(tc.addr, tc.endpointURL); got != tc.want {
				t.Errorf("normalizeUpstreamAddr(%q, %q) = %q, want %q", tc.addr, tc.endpointURL, got, tc.want)
			}
		})
	}
}
