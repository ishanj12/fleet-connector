package tunnel

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.ngrok.com/ngrok/v2"

	"fleet-connector/internal/config"
)

var proxyProtoVersions = map[string]ngrok.ProxyProtoVersion{
	"1": ngrok.ProxyProtoV1,
	"2": ngrok.ProxyProtoV2,
}

// Build turns an Endpoint into the arguments ngrok.Agent.Forward expects:
// an upstream target plus zero or more EndpointOptions. Pure and testable —
// no network calls, only local file reads for TLS material.
func Build(ep config.Endpoint) (*ngrok.Upstream, []ngrok.EndpointOption, error) {
	var upstreamOpts []ngrok.UpstreamOption
	if ep.Upstream.Protocol != "" {
		upstreamOpts = append(upstreamOpts, ngrok.WithUpstreamProtocol(ep.Upstream.Protocol))
	}
	if v, ok := proxyProtoVersions[ep.Upstream.ProxyProtocol]; ok {
		upstreamOpts = append(upstreamOpts, ngrok.WithUpstreamProxyProto(v))
	}
	if ep.Upstream.TLSVerify {
		tlsConfig := &tls.Config{}
		if ep.Upstream.TLSVerifyCAs != "" {
			pool, err := loadCertPool(ep.Upstream.TLSVerifyCAs)
			if err != nil {
				return nil, nil, fmt.Errorf("upstream.tls_verify_cas: %w", err)
			}
			tlsConfig.RootCAs = pool
		}
		upstreamOpts = append(upstreamOpts, ngrok.WithUpstreamTLSClientConfig(tlsConfig))
	}
	upstream := ngrok.WithUpstream(normalizeUpstreamAddr(ep.Upstream.URL, ep.URL), upstreamOpts...)

	var opts []ngrok.EndpointOption
	if ep.URL != "" {
		opts = append(opts, ngrok.WithURL(ep.URL))
	}
	if ep.Name != "" {
		opts = append(opts, ngrok.WithName(ep.Name))
	}
	if ep.Description != "" {
		opts = append(opts, ngrok.WithDescription(ep.Description))
	}
	if ep.Metadata != "" {
		opts = append(opts, ngrok.WithMetadata(ep.Metadata))
	}
	if ep.TrafficPolicy != "" {
		opts = append(opts, ngrok.WithTrafficPolicy(ep.TrafficPolicy))
	}
	if len(ep.Bindings) > 0 {
		opts = append(opts, ngrok.WithBindings(ep.Bindings...))
	}
	if ep.PoolingEnabled {
		opts = append(opts, ngrok.WithPoolingEnabled(true))
	}
	if t := ep.AgentTLSTermination; t != nil {
		cert, err := tls.LoadX509KeyPair(t.ServerCertificate, t.ServerPrivateKey)
		if err != nil {
			return nil, nil, fmt.Errorf("load agent_tls_termination keypair: %w", err)
		}
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
		if t.MutualTLSCertificateAuthorities != "" {
			pool, err := loadCertPool(t.MutualTLSCertificateAuthorities)
			if err != nil {
				return nil, nil, fmt.Errorf("agent_tls_termination.mutual_tls_certificate_authorities: %w", err)
			}
			tlsConfig.ClientCAs = pool
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		}
		opts = append(opts, ngrok.WithAgentTLSTermination(tlsConfig))
	}

	return upstream, opts, nil
}

// normalizeUpstreamAddr works around a real parsing gap in
// golang.ngrok.com/ngrok/v2@v2.1.4: WithUpstream's own doc comment claims a
// bare port or a schemeless "host:port" work directly, but the SDK parses
// the address with net/url.Parse, which doesn't treat either form as a
// hierarchical host:port — "localhost:8080" parses with Scheme="localhost"
// and Opaque="8080" (Go reads anything before the first colon that looks
// like a scheme token as one), and a bare "8080" parses as a relative
// Path with no host at all. Either way Hostname()/Port() come back empty,
// and the SDK's own fallback then silently dials "localhost:80" instead of
// the port actually configured — confirmed directly against a real ngrok
// endpoint (a working backend on :8080 got zero connections until the
// upstream address carried an explicit scheme).
//
// The scheme this prepends matters beyond just parsing: the SDK's own
// forwarder picks its entire forwarding strategy off the upstream URL's
// scheme alone (isHTTP() in forwarder.go checks only http/https there,
// completely independent of the endpoint's own public scheme) — an
// "http"/"https" upstream gets reverse-proxied at the HTTP layer,
// anything else gets forwarded as a raw byte stream. So a schemeless
// address can't always default to "http://": a raw-TCP-shaped endpoint
// (Endpoint.URL scheme tcp/tls — this spec's own RDP example, §4.1, is
// exactly this) needs its schemeless upstream treated as raw TCP too, or
// the HTTP reverse-proxy path would try to parse RDP's wire protocol as
// HTTP and break it. Matches classic ngrok CLI convention: a tcp/tls
// endpoint implies a raw TCP upstream by default; anything else
// (including the blank/default-https case) implies an HTTP-shaped one.
func normalizeUpstreamAddr(addr, endpointURL string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	scheme := "http"
	if u, err := url.Parse(endpointURL); err == nil {
		switch strings.ToLower(u.Scheme) {
		case "tcp", "tls":
			scheme = "tcp"
		}
	}
	if _, err := strconv.Atoi(addr); err == nil {
		return scheme + "://localhost:" + addr
	}
	return scheme + "://" + addr
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid PEM certificates found in %q", path)
	}
	return pool, nil
}
