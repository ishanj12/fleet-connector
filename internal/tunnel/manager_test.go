package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.ngrok.com/ngrok/v2"
	"golang.ngrok.com/ngrok/v2/rpc"

	"fleet-connector/internal/config"
	"fleet-connector/internal/credentials"
	"fleet-connector/internal/update"
)

// fakeForwarder is the minimal stand-in for ngrok.EndpointForwarder — only
// URL() is ever actually read by Manager, everything else just needs to
// satisfy the interface.
type fakeForwarder struct{ url *url.URL }

func (f *fakeForwarder) Agent() ngrok.Agent                     { return nil }
func (f *fakeForwarder) PoolingEnabled() bool                   { return false }
func (f *fakeForwarder) Bindings() []string                     { return nil }
func (f *fakeForwarder) Close() error                           { return nil }
func (f *fakeForwarder) CloseWithContext(context.Context) error { return nil }
func (f *fakeForwarder) Description() string                    { return "" }
func (f *fakeForwarder) Done() <-chan struct{}                  { return nil }
func (f *fakeForwarder) Wait()                                  {}
func (f *fakeForwarder) ID() string                             { return "ep-fake" }
func (f *fakeForwarder) Metadata() string                       { return "" }
func (f *fakeForwarder) Name() string                           { return "" }
func (f *fakeForwarder) Protocol() string                       { return f.url.Scheme }
func (f *fakeForwarder) AgentTLSTermination() *tls.Config       { return nil }
func (f *fakeForwarder) TrafficPolicy() string                  { return "" }
func (f *fakeForwarder) URL() *url.URL                          { return f.url }
func (f *fakeForwarder) CreatedAt() time.Time                   { return time.Time{} }
func (f *fakeForwarder) UpdatedAt() time.Time                   { return time.Time{} }
func (f *fakeForwarder) TunnelSessionID() string                { return "" }
func (f *fakeForwarder) TunnelID() string                       { return "" }
func (f *fakeForwarder) UpstreamProtocol() string               { return "" }
func (f *fakeForwarder) UpstreamURL() url.URL                   { return url.URL{} }
func (f *fakeForwarder) UpstreamTLSClientConfig() *tls.Config   { return nil }
func (f *fakeForwarder) ProxyProtocol() ngrok.ProxyProtoVersion { return "" }

// fakeAgent is a scriptable stand-in for ngrok.Agent — Manager only ever
// calls Connect/Disconnect/Forward, so that's all that's wired up.
type fakeAgent struct {
	mu              sync.Mutex
	connectErr      error
	forwardErr      error
	connectCalls    int
	disconnectCalls int
	forwardCalls    int
}

func (a *fakeAgent) Connect(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connectCalls++
	return a.connectErr
}

func (a *fakeAgent) Disconnect() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.disconnectCalls++
	return nil
}

func (a *fakeAgent) Session() (ngrok.AgentSession, error) { return nil, nil }
func (a *fakeAgent) Endpoints() []ngrok.Endpoint          { return nil }
func (a *fakeAgent) Listen(context.Context, ...ngrok.EndpointOption) (ngrok.EndpointListener, error) {
	return nil, errors.New("fakeAgent: Listen not implemented")
}

func (a *fakeAgent) Forward(_ context.Context, _ *ngrok.Upstream, _ ...ngrok.EndpointOption) (ngrok.EndpointForwarder, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forwardCalls++
	if a.forwardErr != nil {
		return nil, a.forwardErr
	}
	u, _ := url.Parse("https://fake.ngrok.app")
	return &fakeForwarder{url: u}, nil
}

// fakeFactory scripts a sequence of fakeAgents, one per connect attempt —
// index 0 for the first attempt, index 1 for the first reconnect, and so
// on. The last agent in the list is reused for any further attempts past
// the end of the list, so a test can just describe "the first N attempts
// behave like this, then it recovers" without knowing exactly how many
// retries will happen.
type fakeFactory struct {
	mu     sync.Mutex
	agents []*fakeAgent
	calls  int
}

func (f *fakeFactory) factory(_ ...ngrok.AgentOption) (ngrok.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	if idx >= len(f.agents) {
		idx = len(f.agents) - 1
	}
	f.calls++
	return f.agents[idx], nil
}

func (f *fakeFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRPCRequest struct{ method string }

func (r fakeRPCRequest) Method() string { return r.method }

func testConfig(t *testing.T, endpoints ...config.Endpoint) config.Config {
	t.Helper()
	cfg := config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Credential:    config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "fake-token"}},
		Endpoints:     endpoints,
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("test config failed to validate: %v", err)
	}
	return cfg
}

func waitForState(t *testing.T, m *Manager, want State, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last Status
	for time.Now().Before(deadline) {
		last = m.StatusSnapshot()
		if last.State == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %v, last status: %+v", want, last)
	return last
}

func TestManagerConnectSuccessReachesConnected(t *testing.T) {
	ff := &fakeFactory{agents: []*fakeAgent{{}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}, Name: "pos-1"})
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := waitForState(t, m, StateConnected, 2*time.Second)
	if len(status.Endpoints) != 1 || status.Endpoints[0].Name != "pos-1" || status.Endpoints[0].URL != "https://fake.ngrok.app" {
		t.Errorf("unexpected endpoint status: %+v", status.Endpoints)
	}
	if status.LastConnectAt.IsZero() {
		t.Error("LastConnectAt should be set after a successful connect")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForState(t, m, StateStopped, time.Second)
}

func TestManagerConnectFailureRetries(t *testing.T) {
	// Every attempt fails to connect — Manager should keep retrying
	// (not give up) and report StateReconnecting with the error.
	ff := &fakeFactory{agents: []*fakeAgent{{connectErr: errors.New("boom")}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Start returns once the ~30s startup budget elapses or firstConnect
	// fires — neither will happen here quickly, so drive it with a short
	// ctx instead of waiting out the real 30s budget.
	startCtx, startCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer startCancel()
	_ = m.Start(startCtx) // expected to return ctx.Err(); the retry loop keeps running regardless

	status := waitForState(t, m, StateReconnecting, 3*time.Second)
	if status.LastError == nil {
		t.Error("expected LastError to be set after a failed connect")
	}

	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForState(t, m, StateStopped, time.Second)

	if calls := ff.callCount(); calls < 1 {
		t.Errorf("expected at least one connect attempt, got %d", calls)
	}
}

func TestManagerRPCStopRequested(t *testing.T) {
	ff := &fakeFactory{agents: []*fakeAgent{{}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	select {
	case <-m.StopRequested():
		t.Fatal("StopRequested should not be closed before any StopAgentMethod RPC")
	default:
	}

	if _, err := m.handleRPC(context.Background(), nil, fakeRPCRequest{method: rpc.StopAgentMethod}); err != nil {
		t.Fatalf("handleRPC: %v", err)
	}

	select {
	case <-m.StopRequested():
	case <-time.After(time.Second):
		t.Fatal("StopRequested should be closed immediately after a StopAgentMethod RPC")
	}

	// A second StopAgentMethod must not panic (sync.Once guards the close).
	if _, err := m.handleRPC(context.Background(), nil, fakeRPCRequest{method: rpc.StopAgentMethod}); err != nil {
		t.Fatalf("second handleRPC: %v", err)
	}
}

func TestManagerRPCRestartTriggersReconnect(t *testing.T) {
	ff := &fakeFactory{agents: []*fakeAgent{{}, {}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, m, StateConnected, 2*time.Second)

	if calls := ff.callCount(); calls != 1 {
		t.Fatalf("expected exactly 1 connect attempt before restart, got %d", calls)
	}

	if _, err := m.handleRPC(context.Background(), nil, fakeRPCRequest{method: rpc.RestartAgentMethod}); err != nil {
		t.Fatalf("handleRPC restart: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ff.callCount() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if calls := ff.callCount(); calls < 2 {
		t.Fatalf("expected a second connect attempt after RestartAgentMethod, got %d", calls)
	}
	waitForState(t, m, StateConnected, 2*time.Second)

	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestManagerStopBeforeStartIsNoop(t *testing.T) {
	ff := &fakeFactory{agents: []*fakeAgent{{}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start should be a no-op, got: %v", err)
	}
}

// withGOOS temporarily overrides the goos var (see rpc.go) so a test can
// simulate running on Windows regardless of the platform actually running
// the test suite — the self-update mechanism is Windows-only, so this is
// the only way to exercise handleUpdate's real branch in CI on other OSes.
func withGOOS(t *testing.T, value string) {
	t.Helper()
	original := goos
	goos = value
	t.Cleanup(func() { goos = original })
}

func TestManagerRPCUpdateIgnoredWhenNotConfigured(t *testing.T) {
	withGOOS(t, "windows")
	ff := &fakeFactory{agents: []*fakeAgent{{}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	// cfg.UpdateSourceURL deliberately left empty.
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	rec := &updateRecorder{}
	m.updater = update.New(nil, rec.verify, rec.launch)

	if _, err := m.handleRPC(context.Background(), nil, fakeRPCRequest{method: rpc.UpdateAgentMethod}); err != nil {
		t.Fatalf("handleRPC: %v", err)
	}

	// handleUpdate's own early return happens synchronously, before any
	// goroutine is spawned, so there's nothing to race against here.
	if rec.callCount() != 0 {
		t.Errorf("expected no verify/launch calls with update_source_url unset, got %d", rec.callCount())
	}
}

func TestManagerRPCUpdateIgnoredOnNonWindows(t *testing.T) {
	withGOOS(t, "linux")
	ff := &fakeFactory{agents: []*fakeAgent{{}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	cfg.UpdateSourceURL = "http://example.invalid/installer.msi"
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	rec := &updateRecorder{}
	m.updater = update.New(nil, rec.verify, rec.launch)

	if _, err := m.handleRPC(context.Background(), nil, fakeRPCRequest{method: rpc.UpdateAgentMethod}); err != nil {
		t.Fatalf("handleRPC: %v", err)
	}

	if rec.callCount() != 0 {
		t.Errorf("expected no verify/launch calls on a non-Windows platform, got %d", rec.callCount())
	}
}

func TestManagerRPCUpdateAppliesOnWindows(t *testing.T) {
	withGOOS(t, "windows")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake installer bytes"))
	}))
	defer srv.Close()

	ff := &fakeFactory{agents: []*fakeAgent{{}}}
	cfg := testConfig(t, config.Endpoint{Upstream: config.Upstream{URL: "localhost:8080"}})
	cfg.UpdateSourceURL = srv.URL
	m, err := NewManager(cfg, credentials.StaticProvider{}, ff.factory, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	rec := &updateRecorder{}
	m.updater = update.New(nil, rec.verify, rec.launch)

	if _, err := m.handleRPC(context.Background(), nil, fakeRPCRequest{method: rpc.UpdateAgentMethod}); err != nil {
		t.Fatalf("handleRPC: %v", err)
	}

	// Apply runs in its own goroutine (see handleUpdate) — poll for the
	// launch call rather than assuming it's already happened by the time
	// handleRPC returns.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.callCount() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if rec.launched() != 1 {
		t.Fatalf("expected launch to be called exactly once, got %d", rec.launched())
	}
}

// updateRecorder is manager_test.go's own minimal stand-in for
// internal/update's recorder (unexported there, so not reusable directly
// across packages) — just enough to confirm whether/how many times
// verify/launch were invoked by code reached through Manager's RPC wiring.
type updateRecorder struct {
	mu           sync.Mutex
	verifyCalls  int
	launchCallsN int
}

func (r *updateRecorder) verify(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verifyCalls++
	return nil
}

func (r *updateRecorder) launch(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launchCallsN++
	return nil
}

func (r *updateRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verifyCalls + r.launchCallsN
}

func (r *updateRecorder) launched() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.launchCallsN
}
