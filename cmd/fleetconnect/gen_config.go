package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"fleet-connector/internal/config"
	"fleet-connector/internal/config/local"
	"fleet-connector/internal/provisioning"
)

// repeatedFlag collects every occurrence of a flag passed more than once,
// e.g. --endpoint a --endpoint b --endpoint c.
type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, "; ") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

func runGenConfig(args []string) error {
	fs := flag.NewFlagSet("gen-config", flag.ContinueOnError)
	path := fs.String("path", "", "output path (default: config.yaml)")
	description := fs.String("description", "", "human-readable description, e.g. install:store-042 (required; also used as the minted credential's description, if minting)")
	metadata := fs.String("metadata", "", "agent/session-level metadata")
	logLevel := fs.String("log-level", "", "log_level for the installed config: debug, info (default), warn, or error")
	authtokenFlag := fs.String("authtoken", "", `an already-existing authtoken to use directly, one of: a literal value; "env:VARNAME" to read it from that environment variable; "file:PATH" to read it from a pre-staged, ACL'd file (§10's FLEETCONNECT_CREDFILE pattern — usable directly from a WiX deferred custom action's command line, since only the path crosses that boundary, never the secret); or omit entirely to mint a fresh per-install credential via the Credentials API (recommended default, requires FLEETCONNECT_NGROK_API_KEY[_FILE])`)
	connectURL := fs.String("connect-url", "", "custom/dedicated/self-hosted ngrok connect endpoint (blank uses ngrok's public one)")
	connectCACertFile := fs.String("connect-ca-cert-file", "", "PEM CA bundle to trust for --connect-url, if it needs one")
	proxyURL := fs.String("proxy-url", "", "outbound HTTP/SOCKS proxy URL to route the agent's own connection through")
	heartbeatInterval := fs.String("heartbeat-interval", "", "connection heartbeat interval, e.g. 30s (blank uses the SDK's default)")
	heartbeatTolerance := fs.String("heartbeat-tolerance", "", "how long a missed heartbeat is tolerated before the connection is considered disconnected, e.g. 1m")
	updateSourceURL := fs.String("update-source-url", "", "where the RPC-triggered self-update flow downloads a new installer from (Windows only); blank disables the feature. Requires --update-signer-thumbprint")
	updateSignerThumbprint := fs.String("update-signer-thumbprint", "", `the SHA-1 or SHA-256 thumbprint of the certificate that must have signed whatever --update-source-url serves, as reported by PowerShell's (Get-AuthenticodeSignature path).SignerCertificate.Thumbprint. Required whenever --update-source-url is set — this, not who can reach the URL, is the actual safety gate`)

	// Single-endpoint convenience flags — the common case.
	upstream := fs.String("upstream", "", "single-endpoint shorthand: upstream address, e.g. localhost:8080 (mutually exclusive with --endpoint)")
	name := fs.String("name", "", "single-endpoint shorthand: endpoint name")
	url := fs.String("url", "", "single-endpoint shorthand: endpoint URL (blank for https, or tcp://, tls://)")

	// Repeatable multi-endpoint flag — for installs with more than one
	// tunnel. Each occurrence is "key=value,key=value", keys: name,
	// upstream (required), url. Only these three fields are exposed here;
	// anything more elaborate per-endpoint (traffic_policy, bindings,
	// agent_tls_termination) means hand-editing the generated file
	// afterward, or authoring it directly (§6).
	var endpointFlags repeatedFlag
	fs.Var(&endpointFlags, "endpoint", `one endpoint, as "name=...,upstream.url=...,url=..." (upstream.url required, name/url optional) — key names match config.yaml's own nesting. Repeat for multiple endpoints, e.g. --endpoint "name=pos-1,upstream.url=localhost:8080" --endpoint "name=pos-2,upstream.url=localhost:8081". Mutually exclusive with --name/--upstream/--url.`)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *description == "" {
		return errors.New("--description is required")
	}

	endpoints, err := resolveEndpoints(endpointFlags, *name, *upstream, *url)
	if err != nil {
		return err
	}

	authtoken, err := resolveAuthtoken(*description, *authtokenFlag)
	if err != nil {
		return err
	}

	return writeConfig(genConfigOptions{
		path:                   *path,
		description:            *description,
		metadata:               *metadata,
		logLevel:               *logLevel,
		endpoints:              endpoints,
		authtoken:              authtoken,
		connectURL:             *connectURL,
		connectCACertFile:      *connectCACertFile,
		proxyURL:               *proxyURL,
		heartbeatInterval:      *heartbeatInterval,
		heartbeatTolerance:     *heartbeatTolerance,
		updateSourceURL:        *updateSourceURL,
		updateSignerThumbprint: *updateSignerThumbprint,
	})
}

// resolveEndpoints builds the endpoint list from whichever flag style was
// used — the single-endpoint shorthand or one-or-more --endpoint entries —
// and rejects mixing both, since precedence between them would be
// ambiguous rather than a sensible default.
func resolveEndpoints(endpointFlags repeatedFlag, name, upstream, url string) ([]config.Endpoint, error) {
	shorthandUsed := name != "" || upstream != "" || url != ""
	if shorthandUsed && len(endpointFlags) > 0 {
		return nil, errors.New("use either --endpoint or --name/--upstream/--url, not both")
	}

	if len(endpointFlags) > 0 {
		endpoints := make([]config.Endpoint, len(endpointFlags))
		for i, spec := range endpointFlags {
			ep, err := parseEndpointFlag(spec)
			if err != nil {
				return nil, fmt.Errorf("--endpoint %q: %w", spec, err)
			}
			endpoints[i] = ep
		}
		return endpoints, nil
	}

	if upstream == "" {
		return nil, errors.New("--upstream is required (or use one or more --endpoint instead)")
	}
	return []config.Endpoint{{Name: name, Upstream: config.Upstream{URL: upstream}, URL: url}}, nil
}

// parseEndpointFlag parses "name=...,upstream=...,url=..." into an Endpoint.
func parseEndpointFlag(spec string) (config.Endpoint, error) {
	var ep config.Endpoint
	for _, pair := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return config.Endpoint{}, fmt.Errorf("expected key=value, got %q", pair)
		}
		switch key {
		case "name":
			ep.Name = value
		case "upstream.url":
			ep.Upstream.URL = value
		case "url":
			ep.URL = value
		default:
			return config.Endpoint{}, fmt.Errorf("unknown key %q (want name, upstream.url, or url)", key)
		}
	}
	if ep.Upstream.URL == "" {
		return config.Endpoint{}, errors.New("upstream.url is required")
	}
	return ep, nil
}

// resolveAuthtoken prefers an already-existing authtoken supplied directly
// by ops over minting a new one — not every ops setup wants a fresh
// per-install credential; some already have one (dashboard-minted, shared
// across a small deployment, pulled from a CSV of per-site tokens, etc).
// Minting via the Credentials API stays the default/recommended path
// (§5's "never a shared secret" invariant) whenever --authtoken is omitted.
func resolveAuthtoken(description, authtokenFlag string) (string, error) {
	if authtokenFlag != "" {
		if varName, ok := strings.CutPrefix(authtokenFlag, "env:"); ok {
			val := os.Getenv(varName)
			if val == "" {
				return "", fmt.Errorf("--authtoken=env:%s: environment variable not set", varName)
			}
			return val, nil
		}
		// file: — reads a pre-staged, ACL'd credentials file (§10's
		// FLEETCONNECT_CREDFILE pattern). Passing a file *path* as a CLI
		// argument is safe — it's the credential's location, not the
		// credential itself — which is what makes this usable directly in
		// a WiX deferred custom action's interpolated command line (MSI
		// properties reach a launched EXE via command-line substitution,
		// not environment variables — see installer/product.wxs).
		if path, ok := strings.CutPrefix(authtokenFlag, "file:"); ok {
			b, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("--authtoken=file:%s: %w", path, err)
			}
			return strings.TrimSpace(string(b)), nil
		}
		return authtokenFlag, nil
	}

	apiKey, err := resolveAPIKey()
	if err != nil {
		return "", fmt.Errorf("no --authtoken supplied and could not mint one: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	authtoken, _, err := provisioning.MintCredential(ctx, apiKey, provisioning.ProvisionRequest{Description: description})
	if err != nil {
		return "", err
	}
	return authtoken, nil
}

func resolveAPIKey() (string, error) {
	if k := os.Getenv("FLEETCONNECT_NGROK_API_KEY"); k != "" {
		return k, nil
	}
	if p := os.Getenv("FLEETCONNECT_NGROK_API_KEY_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("FLEETCONNECT_NGROK_API_KEY_FILE: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", errors.New("one of FLEETCONNECT_NGROK_API_KEY or FLEETCONNECT_NGROK_API_KEY_FILE must be set")
}

// genConfigOptions is a plain struct rather than more positional string
// params — writeConfig's parameter list was already at the point where
// adding the connection-level fields below as more bare strings would
// make call sites error-prone to read and easy to mis-order.
type genConfigOptions struct {
	path                                    string
	description, metadata, logLevel         string
	endpoints                               []config.Endpoint
	authtoken                               string
	connectURL, connectCACertFile           string
	proxyURL                                string
	heartbeatInterval, heartbeatTolerance   string
	updateSourceURL, updateSignerThumbprint string
}

func writeConfig(opts genConfigOptions) error {
	path := opts.path
	if path == "" {
		path = "config.yaml"
	}
	cfg := config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Description:   opts.description,
		Metadata:      opts.metadata,
		LogLevel:      opts.logLevel,
		Credential: config.CredentialRef{
			Provider: "static",
			Params:   map[string]string{"authtoken": opts.authtoken},
		},
		Endpoints:              opts.endpoints,
		ConnectURL:             opts.connectURL,
		ConnectCACertFile:      opts.connectCACertFile,
		ProxyURL:               opts.proxyURL,
		HeartbeatInterval:      opts.heartbeatInterval,
		HeartbeatTolerance:     opts.heartbeatTolerance,
		UpdateSourceURL:        opts.updateSourceURL,
		UpdateSignerThumbprint: opts.updateSignerThumbprint,
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("generated config failed validation: %w", err)
	}
	return local.Write(path, cfg)
}
