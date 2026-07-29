package wizard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.ngrok.com/ngrok/v2"

	"fleet-connector/internal/credentials"
)

// writeTempPEMCert generates a real, minimal self-signed cert and writes
// it as a PEM file in t.TempDir() — config.Validate's ConnectCACertFile
// check parses real x509 PEM data, so a fixture path needs to hold one,
// not a stub string, the same technique already used in
// internal/config/validate_test.go and internal/tunnel/mapping_test.go.
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

// fakeAgent/fakeForwarder let handleConfirm's TestConnect probe (see
// server.go) succeed without a real network call — these tests are about
// the wizard's own form/state handling, not connection validation itself
// (that's covered directly in internal/tunnel's own tests). Every method
// on ngrok.Agent/ngrok.EndpointForwarder needs to exist to satisfy the
// interfaces; only Connect/Forward/Disconnect are ever actually called.
type fakeForwarder struct{}

func (fakeForwarder) Agent() ngrok.Agent                     { return nil }
func (fakeForwarder) PoolingEnabled() bool                   { return false }
func (fakeForwarder) Bindings() []string                     { return nil }
func (fakeForwarder) Close() error                           { return nil }
func (fakeForwarder) CloseWithContext(context.Context) error { return nil }
func (fakeForwarder) Description() string                    { return "" }
func (fakeForwarder) Done() <-chan struct{}                  { return nil }
func (fakeForwarder) Wait()                                  {}
func (fakeForwarder) ID() string                             { return "ep-fake" }
func (fakeForwarder) Metadata() string                       { return "" }
func (fakeForwarder) Name() string                           { return "" }
func (fakeForwarder) Protocol() string                       { return "https" }
func (fakeForwarder) AgentTLSTermination() *tls.Config       { return nil }
func (fakeForwarder) TrafficPolicy() string                  { return "" }
func (fakeForwarder) URL() *url.URL                          { u, _ := url.Parse("https://fake.ngrok.app"); return u }
func (fakeForwarder) CreatedAt() time.Time                   { return time.Time{} }
func (fakeForwarder) UpdatedAt() time.Time                   { return time.Time{} }
func (fakeForwarder) TunnelSessionID() string                { return "" }
func (fakeForwarder) TunnelID() string                       { return "" }
func (fakeForwarder) UpstreamProtocol() string               { return "" }
func (fakeForwarder) UpstreamURL() url.URL                   { return url.URL{} }
func (fakeForwarder) UpstreamTLSClientConfig() *tls.Config   { return nil }
func (fakeForwarder) ProxyProtocol() ngrok.ProxyProtoVersion { return "" }

type fakeAgent struct{}

func (fakeAgent) Connect(context.Context) error        { return nil }
func (fakeAgent) Disconnect() error                    { return nil }
func (fakeAgent) Session() (ngrok.AgentSession, error) { return nil, nil }
func (fakeAgent) Endpoints() []ngrok.Endpoint          { return nil }
func (fakeAgent) Listen(context.Context, ...ngrok.EndpointOption) (ngrok.EndpointListener, error) {
	return nil, nil
}
func (fakeAgent) Forward(context.Context, *ngrok.Upstream, ...ngrok.EndpointOption) (ngrok.EndpointForwarder, error) {
	return fakeForwarder{}, nil
}

func fakeFactory(_ ...ngrok.AgentOption) (ngrok.Agent, error) { return fakeAgent{}, nil }

// failingAgent simulates a bad-authtoken-style connect failure, for
// TestServeConfirmSurfacesConnectionFailure below.
type failingAgent struct{ fakeAgent }

func (failingAgent) Connect(context.Context) error {
	return errors.New("authentication failed: The authtoken you specified does not look like a proper ngrok authtoken")
}

func failingFactory(_ ...ngrok.AgentOption) (ngrok.Agent, error) { return failingAgent{}, nil }

func TestServeManualEntrySingleEndpoint(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/?t=" + tok)
	if err != nil {
		t.Fatalf("GET form: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET form: status %d", resp.StatusCode)
	}

	// Not clicking "Save" at all — a single filled-in draft submitted
	// straight to /submit should still work.
	form := url.Values{
		"t":                {tok},
		"saved_count":      {"0"},
		"draft_open":       {"1"},
		"name_new":         {"pos-1"},
		"upstream_url_new": {"localhost:8080"},
		"url_new":          {"https://mystore.ngrok.app"},
		"authtoken":        {"fake_token_1234567890"},
	}
	resp, err = client.PostForm(base+"/submit?t="+tok, form)
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST submit: status %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "fake…7890") {
		t.Errorf("confirm page should mask the token, got: %s", body)
	}
	if !strings.Contains(string(body), `value="fake_token_1234567890"`) {
		t.Errorf("confirm page's hidden field should carry the real token through to /confirm, got: %s", body)
	}

	confirmForm := url.Values{
		"t":                      {tok},
		"confirm_count":          {"1"},
		"confirm_name_0":         {"pos-1"},
		"confirm_upstream_url_0": {"localhost:8080"},
		"confirm_url_0":          {"https://mystore.ngrok.app"},
		"authtoken":              {"fake_token_1234567890"},
	}
	resp, err = client.PostForm(base+"/confirm?t="+tok, confirmForm)
	if err != nil {
		t.Fatalf("POST confirm: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST confirm: status %d", resp.StatusCode)
	}

	if err := <-serveErr; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.yaml was not written: %v", err)
	}
	got := string(data)
	for _, want := range []string{"name: pos-1", "url: localhost:8080", "url: https://mystore.ngrok.app", "authtoken: fake_token_1234567890"} {
		if !strings.Contains(got, want) {
			t.Errorf("config.yaml missing %q, got:\n%s", want, got)
		}
	}
}

// TestServeSaveEndpointAccumulates covers the "Save" button (/save-endpoint)
// actually accumulating endpoints across round trips without losing
// previously-saved ones — the fix for "what if I want to set up more than
// one endpoint."
func TestServeSaveEndpointAccumulates(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 5 * time.Second}

	// Save endpoint 1.
	resp, err := client.PostForm(base+"/save-endpoint?t="+tok, url.Values{
		"t": {tok}, "saved_count": {"0"},
		"name_new": {"pos-1"}, "upstream_url_new": {"localhost:8081"}, "url_new": {"https://pos1.ngrok.app"},
	})
	if err != nil {
		t.Fatalf("POST save-endpoint (1st): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST save-endpoint (1st): status %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "pos-1") || !strings.Contains(string(body), "localhost:8081") {
		t.Fatalf("after saving endpoint 1, form should show it as a saved box, got: %s", body)
	}
	if !strings.Contains(string(body), `name="saved_count" value="1"`) {
		t.Errorf("saved_count should be 1 after saving one endpoint, got: %s", body)
	}
	// Draft should have closed after saving (back to the "+" prompt), not
	// stay open with another blank fieldset.
	if !strings.Contains(string(body), `name="draft_open" value="0"`) {
		t.Errorf("draft should close after Save, got: %s", body)
	}

	// Click "+" to open a fresh draft, carrying endpoint 1 forward.
	resp, err = client.PostForm(base+"/new-endpoint?t="+tok, url.Values{
		"t": {tok}, "saved_count": {"1"},
		"saved_name_0": {"pos-1"}, "saved_upstream_url_0": {"localhost:8081"}, "saved_url_0": {"https://pos1.ngrok.app"},
	})
	if err != nil {
		t.Fatalf("POST new-endpoint: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST new-endpoint: status %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `name="draft_open" value="1"`) {
		t.Errorf("draft should be open after clicking +, got: %s", body)
	}
	if !strings.Contains(string(body), "pos-1") {
		t.Errorf("endpoint 1 should still be shown after clicking +, got: %s", body)
	}

	// Save endpoint 2 — must carry endpoint 1 forward via saved_count=1 +
	// saved_name_0/saved_upstream_url_0/saved_url_0.
	resp, err = client.PostForm(base+"/save-endpoint?t="+tok, url.Values{
		"t": {tok}, "saved_count": {"1"},
		"saved_name_0": {"pos-1"}, "saved_upstream_url_0": {"localhost:8081"}, "saved_url_0": {"https://pos1.ngrok.app"},
		"name_new": {"pos-2"}, "upstream_url_new": {"localhost:8082"}, "url_new": {"https://pos2.ngrok.app"},
	})
	if err != nil {
		t.Fatalf("POST save-endpoint (2nd): %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST save-endpoint (2nd): status %d, body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"pos-1", "localhost:8081", "pos-2", "localhost:8082"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("after saving endpoint 2, form should still show endpoint 1 AND endpoint 2, missing %q in: %s", want, body)
		}
	}

	// Finish via Continue, carrying both saved endpoints forward.
	resp, err = client.PostForm(base+"/submit?t="+tok, url.Values{
		"t": {tok}, "saved_count": {"2"}, "draft_open": {"0"},
		"saved_name_0": {"pos-1"}, "saved_upstream_url_0": {"localhost:8081"}, "saved_url_0": {"https://pos1.ngrok.app"},
		"saved_name_1": {"pos-2"}, "saved_upstream_url_1": {"localhost:8082"}, "saved_url_1": {"https://pos2.ngrok.app"},
		"authtoken": {"fake_shared_token"},
	})
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST submit: status %d, body: %s", resp.StatusCode, body)
	}

	resp, err = client.PostForm(base+"/confirm?t="+tok, url.Values{
		"t": {tok}, "confirm_count": {"2"},
		"confirm_name_0": {"pos-1"}, "confirm_upstream_url_0": {"localhost:8081"}, "confirm_url_0": {"https://pos1.ngrok.app"},
		"confirm_name_1": {"pos-2"}, "confirm_upstream_url_1": {"localhost:8082"}, "confirm_url_1": {"https://pos2.ngrok.app"},
		"authtoken": {"fake_shared_token"},
	})
	if err != nil {
		t.Fatalf("POST confirm: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST confirm: status %d", resp.StatusCode)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.yaml was not written: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"name: pos-1", "url: localhost:8081", "url: https://pos1.ngrok.app",
		"name: pos-2", "url: localhost:8082", "url: https://pos2.ngrok.app",
		"authtoken: fake_shared_token",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.yaml missing %q, got:\n%s", want, got)
		}
	}
}

// TestServeAuthtokenSurvivesNewEndpoint guards against a real bug found
// live: typing an authtoken, then clicking "+" to add a second endpoint,
// silently wiped the authtoken field because it wasn't part of
// sessionFields — every button is a full-page POST-and-re-render, so
// without a server-side value to refill the field with, it just comes
// back empty on the next page. Confirmed by asserting the rendered HTML
// actually contains the authtoken's value= attribute, not just that the
// final config.yaml ends up correct (the original tests never caught this
// because they always resupplied the authtoken manually on every request
// rather than checking what the intermediate rendered page preserves).
func TestServeAuthtokenSurvivesNewEndpoint(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.PostForm(base+"/new-endpoint?t="+tok, url.Values{
		"t": {tok}, "saved_count": {"0"}, "authtoken": {"fake_token_should_survive"},
	})
	if err != nil {
		t.Fatalf("POST new-endpoint: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST new-endpoint: status %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `value="fake_token_should_survive"`) {
		t.Errorf("authtoken should be preserved in the re-rendered form after clicking +, got: %s", body)
	}

	cancel()
	<-serveErr
}

// TestServeRemoveEndpoint covers the trash-can button (/remove-endpoint)
// dropping one saved endpoint while keeping the rest.
func TestServeRemoveEndpoint(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.PostForm(base+"/remove-endpoint?t="+tok+"&index=0", url.Values{
		"t": {tok}, "saved_count": {"2"},
		"saved_name_0": {"pos-1"}, "saved_upstream_url_0": {"localhost:8081"}, "saved_url_0": {"https://pos1.ngrok.app"},
		"saved_name_1": {"pos-2"}, "saved_upstream_url_1": {"localhost:8082"}, "saved_url_1": {"https://pos2.ngrok.app"},
	})
	if err != nil {
		t.Fatalf("POST remove-endpoint: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST remove-endpoint: status %d, body: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "pos-1") {
		t.Errorf("removed endpoint pos-1 should no longer appear, got: %s", body)
	}
	if !strings.Contains(string(body), "pos-2") {
		t.Errorf("remaining endpoint pos-2 should still appear, got: %s", body)
	}
	if !strings.Contains(string(body), `name="saved_count" value="1"`) {
		t.Errorf("saved_count should drop to 1 after removing one of two, got: %s", body)
	}
}

// TestServeEditEndpoint covers the pencil-icon button (/edit-endpoint):
// clicking it should pull that endpoint out of the saved list and reopen
// it as the editable draft, so its fields show up ready to change rather
// than requiring a full remove-then-retype-from-scratch.
func TestServeEditEndpoint(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.PostForm(base+"/edit-endpoint?t="+tok+"&index=0", url.Values{
		"t": {tok}, "saved_count": {"2"},
		"saved_name_0": {"pos-1"}, "saved_upstream_url_0": {"localhost:8081"}, "saved_url_0": {"https://pos1.ngrok.app"},
		"saved_name_1": {"pos-2"}, "saved_upstream_url_1": {"localhost:8082"}, "saved_url_1": {"https://pos2.ngrok.app"},
	})
	if err != nil {
		t.Fatalf("POST edit-endpoint: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST edit-endpoint: status %d, body: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), `name="saved_count" value="2"`) {
		t.Errorf("edited endpoint should no longer be in the saved list, got: %s", body)
	}
	if !strings.Contains(string(body), `name="saved_count" value="1"`) {
		t.Errorf("the other endpoint (pos-2) should remain saved, got: %s", body)
	}
	if !strings.Contains(string(body), "pos-2") {
		t.Errorf("remaining endpoint pos-2 should still appear as a saved box, got: %s", body)
	}
	if !strings.Contains(string(body), `name="draft_open" value="1"`) {
		t.Errorf("draft should be open after clicking edit, got: %s", body)
	}
	if !strings.Contains(string(body), `value="pos-1"`) || !strings.Contains(string(body), `value="localhost:8081"`) {
		t.Errorf("edited endpoint's own values should be pre-filled into the open draft, got: %s", body)
	}
}

// TestServeAdvancedFields covers the "Advanced" per-endpoint fields
// (bindings, pooling_enabled, traffic_policy, upstream protocol/proxy
// protocol/TLS verify, agent TLS termination) actually reaching the
// written config.yaml, not just the always-visible name/upstream/url.
func TestServeAdvancedFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.PostForm(base+"/submit?t="+tok, url.Values{
		"t": {tok}, "saved_count": {"0"}, "draft_open": {"1"},
		"name_new": {"pos-1"}, "upstream_url_new": {"localhost:8080"},
		"bindings_new": {"internal"}, "pooling_enabled_new": {"on"},
		"description_new": {"POS terminal"}, "metadata_new": {"site=042"},
		"upstream_protocol_new": {"http2"}, "upstream_proxy_protocol_new": {"1"},
		"config_description": {"install:store-042"}, "config_log_level": {"debug"},
		"authtoken": {"fake_token_advanced"},
	})
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST submit: status %d, body: %s", resp.StatusCode, body)
	}

	resp, err = client.PostForm(base+"/confirm?t="+tok, url.Values{
		"t": {tok}, "confirm_count": {"1"},
		"confirm_name_0": {"pos-1"}, "confirm_upstream_url_0": {"localhost:8080"},
		"confirm_bindings_0": {"internal"}, "confirm_pooling_enabled_0": {"on"},
		"confirm_description_0": {"POS terminal"}, "confirm_metadata_0": {"site=042"},
		"confirm_upstream_protocol_0": {"http2"}, "confirm_upstream_proxy_protocol_0": {"1"},
		"config_description": {"install:store-042"}, "config_log_level": {"debug"},
		"authtoken": {"fake_token_advanced"},
	})
	if err != nil {
		t.Fatalf("POST confirm: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST confirm: status %d", resp.StatusCode)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.yaml was not written: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"description: install:store-042", "log_level: debug",
		"description: POS terminal", "metadata: site=042",
		"pooling_enabled: true", "protocol: http2", "proxy_protocol: \"1\"",
		"- internal",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.yaml missing %q, got:\n%s", want, got)
		}
	}
}

// TestServeConnectionSettings covers the five Config-level fields
// (ConnectURL, ConnectCACertFile, ProxyURL, HeartbeatInterval,
// HeartbeatTolerance) added to the wizard's "Advanced connection
// settings" section — previously settable only by hand-editing
// config.yaml directly or via gen-config's matching flags, not through
// the wizard at all. Verifies both that submit->confirm preserves them
// (the same round-trip bug class the authtoken fix above guards against)
// and that they land correctly in the final written config.yaml.
func TestServeConnectionSettings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 5 * time.Second}

	caCertPath := writeTempPEMCert(t)

	connectionFields := url.Values{
		"t": {tok}, "saved_count": {"0"}, "draft_open": {"1"},
		"name_new": {"pos-1"}, "upstream_url_new": {"localhost:8080"},
		"authtoken":                   {"fake_token_conn_settings"},
		"config_connect_url":          {"https://connect.example.com:443"},
		"config_connect_ca_cert_file": {caCertPath},
		"config_proxy_url":            {"http://proxy.example.com:8080"},
		"config_heartbeat_interval":   {"30s"},
		"config_heartbeat_tolerance":  {"1m"},
	}

	resp, err := client.PostForm(base+"/submit?t="+tok, connectionFields)
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST submit: status %d, body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`value="https://connect.example.com:443"`,
		`value="` + caCertPath + `"`,
		`value="http://proxy.example.com:8080"`,
		`value="30s"`,
		`value="1m"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("confirm.html missing round-tripped %q, got:\n%s", want, body)
		}
	}

	resp, err = client.PostForm(base+"/confirm?t="+tok, url.Values{
		"t": {tok}, "confirm_count": {"1"},
		"confirm_name_0": {"pos-1"}, "confirm_upstream_url_0": {"localhost:8080"},
		"authtoken":                   {"fake_token_conn_settings"},
		"config_connect_url":          {"https://connect.example.com:443"},
		"config_connect_ca_cert_file": {caCertPath},
		"config_proxy_url":            {"http://proxy.example.com:8080"},
		"config_heartbeat_interval":   {"30s"},
		"config_heartbeat_tolerance":  {"1m"},
	})
	if err != nil {
		t.Fatalf("POST confirm: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST confirm: status %d", resp.StatusCode)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.yaml was not written: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"connect_url: https://connect.example.com:443",
		"connect_ca_cert_file: " + caCertPath,
		"proxy_url: http://proxy.example.com:8080",
		"heartbeat_interval: 30s",
		"heartbeat_tolerance: 1m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.yaml missing %q, got:\n%s", want, got)
		}
	}
}

// TestServeConfirmSurfacesConnectionFailure guards the actual fix: a bad
// authtoken (or any other connect/forward-time failure) must be caught by
// a real test connection before config.yaml is ever written, and shown
// back on the same confirm screen with everything preserved — confirmed
// live that without this, the wizard reported "done" regardless, and the
// only place the failure ever showed up was the log file, well after the
// wizard itself had already exited.
func TestServeConfirmSurfacesConnectionFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- Serve(ctx, configPath, credentials.StaticProvider{}, failingFactory, log)
	}()

	wizardURL := waitForURL(t, &logBuf)
	base, tok := splitURL(t, wizardURL)
	client := &http.Client{Timeout: 25 * time.Second}

	resp, err := client.PostForm(base+"/confirm?t="+tok, url.Values{
		"t": {tok}, "confirm_count": {"1"},
		"confirm_name_0": {"pos-1"}, "confirm_upstream_url_0": {"localhost:8080"}, "confirm_url_0": {"https://mystore.ngrok.app"},
		"authtoken": {"bad_token"},
	})
	if err != nil {
		t.Fatalf("POST confirm: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST confirm: status %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "could not connect") {
		t.Errorf("confirm page should surface the connection failure, got: %s", body)
	}
	if !strings.Contains(string(body), `value="bad_token"`) {
		t.Errorf("authtoken should be preserved on the re-rendered confirm page, got: %s", body)
	}
	if !strings.Contains(string(body), "pos-1") {
		t.Errorf("endpoint should still be shown on the re-rendered confirm page, got: %s", body)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("config.yaml should NOT be written when the connection test fails, stat err: %v", err)
	}

	cancel()
	<-serveErr
}

func TestServeRejectsBadToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { Serve(ctx, configPath, credentials.StaticProvider{}, fakeFactory, log) }()

	wizardURL := waitForURL(t, &logBuf)
	base, _ := splitURL(t, wizardURL)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/?t=wrong-token")
	if err != nil {
		t.Fatalf("GET form: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad token, got %d", resp.StatusCode)
	}
}

var urlRe = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?t=[0-9a-f]+`)

func waitForURL(t *testing.T, buf *strings.Builder) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m := urlRe.FindString(buf.String()); m != "" {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wizard URL never appeared in logs: %s", buf.String())
	return ""
}

func splitURL(t *testing.T, wizardURL string) (base, token string) {
	t.Helper()
	u, err := url.Parse(wizardURL)
	if err != nil {
		t.Fatalf("parse wizard URL: %v", err)
	}
	return u.Scheme + "://" + u.Host, u.Query().Get("t")
}
