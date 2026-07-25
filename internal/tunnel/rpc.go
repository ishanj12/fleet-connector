package tunnel

import (
	"context"

	"golang.ngrok.com/ngrok/v2"
	"golang.ngrok.com/ngrok/v2/rpc"
)

// handleRPC is the free fleet-management leverage behind goal 2 in §1:
// ngrok-dashboard-initiated stop/restart. RestartAgentMethod disconnects
// the current Agent and lets Manager's own supervisory loop reconnect it,
// same as any other disconnect. StopAgentMethod does NOT call m.cancel()
// directly — Manager has no built-in notion of a permanent stop beyond the
// caller's own Stop(ctx), and canceling the whole run() loop here would
// leave nothing to ever call Start() again. Instead it signals
// StopRequested(), which the platform service wrapper (§8, §9) observes to
// decide whether/how to actually halt the service. UpdateAgentMethod has no
// meaningful action here — this binary isn't self-updating (see §12) — so
// it's acknowledged as a no-op rather than left unhandled.
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
		// no-op: updates are re-pushed packages (§12), not an in-app action
	}
	return nil, nil
}
