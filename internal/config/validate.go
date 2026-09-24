package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

var validURLSchemes = map[string]bool{
	"":      true,
	"http":  true,
	"https": true,
	"tcp":   true,
	"tls":   true,
}

var validProxyProtocols = map[string]bool{
	"":  true,
	"1": true,
	"2": true,
}

// The levels slog itself has — not a port of the real ngrok agent's own
// log_level enum, which also has "crit" (slog has no equivalent).
var validLogLevels = map[string]bool{
	"":      true,
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// Validate checks: SchemaVersion == CurrentSchemaVersion (the zero value
// from an unset field is rejected, same as any other unrecognized version);
// zero or more endpoints permitted (matches the SDK — a session with no
// active tunnels is valid); any non-blank endpoint names must be unique
// among themselves; Upstream.URL required, Upstream.ProxyProtocol (if set)
// is "1" or "2"; URL's scheme (if set) is one of http/https/tcp/tls; if
// AgentTLSTermination is set, both ServerCertificate and ServerPrivateKey
// are required (a structural crypto/tls constraint, not a server-side
// business rule — see mapping.go), and MutualTLSCertificateAuthorities (if
// set) exists and parses as PEM; Upstream.TLSVerifyCAs (if set) exists and
// parses as PEM; ProxyURL (if set) parses as a URL;
// HeartbeatInterval/HeartbeatTolerance (if set) parse via
// time.ParseDuration; ConnectCACertFile (if set) exists and parses as PEM;
// Credential.Provider set; LogLevel (if set) is debug/info/warn/error;
// UpdateSourceURL (if set) parses as a URL and requires
// UpdateSignerThumbprint to also be set; UpdateSignerThumbprint (if set)
// is a 40- or 64-character hex string.
func Validate(cfg Config) error {
	if cfg.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (expected %d)", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if cfg.Credential.Provider == "" {
		return errors.New("credential.provider is required")
	}
	if !validLogLevels[strings.ToLower(cfg.LogLevel)] {
		return fmt.Errorf("log_level must be debug, info, warn, error, or empty, got %q", cfg.LogLevel)
	}

	seenNames := make(map[string]bool, len(cfg.Endpoints))
	for i, ep := range cfg.Endpoints {
		if ep.Upstream.URL == "" {
			return fmt.Errorf("endpoint %d: upstream.url is required", i)
		}
		if !validProxyProtocols[ep.Upstream.ProxyProtocol] {
			return fmt.Errorf("endpoint %d: upstream.proxy_protocol must be \"1\", \"2\", or empty, got %q", i, ep.Upstream.ProxyProtocol)
		}
		if ep.Upstream.TLSVerifyCAs != "" {
			if err := validatePEMFile("upstream.tls_verify_cas", ep.Upstream.TLSVerifyCAs); err != nil {
				return fmt.Errorf("endpoint %d: %w", i, err)
			}
		}
		if ep.Name != "" {
			if seenNames[ep.Name] {
				return fmt.Errorf("endpoint %d: duplicate name %q", i, ep.Name)
			}
			seenNames[ep.Name] = true
		}
		if ep.URL != "" {
			u, err := url.Parse(ep.URL)
			if err != nil {
				return fmt.Errorf("endpoint %d: invalid url: %w", i, err)
			}
			if !validURLSchemes[strings.ToLower(u.Scheme)] {
				return fmt.Errorf("endpoint %d: unsupported url scheme %q", i, u.Scheme)
			}
		}
		if t := ep.AgentTLSTermination; t != nil {
			if t.ServerCertificate == "" || t.ServerPrivateKey == "" {
				return fmt.Errorf("endpoint %d: agent_tls_termination requires both server_certificate and server_private_key", i)
			}
			if t.MutualTLSCertificateAuthorities != "" {
				if err := validatePEMFile("agent_tls_termination.mutual_tls_certificate_authorities", t.MutualTLSCertificateAuthorities); err != nil {
					return fmt.Errorf("endpoint %d: %w", i, err)
				}
			}
		}
	}

	if cfg.ProxyURL != "" {
		if _, err := url.Parse(cfg.ProxyURL); err != nil {
			return fmt.Errorf("proxy_url: %w", err)
		}
	}
	if cfg.HeartbeatInterval != "" {
		if _, err := time.ParseDuration(cfg.HeartbeatInterval); err != nil {
			return fmt.Errorf("heartbeat_interval: %w", err)
		}
	}
	if cfg.HeartbeatTolerance != "" {
		if _, err := time.ParseDuration(cfg.HeartbeatTolerance); err != nil {
			return fmt.Errorf("heartbeat_tolerance: %w", err)
		}
	}
	if cfg.ConnectCACertFile != "" {
		if err := validatePEMFile("connect_ca_cert_file", cfg.ConnectCACertFile); err != nil {
			return err
		}
	}
	if cfg.UpdateSourceURL != "" {
		if _, err := url.Parse(cfg.UpdateSourceURL); err != nil {
			return fmt.Errorf("update_source_url: %w", err)
		}
		// Required, not optional, once update_source_url is set — checking
		// only that *some* certificate signed the download isn't a
		// meaningful safety gate (see update_signer_thumbprint's own doc
		// comment), so the feature refuses to even validate as "on"
		// without it, rather than silently falling back to a weaker check.
		if cfg.UpdateSignerThumbprint == "" {
			return errors.New("update_signer_thumbprint is required when update_source_url is set")
		}
	}
	if cfg.UpdateSignerThumbprint != "" {
		if !isHexThumbprint(cfg.UpdateSignerThumbprint) {
			return fmt.Errorf("update_signer_thumbprint must be a 40-character (SHA-1) or 64-character (SHA-256) hex string, got %q", cfg.UpdateSignerThumbprint)
		}
	}

	return nil
}

func isHexThumbprint(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func validatePEMFile(field, path string) error {
	pem, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(pem) {
		return fmt.Errorf("%s: no valid PEM certificates found in %q", field, path)
	}
	return nil
}
