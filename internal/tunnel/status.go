package tunnel

import "time"

type State int

const (
	StateConnecting   State = iota // run() is attempting a connection (initial or after backoff)
	StateConnected                 // Agent connected, all endpoints forwarding
	StateReconnecting              // a prior attempt ended; backing off before the next one
	StateStopped                   // Stop() was called; run() has exited
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

type EndpointStatus struct {
	Name string // Endpoint.Name, or the positional fallback — see §4.1
	URL  string // from ngrok.Endpoint.URL(), populated once Forward succeeds
}

type Status struct {
	State         State
	LastError     error     // set on transition into StateReconnecting; cleared on StateConnected
	LastConnectAt time.Time // zero if never connected
	Endpoints     []EndpointStatus
}
