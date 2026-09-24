package tunnel

import (
	"context"
	"runtime"

	"golang.ngrok.com/ngrok/v2"
	"golang.ngrok.com/ngrok/v2/rpc"
)

// handleRPC is the free fleet-management leverage behind goal 2 in §1:
// ngrok-dashboard-initiated stop/restart/update. RestartAgentMethod
// disconnects the current Agent and lets Manager's own supervisory loop
// reconnect it, same as any other disconnect. StopAgentMethod does NOT call
// m.cancel() directly — Manager has no built-in notion of a permanent stop
// beyond the caller's own Stop(ctx), and canceling the whole run() loop here
// would leave nothing to ever call Start() again. Instead it signals
// StopRequested(), which the platform service wrapper (§8, §9) observes to
// decide whether/how to actually halt the service.
//
// UpdateAgentMethod, if cfg.UpdateSourceURL is configured, downloads,
// verifies, and launches a new installer via internal/update — see that
// package's doc comment for why the signature check there is
// non-negotiable. The self-update mechanism assumes an MSI, so this checks
// runtime.GOOS itself before ever attempting a download that could never be
// launched on a non-Windows box. Confirmed live (see §12) that the ngrok
// API's tunnel session update operation delivers this method to a custom
// RPCHandler, not just to the stock ngrok agent binary — the RPC itself
// arrives regardless of platform, only the handling here is Windows-only.
func (m *Manager) handleRPC(_ context.Context, _ ngrok.AgentSession, req rpc.Request) ([]byte, error) {
	switch req.Method() {
	case rpc.StopAgentMethod:
		m.log.Info("stop requested via ngrok dashboard")
		m.stopRequestedOne.Do(func() { close(m.stopRequested) })
	case rpc.RestartAgentMethod:
		m.log.Info("restart requested via ngrok dashboard")
		select {
		case m.restart <- struct{}{}:
		default:
		}
	case rpc.UpdateAgentMethod:
		m.handleUpdate()
	}
	return nil, nil
}

// goos exists as a var (rather than reading runtime.GOOS directly in
// handleUpdate) purely so a test can simulate "this is Windows" without
// actually running on Windows — the real self-update mechanism is only
// meaningfully testable there otherwise.
var goos = runtime.GOOS

// handleUpdate is split out from handleRPC's switch so its own early-return
// branches (not configured, wrong platform) stay readable rather than
// nested inside the switch statement.
func (m *Manager) handleUpdate() {
	if m.cfg.UpdateSourceURL == "" {
		m.log.Info("update requested via ngrok dashboard (ignored: update_source_url not configured)")
		return
	}
	if goos != "windows" {
		m.log.Info("update requested via ngrok dashboard (ignored: self-update only supported on Windows)", "os", goos)
		return
	}

	m.log.Info("update requested via ngrok dashboard, applying", "source", m.cfg.UpdateSourceURL)
	// Apply downloads/launches an installer that's about to stop this very
	// process's service — run it in its own goroutine so a slow or stalled
	// attempt can't block handleRPC (and, by extension, the SDK's own
	// session-handling loop that calls it).
	go func() {
		if err := m.updater.Apply(context.Background(), m.cfg.UpdateSourceURL); err != nil {
			m.log.Error("self-update failed", "error", err)
		}
	}()
}
