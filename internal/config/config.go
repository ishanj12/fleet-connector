package config

// CurrentSchemaVersion is the only schema_version Validate accepts today.
const CurrentSchemaVersion = 1

// CredentialRef is provider-agnostic on purpose — swapping credential
// providers never changes this schema.
type CredentialRef struct {
	Provider string            `yaml:"provider"` // "static" default
	Params   map[string]string `yaml:"params"`
}

// Endpoint is a slice element supporting >1 tunnel per install without
// schema changes. The endpoint-facing protocol is carried entirely by
// URL's scheme (matches the SDK directly: v2 infers it from the URL rather
// than from separate HTTPEndpoint()/TCPEndpoint()/TLSEndpoint()
// constructors), so there's a single source of truth for it. Upstream is a
// nested object (not a flat string) to match the real ngrok agent's own
// v3 config schema (https://ngrok.com/docs/agent/config/v3/#upstream).
// Field order (and yaml tag order) deliberately matches the real agent's
// own documented field order — name, url, upstream, bindings, metadata,
// description, traffic_policy, agent_tls_termination — so a generated
// config.yaml reads in the order anyone already familiar with a real
// ngrok config would expect. PoolingEnabled has no analogue there, so it's
// appended after.
type Endpoint struct {
	Name                string               `yaml:"name,omitempty"`
	URL                 string               `yaml:"url,omitempty"`
	Upstream            Upstream             `yaml:"upstream"`
	Bindings            []string             `yaml:"bindings,omitempty"`
	Metadata            string               `yaml:"metadata,omitempty"`
	Description         string               `yaml:"description,omitempty"`
	TrafficPolicy       string               `yaml:"traffic_policy,omitempty"`
	AgentTLSTermination *AgentTLSTermination `yaml:"agent_tls_termination,omitempty"`
	PoolingEnabled      bool                 `yaml:"pooling_enabled,omitempty"`
}

// Upstream mirrors the real agent's own upstream object field names
// (https://ngrok.com/docs/agent/config/v3/#upstream) for URL/Protocol/
// ProxyProtocol. TLSVerify/TLSVerifyCAs back
// ngrok.WithUpstreamTLSClientConfig — a real, SDK-verified capability
// (also exposed as --upstream-tls-verify/--upstream-tls-verify-cas CLI
// flags and other SDKs' verify_upstream_tls) — but its exact v3 YAML key
// wasn't independently confirmed, so these names were chosen to match the
// sibling fields' no-redundant-prefix convention rather than copied from a
// verified doc.
type Upstream struct {
	URL           string `yaml:"url"`
	Protocol      string `yaml:"protocol,omitempty"`       // "http1" (default) or "http2"
	ProxyProtocol string `yaml:"proxy_protocol,omitempty"` // "1", "2", or empty
	TLSVerify     bool   `yaml:"tls_verify,omitempty"`     // ngrok's own default is to not verify the upstream's TLS certificate
	TLSVerifyCAs  string `yaml:"tls_verify_cas,omitempty"` // PEM CA bundle path; empty means the system CA pool
}

// AgentTLSTermination mirrors the real ngrok agent's own config field
// names for this feature (https://ngrok.com/docs/agent/agent-tls-termination/,
// https://ngrok.com/docs/agent/config/v3/#agent_tls_termination) rather
// than inventing new ones, so admins already familiar with ngrok's own
// config format recognize it immediately.
type AgentTLSTermination struct {
	ServerCertificate               string `yaml:"server_certificate"`
	ServerPrivateKey                string `yaml:"server_private_key"`
	MutualTLSCertificateAuthorities string `yaml:"mutual_tls_certificate_authorities,omitempty"`
}

// Config is the one artifact everything else produces or consumes.
type Config struct {
	SchemaVersion      int           `yaml:"schema_version"`
	Description        string        `yaml:"description,omitempty"`
	Metadata           string        `yaml:"metadata,omitempty"` // opaque, dashboard/API-visible session metadata; distinct from Description's human-readable text
	Credential         CredentialRef `yaml:"credential"`
	Endpoints          []Endpoint    `yaml:"endpoints,omitempty"`
	ConnectURL         string        `yaml:"connect_url,omitempty"`
	ConnectCACertFile  string        `yaml:"connect_ca_cert_file,omitempty"`
	ProxyURL           string        `yaml:"proxy_url,omitempty"`
	HeartbeatInterval  string        `yaml:"heartbeat_interval,omitempty"`
	HeartbeatTolerance string        `yaml:"heartbeat_tolerance,omitempty"`
	LogLevel           string        `yaml:"log_level,omitempty"` // "debug", "info" (default), "warn", or "error" — governs both this app's own logs and whatever the SDK emits via ngrok.WithLogger

	// UpdateSourceURL, if set, is where the RPC-triggered self-update flow
	// (see internal/update) downloads a new installer from when ngrok's
	// dashboard/API sends this agent an UpdateAgentMethod command. Empty
	// (the default) disables the feature entirely — UpdateAgentMethod is
	// logged and otherwise ignored, never silently attempted. This
	// reference implementation has no distribution point of its own; an
	// adopting customer wires this to wherever they host their own signed
	// build. The download target may end up being a plain, unauthenticated
	// URL in practice (an unattended process can't complete an interactive
	// SSO/MFA login the way a human fetching the same file could) — the
	// signature check in internal/update.Apply, not who can reach this URL,
	// is what actually has to be trusted. Windows-only today — see
	// internal/update's platform files.
	UpdateSourceURL string `yaml:"update_source_url,omitempty"`

	// UpdateSignerThumbprint is the SHA-1 (or SHA-256) thumbprint of the
	// code-signing certificate that must have signed whatever
	// UpdateSourceURL serves, as reported by PowerShell's
	// Get-AuthenticodeSignature: `(Get-AuthenticodeSignature path).
	// SignerCertificate.Thumbprint`. Required whenever UpdateSourceURL is
	// set (see Validate) — checking only that *some* certificate signed
	// the download isn't a meaningful safety gate on its own, since anyone
	// with their own legitimately-issued signing certificate would also
	// pass that check. Pinning to this specific thumbprint is what
	// actually makes UpdateSourceURL safe to point at a plain,
	// unauthenticated URL (see that field's own doc comment).
	UpdateSignerThumbprint string `yaml:"update_signer_thumbprint,omitempty"`
}
