package tunnel

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.ngrok.com/ngrok/v2"

	"fleet-connector/internal/config"
	"fleet-connector/internal/credentials"
	"fleet-connector/internal/update"
)

type Manager struct {
	cfg     config.Config
	creds   credentials.Provider
	factory AgentFactory
	log     *slog.Logger

	connectCAs         *x509.CertPool  // parsed from cfg.ConnectCACertFile once in NewManager, not re-read per retry
	heartbeatInterval  time.Duration   // parsed from cfg.HeartbeatInterval by NewManager
	heartbeatTolerance time.Duration   // parsed from cfg.HeartbeatTolerance by NewManager
	updater            *update.Applier // handles UpdateAgentMethod RPCs, see rpc.go

	mu               sync.RWMutex
	status           Status
	cancel           context.CancelFunc
	done             chan struct{} // closed when run() returns
	firstConnect     chan struct{} // closed once, the first time StateConnected is reached
	firstConnectOnce sync.Once
	backoff          *backoff // shared with connectAndServe so a successful connect can reset it

	restart          chan struct{} // handleRPC sends here on RestartAgentMethod; connectAndServe observes it to disconnect and let run()'s loop reconnect
	stopRequested    chan struct{} // closed once on StopAgentMethod; see StopRequested()
	stopRequestedOne sync.Once
}

// NewManager assumes cfg has already passed config.Validate — it re-parses
// the same duration/PEM fields Validate checked, since Validate only
// verifies they parse, it doesn't hand back the parsed values.
func NewManager(cfg config.Config, creds credentials.Provider, factory AgentFactory, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	connectCAs, heartbeatInterval, heartbeatTolerance, err := parseAgentConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Manager{
		cfg: cfg, creds: creds, factory: factory, log: log,
		connectCAs: connectCAs, heartbeatInterval: heartbeatInterval, heartbeatTolerance: heartbeatTolerance,
		updater:       update.New(log, update.DefaultVerify(cfg.UpdateSignerThumbprint), update.DefaultLaunch),
		restart:       make(chan struct{}, 1),
		stopRequested: make(chan struct{}),
	}, nil
}

// parseAgentConfig re-parses the duration/PEM fields config.Validate
// already checked (Validate only verifies they parse, it doesn't hand
// back the parsed values) — shared between NewManager's persistent
// connection loop and TestConnect's one-shot probe, so both build an
// Agent the same way.
func parseAgentConfig(cfg config.Config) (connectCAs *x509.CertPool, heartbeatInterval, heartbeatTolerance time.Duration, err error) {
	if cfg.ConnectCACertFile != "" {
		pem, err := os.ReadFile(cfg.ConnectCACertFile)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("connect_ca_cert_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, 0, 0, fmt.Errorf("connect_ca_cert_file: no valid PEM certificates found in %q", cfg.ConnectCACertFile)
		}
		connectCAs = pool
	}
	if cfg.HeartbeatInterval != "" {
		d, err := time.ParseDuration(cfg.HeartbeatInterval)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("heartbeat_interval: %w", err)
		}
		heartbeatInterval = d
	}
	if cfg.HeartbeatTolerance != "" {
		d, err := time.ParseDuration(cfg.HeartbeatTolerance)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("heartbeat_tolerance: %w", err)
		}
		heartbeatTolerance = d
	}
	return connectCAs, heartbeatInterval, heartbeatTolerance, nil
}

// buildAgentOptions assembles the AgentOptions shared between Manager's
// own persistent connection loop (connectAndServe) and TestConnect's
// one-shot validation probe — eventHandler/rpcHandler are nil for
// TestConnect, which has no ongoing session to react to events on or
// accept dashboard RPCs for.
func buildAgentOptions(cfg config.Config, authToken string, connectCAs *x509.CertPool, heartbeatInterval, heartbeatTolerance time.Duration, log *slog.Logger, eventHandler ngrok.EventHandler, rpcHandler ngrok.RPCHandler) []ngrok.AgentOption {
	agentOpts := []ngrok.AgentOption{
		ngrok.WithAuthtoken(authToken),
		ngrok.WithLogger(log), // surfaces the SDK's own internal connection/protocol logs into the same sink as this app's own logs
	}
	if eventHandler != nil {
		agentOpts = append(agentOpts, ngrok.WithEventHandler(eventHandler))
	}
	if rpcHandler != nil {
		agentOpts = append(agentOpts, ngrok.WithRPCHandler(rpcHandler))
	}
	if cfg.Description != "" {
		agentOpts = append(agentOpts, ngrok.WithAgentDescription(cfg.Description))
	}
	if cfg.Metadata != "" {
		agentOpts = append(agentOpts, ngrok.WithAgentMetadata(cfg.Metadata))
	}
	if cfg.ConnectURL != "" {
		agentOpts = append(agentOpts, ngrok.WithAgentConnectURL(cfg.ConnectURL))
	}
	if cfg.ProxyURL != "" {
		agentOpts = append(agentOpts, ngrok.WithProxyURL(cfg.ProxyURL))
	}
	if connectCAs != nil {
		agentOpts = append(agentOpts, ngrok.WithAgentConnectCAs(connectCAs))
	}
	if heartbeatInterval != 0 {
		agentOpts = append(agentOpts, ngrok.WithHeartbeatInterval(heartbeatInterval))
	}
	if heartbeatTolerance != 0 {
		agentOpts = append(agentOpts, ngrok.WithHeartbeatTolerance(heartbeatTolerance))
	}
	return agentOpts
}

// TestConnect is a one-shot validation probe: resolve the credential,
// connect, forward every endpoint, then immediately disconnect — used by
// the wizard (see internal/wizard's handleConfirm) to catch a bad
// authtoken or a broken endpoint (e.g. "already online") before writing
// config.yaml at all, rather than writing it, exiting, and only finding
// out it doesn't actually work once the real service starts. Deliberately
// not a *Manager method: it has no retry loop, no status tracking, and no
// RPC/event handling — it either connects cleanly or it doesn't, once.
func TestConnect(ctx context.Context, cfg config.Config, creds credentials.Provider, factory AgentFactory, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	connectCAs, heartbeatInterval, heartbeatTolerance, err := parseAgentConfig(cfg)
	if err != nil {
		return err
	}
	cred, err := creds.Resolve(ctx, cfg.Credential)
	if err != nil {
		return fmt.Errorf("resolve credential: %w", err)
	}
	agentOpts := buildAgentOptions(cfg, cred.AuthToken, connectCAs, heartbeatInterval, heartbeatTolerance, log, nil, nil)

	a, err := factory(agentOpts...)
	if err != nil {
		return err
	}
	if err := a.Connect(ctx); err != nil {
		return err
	}
	defer a.Disconnect()

	for i, ep := range cfg.Endpoints {
		label := ep.Name
		if label == "" {
			label = fmt.Sprintf("endpoint-%d", i)
		}
		upstream, opts, err := Build(ep)
		if err != nil {
			return fmt.Errorf("endpoint %q: %w", label, err)
		}
		if _, err := a.Forward(ctx, upstream, opts...); err != nil {
			return fmt.Errorf("endpoint %q: forward: %w", label, err)
		}
	}
	return nil
}

// Start launches the supervisory loop in the background and returns once
// either the first connect attempt succeeds or a bounded startup budget
// (~30s) elapses — so a Windows SCM start callback (or systemd's own start
// timeout) isn't blocked forever. The loop keeps retrying regardless of
// what Start returns; a slow/unreachable network at boot is not a startup
// failure, just a status the caller can observe via StatusSnapshot.
func (m *Manager) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(context.Background())
	m.cancel, m.done, m.firstConnect = cancel, make(chan struct{}), make(chan struct{})
	go m.run(runCtx)

	select {
	case <-m.firstConnect:
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// Stop is idempotent and respects ctx's deadline (the SCM/systemd
// stop-control budget is finite — see §8, §9).
func (m *Manager) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	select {
	case <-m.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (m *Manager) StatusSnapshot() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// StopRequested reports ngrok-dashboard-initiated stop commands
// (RPCHandler's StopAgentMethod, see rpc.go). Manager itself always keeps
// retrying on its own — it has no notion of a permanent stop other than
// the caller's own Stop(ctx) — so it's up to whatever owns this Manager
// (the service wrapper on each platform) to observe this channel and
// decide to actually halt the service.
func (m *Manager) StopRequested() <-chan struct{} {
	return m.stopRequested
}

func (m *Manager) setStatus(mutate func(s *Status)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mutate(&m.status)
}

// run is the supervisory loop: retry "resolve credential -> build Agent ->
// Connect -> Forward every endpoint -> wait for disconnect" as a unit,
// forever, with backoff between attempts. It never returns on its own
// except when runCtx is canceled by Stop().
func (m *Manager) run(ctx context.Context) {
	defer close(m.done)
	m.backoff = newBackoff(1*time.Second, 5*time.Minute)
	for {
		select {
		case <-ctx.Done():
			m.setStatus(func(s *Status) { s.State = StateStopped })
			m.log.Info("stopped")
			return
		default:
		}
		m.setStatus(func(s *Status) { s.State = StateConnecting })
		err := m.connectAndServe(ctx)
		if ctx.Err() != nil {
			m.setStatus(func(s *Status) { s.State = StateStopped })
			m.log.Info("stopped")
			return // Stop() was called; connectAndServe returned because ctx.Done() fired
		}
		m.setStatus(func(s *Status) { s.State = StateReconnecting; s.LastError = err })
		wait := m.backoff.Next(classify(err))
		m.log.Warn("disconnected, backing off before reconnect", "error", err, "wait", wait)
		select {
		case <-ctx.Done():
			m.setStatus(func(s *Status) { s.State = StateStopped })
			m.log.Info("stopped")
			return
		case <-time.After(wait):
		}
	}
}

// Because ngrok.WithAuthtoken is only applied at ngrok.NewAgent(...)
// construction time (not on Connect), a credential rotation means building
// a fresh Agent on the next retry, not reconnecting an existing one.
func (m *Manager) connectAndServe(ctx context.Context) error {
	cred, err := m.creds.Resolve(ctx, m.cfg.Credential)
	if err != nil {
		return fmt.Errorf("resolve credential: %w", err)
	}

	disconnected := make(chan error, 1) // fed by the event handler below
	eventHandler := func(evt ngrok.Event) {
		if d, ok := evt.(*ngrok.EventAgentDisconnected); ok {
			select {
			case disconnected <- d.Error:
			default:
			}
		}
	}
	agentOpts := buildAgentOptions(m.cfg, cred.AuthToken, m.connectCAs, m.heartbeatInterval, m.heartbeatTolerance, m.log, eventHandler, m.handleRPC)

	a, err := m.factory(agentOpts...)
	if err != nil {
		return err
	}

	if err := a.Connect(ctx); err != nil {
		return err
	}
	defer a.Disconnect()

	m.setStatus(func(s *Status) { s.Endpoints = nil })
	for i, ep := range m.cfg.Endpoints { // zero endpoints is valid — loop just does nothing
		label := ep.Name
		if label == "" { // Name is optional; fall back to a positional key for internal tracking only
			label = fmt.Sprintf("endpoint-%d", i)
		}
		upstream, opts, err := Build(ep)
		if err != nil {
			return fmt.Errorf("endpoint %q: %w", label, err)
		}
		fwd, err := a.Forward(ctx, upstream, opts...)
		if err != nil {
			return fmt.Errorf("endpoint %q: forward: %w", label, err)
		}
		m.setStatus(func(s *Status) {
			s.Endpoints = append(s.Endpoints, EndpointStatus{Name: label, URL: fwd.URL().String()})
		})
		m.log.Info("endpoint forwarding", "name", label, "url", fwd.URL().String())
	}

	m.backoff.Reset() // a real connection was established; forget any accumulated backoff
	m.setStatus(func(s *Status) {
		s.State, s.LastError, s.LastConnectAt = StateConnected, nil, time.Now()
	})
	m.log.Info("connected")
	m.firstConnectOnce.Do(func() { close(m.firstConnect) }) // unblocks Start(), a no-op on every later reconnect

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-disconnected:
		return err // triggers backoff + rebuild on the next loop iteration
	case <-m.restart:
		return nil // dashboard-initiated restart: reconnect on the next loop iteration, same as any other disconnect
	}
}
