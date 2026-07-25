package tunnel

import "golang.ngrok.com/ngrok/v2"

// AgentFactory wraps ngrok.NewAgent as a test seam: Manager calls
// m.factory(...) rather than ngrok.NewAgent directly, so tests can
// substitute a fake ngrok.Agent with scripted behavior.
type AgentFactory func(opts ...ngrok.AgentOption) (ngrok.Agent, error)

// DefaultFactory is the real SDK constructor.
var DefaultFactory AgentFactory = ngrok.NewAgent
