// Package wizard is the local web UI for a non-technical installer,
// started only when config.yaml doesn't already exist at service start
// (§6). It never listens beyond 127.0.0.1 (§2's invariant) and tears
// itself down the moment a valid config is written.
package wizard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"fleet-connector/internal/config"
	"fleet-connector/internal/config/local"
	"fleet-connector/internal/credentials"
	"fleet-connector/internal/tunnel"
)

//go:embed templates/*.html
var templatesFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

const maxManualEndpoints = 20 // a generous ceiling on the manual-entry accumulator, not a real limit on Config.Endpoints itself

// Serve starts the wizard on an ephemeral loopback port, opens the default
// browser to it (best-effort — see the headless-Linux note in §6), and
// blocks until either the installer confirms a config (written to
// configPath) or ctx is canceled. creds/factory are used to validate the
// submitted config with a real connection attempt before writing it (see
// handleConfirm) — pass tunnel.DefaultFactory and credentials.StaticProvider{}
// for the real thing; tests substitute fakes.
func Serve(ctx context.Context, configPath string, creds credentials.Provider, factory tunnel.AgentFactory, log *slog.Logger) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("wizard: listen: %w", err)
	}

	token, err := randomToken()
	if err != nil {
		ln.Close()
		return fmt.Errorf("wizard: generate token: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), token)
	log.Info("wizard listening", "url", url)

	w := &wizardState{token: token, configPath: configPath, creds: creds, factory: factory, log: log, done: make(chan error, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/", w.handleForm)
	mux.HandleFunc("/new-endpoint", w.handleNewEndpoint)
	mux.HandleFunc("/save-endpoint", w.handleSaveEndpoint)
	mux.HandleFunc("/remove-endpoint", w.handleRemoveEndpoint)
	mux.HandleFunc("/edit-endpoint", w.handleEditEndpoint)
	mux.HandleFunc("/submit", w.handleSubmit)
	mux.HandleFunc("/confirm", w.handleConfirm)
	srv := &http.Server{Handler: mux}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case w.done <- err:
			default:
			}
		}
	}()

	if err := openBrowser(url); err != nil {
		log.Warn("could not auto-launch browser; open the URL manually, or from a different machine that has one",
			"error", err,
			"remote_access_hint", fmt.Sprintf("ssh -L %d:127.0.0.1:%d <user>@<this-host>, then open http://localhost:%d/?t=... in your own browser", port, port, port))
	}

	var result error
	select {
	case result = <-w.done:
	case <-ctx.Done():
		result = ctx.Err()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return result
}

func randomToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type wizardState struct {
	token      string
	configPath string
	creds      credentials.Provider
	factory    tunnel.AgentFactory
	log        *slog.Logger
	done       chan error
}

func (w *wizardState) checkToken(r *http.Request) bool {
	got := r.URL.Query().Get("t")
	if got == "" {
		got = r.FormValue("t")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(w.token)) == 1
}

// endpointFields is one endpoint's worth of form data — every field
// config.Endpoint/config.Upstream/config.AgentTLSTermination exposes,
// covering both the manual-entry accumulator's saved list and the
// currently-open draft fieldset. Bindings is kept as a single
// comma-separated string for the round trip and split when converting to
// config.Endpoint. Only UpstreamURL is actually required (§4.1) —
// everything else here is optional.
type endpointFields struct {
	Index int

	Name string
	URL  string

	UpstreamURL           string
	UpstreamProtocol      string
	UpstreamProxyProtocol string
	UpstreamTLSVerify     bool
	UpstreamTLSVerifyCAs  string

	Bindings      string
	Metadata      string
	Description   string
	TrafficPolicy string

	AgentTLSCert    string
	AgentTLSKey     string
	AgentTLSMTLSCAs string

	PoolingEnabled bool
}

// templateEndpoint adds the hidden-field name prefix ("saved" or
// "confirm") templates need but endpointFields itself has no reason to
// carry — Go templates promote embedded struct fields, so
// {{.Name}}/{{.UpstreamURL}}/etc still work directly on a templateEndpoint.
type templateEndpoint struct {
	Prefix string
	endpointFields
}

func wrapEndpoints(prefix string, fields []endpointFields) []templateEndpoint {
	out := make([]templateEndpoint, len(fields))
	for i, f := range fields {
		out[i] = templateEndpoint{Prefix: prefix, endpointFields: f}
	}
	return out
}

func (f endpointFields) toConfigEndpoint() config.Endpoint {
	ep := config.Endpoint{
		Name:           f.Name,
		URL:            f.URL,
		Metadata:       f.Metadata,
		Description:    f.Description,
		TrafficPolicy:  f.TrafficPolicy,
		PoolingEnabled: f.PoolingEnabled,
		Upstream: config.Upstream{
			URL:           f.UpstreamURL,
			Protocol:      f.UpstreamProtocol,
			ProxyProtocol: f.UpstreamProxyProtocol,
			TLSVerify:     f.UpstreamTLSVerify,
			TLSVerifyCAs:  f.UpstreamTLSVerifyCAs,
		},
	}
	// "public" is the endpoint's own default binding — only set Bindings
	// at all when something else was explicitly chosen, so a form
	// submitted with the default selection doesn't produce a redundant
	// explicit value.
	if f.Bindings != "" && f.Bindings != "public" {
		ep.Bindings = []string{f.Bindings}
	}
	if f.AgentTLSCert != "" || f.AgentTLSKey != "" || f.AgentTLSMTLSCAs != "" {
		ep.AgentTLSTermination = &config.AgentTLSTermination{
			ServerCertificate:               f.AgentTLSCert,
			ServerPrivateKey:                f.AgentTLSKey,
			MutualTLSCertificateAuthorities: f.AgentTLSMTLSCAs,
		}
	}
	return ep
}

func endpointFromConfig(ep config.Endpoint) endpointFields {
	bindings := "public"
	if len(ep.Bindings) > 0 {
		bindings = ep.Bindings[0] // the dropdown only ever selects one; a hand-authored config with several just shows the first
	}
	f := endpointFields{
		Name: ep.Name, URL: ep.URL,
		UpstreamURL: ep.Upstream.URL, UpstreamProtocol: ep.Upstream.Protocol, UpstreamProxyProtocol: ep.Upstream.ProxyProtocol,
		UpstreamTLSVerify: ep.Upstream.TLSVerify, UpstreamTLSVerifyCAs: ep.Upstream.TLSVerifyCAs,
		Bindings: bindings, Metadata: ep.Metadata, Description: ep.Description, TrafficPolicy: ep.TrafficPolicy,
		PoolingEnabled: ep.PoolingEnabled,
	}
	if ep.AgentTLSTermination != nil {
		f.AgentTLSCert = ep.AgentTLSTermination.ServerCertificate
		f.AgentTLSKey = ep.AgentTLSTermination.ServerPrivateKey
		f.AgentTLSMTLSCAs = ep.AgentTLSTermination.MutualTLSCertificateAuthorities
	}
	return f
}

// readEndpointFields pulls one endpoint's worth of fields via get, which
// abstracts the two different form-field naming schemes: "saved_X_N" for
// the accumulator's saved list (get closes over a fixed index N) and
// "X_new" for the currently-open draft (get closes over no index at all).
func readEndpointFields(get func(field string) string) endpointFields {
	return endpointFields{
		Name: get("name"), URL: get("url"),
		UpstreamURL: get("upstream_url"), UpstreamProtocol: get("upstream_protocol"), UpstreamProxyProtocol: get("upstream_proxy_protocol"),
		UpstreamTLSVerify: get("upstream_tls_verify") != "", UpstreamTLSVerifyCAs: get("upstream_tls_verify_cas"),
		Bindings: get("bindings"), Metadata: get("metadata"), Description: get("description"), TrafficPolicy: get("traffic_policy"),
		AgentTLSCert: get("agent_tls_cert"), AgentTLSKey: get("agent_tls_key"), AgentTLSMTLSCAs: get("agent_tls_mtls_cas"),
		PoolingEnabled: get("pooling_enabled") != "",
	}
}

func parseIndexedEndpoints(r *http.Request, prefix string, count int) []endpointFields {
	if count > maxManualEndpoints {
		count = maxManualEndpoints
	}
	var out []endpointFields
	for i := 0; i < count; i++ {
		suffix := strconv.Itoa(i)
		ep := readEndpointFields(func(field string) string { return r.FormValue(prefix + "_" + field + "_" + suffix) })
		if ep.UpstreamURL == "" {
			continue // a blank fieldset that never got saved properly — skip rather than reject
		}
		out = append(out, ep)
	}
	reindex(out)
	return out
}

func parseSavedEndpoints(r *http.Request) []endpointFields {
	count, _ := strconv.Atoi(r.FormValue("saved_count"))
	return parseIndexedEndpoints(r, "saved", count)
}

func parseDraft(r *http.Request) endpointFields {
	return readEndpointFields(func(field string) string { return r.FormValue(field + "_new") })
}

func reindex(fields []endpointFields) {
	for i := range fields {
		fields[i].Index = i
	}
}

// sessionFields are the Config-level (not per-endpoint) settings gen-config
// also exposes — Description/Metadata are session tags visible in the
// dashboard, LogLevel governs this app's own log verbosity (§4.2), and
// AuthToken is the credential itself. They round-trip as plain preserved
// fields, same pattern as the draft endpoint's fields, just without an
// index since there's only ever one. AuthToken belongs here for the same
// reason: every button (new-endpoint, save-endpoint, remove-endpoint) is a
// full form POST that re-renders the page from scratch, and without a
// server-side value to refill it with, the field just comes back empty —
// confirmed live, not hypothetical (typing an authtoken then clicking
// "new endpoint" silently wiped it before this field existed).
type sessionFields struct {
	Description        string
	Metadata           string
	LogLevel           string
	AuthToken          string
	ConnectURL         string
	ConnectCACertFile  string
	ProxyURL           string
	HeartbeatInterval  string
	HeartbeatTolerance string
}

func readSessionFields(r *http.Request) sessionFields {
	return sessionFields{
		Description:        r.FormValue("config_description"),
		Metadata:           r.FormValue("config_metadata"),
		LogLevel:           r.FormValue("config_log_level"),
		AuthToken:          r.FormValue("authtoken"),
		ConnectURL:         r.FormValue("config_connect_url"),
		ConnectCACertFile:  r.FormValue("config_connect_ca_cert_file"),
		ProxyURL:           r.FormValue("config_proxy_url"),
		HeartbeatInterval:  r.FormValue("config_heartbeat_interval"),
		HeartbeatTolerance: r.FormValue("config_heartbeat_tolerance"),
	}
}

// draftOpenState decides whether the editable "endpoint settings"
// fieldset should be shown: explicit if the form said so, otherwise
// closed by default — a fresh page always starts with just the "New
// Endpoint" button, never the fieldset pre-opened.
func draftOpenState(r *http.Request) bool {
	return r.Form.Get("draft_open") == "1"
}

func formData(token string, saved []endpointFields, draftOpen bool, draft endpointFields, session sessionFields, errMsg string) map[string]any {
	return map[string]any{
		"Token": token, "Error": errMsg, "Saved": wrapEndpoints("saved", saved), "DraftOpen": draftOpen, "Draft": draft,
		"Session": session,
	}
}

func (w *wizardState) handleForm(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	saved := parseSavedEndpoints(r)
	w.render(rw, "form.html", formData(w.token, saved, draftOpenState(r), parseDraft(r), readSessionFields(r), ""))
}

// handleNewEndpoint is the "+" button's target: it just opens a fresh
// blank draft fieldset, without touching the already-saved list.
func (w *wizardState) handleNewEndpoint(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	saved := parseSavedEndpoints(r)
	w.render(rw, "form.html", formData(w.token, saved, true, endpointFields{}, readSessionFields(r), ""))
}

// handleSaveEndpoint is the "Save" button's target: it appends the
// currently-open draft to the saved list (rejecting a blank upstream) and
// closes the draft, showing the new entry as a box.
func (w *wizardState) handleSaveEndpoint(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	saved := parseSavedEndpoints(r)
	draft := parseDraft(r)
	session := readSessionFields(r)
	if draft.UpstreamURL == "" {
		w.render(rw, "form.html", formData(w.token, saved, true, draft, session, "upstream is required to save an endpoint"))
		return
	}
	if len(saved) >= maxManualEndpoints {
		w.render(rw, "form.html", formData(w.token, saved, true, draft, session, fmt.Sprintf("cannot add more than %d endpoints here", maxManualEndpoints)))
		return
	}
	saved = append(saved, draft)
	reindex(saved)
	w.render(rw, "form.html", formData(w.token, saved, false, endpointFields{}, session, ""))
}

// handleRemoveEndpoint is each saved box's trash-can button target.
func (w *wizardState) handleRemoveEndpoint(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	saved := parseSavedEndpoints(r)
	if idx, err := strconv.Atoi(r.URL.Query().Get("index")); err == nil && idx >= 0 && idx < len(saved) {
		saved = append(saved[:idx], saved[idx+1:]...)
		reindex(saved)
	}
	w.render(rw, "form.html", formData(w.token, saved, draftOpenState(r), parseDraft(r), readSessionFields(r), ""))
}

// handleEditEndpoint is each saved box's pencil-icon button target: it
// pulls that endpoint out of the saved list and reopens it as the
// currently-editable draft (with the Advanced section's fields intact,
// same as everything else in endpointFields), so editing and re-Saving it
// overwrites the old values rather than requiring a full remove-then-
// retype-from-scratch — the earlier design had no way back into an
// already-saved endpoint at all once Saved.
func (w *wizardState) handleEditEndpoint(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	saved := parseSavedEndpoints(r)
	idx, err := strconv.Atoi(r.URL.Query().Get("index"))
	if err != nil || idx < 0 || idx >= len(saved) {
		w.render(rw, "form.html", formData(w.token, saved, draftOpenState(r), parseDraft(r), readSessionFields(r), ""))
		return
	}
	draft := saved[idx]
	saved = append(saved[:idx], saved[idx+1:]...)
	reindex(saved)
	w.render(rw, "form.html", formData(w.token, saved, true, draft, readSessionFields(r), ""))
}

// handleSubmit accepts the accumulated manual-entry endpoints (the saved
// list plus the open draft, if its upstream is filled in), validates the
// resulting Config, and shows a confirmation screen with the authtoken
// masked — the one manual authoring step happens once, centrally, by ops
// running gen-config; this screen never lets that value be silently
// re-typed wrong (§6).
func (w *wizardState) handleSubmit(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	session := readSessionFields(r)
	saved := parseSavedEndpoints(r)
	if draft := parseDraft(r); draft.UpstreamURL != "" {
		saved = append(saved, draft)
	}
	var endpoints []config.Endpoint
	for _, s := range saved {
		endpoints = append(endpoints, s.toConfigEndpoint())
	}

	cfg := config.Config{
		SchemaVersion:      config.CurrentSchemaVersion,
		Description:        session.Description,
		Metadata:           session.Metadata,
		LogLevel:           session.LogLevel,
		ConnectURL:         session.ConnectURL,
		ConnectCACertFile:  session.ConnectCACertFile,
		ProxyURL:           session.ProxyURL,
		HeartbeatInterval:  session.HeartbeatInterval,
		HeartbeatTolerance: session.HeartbeatTolerance,
		Credential:         config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": session.AuthToken}},
		Endpoints:          endpoints,
	}
	if err := config.Validate(cfg); err != nil {
		saved := parseSavedEndpoints(r)
		w.render(rw, "form.html", formData(w.token, saved, draftOpenState(r), parseDraft(r), session, "invalid configuration: "+err.Error()))
		return
	}

	fields := make([]endpointFields, len(endpoints))
	for i, ep := range endpoints {
		f := endpointFromConfig(ep)
		f.Index = i
		fields[i] = f
	}
	w.render(rw, "confirm.html", map[string]any{
		"Token": w.token, "Endpoints": wrapEndpoints("confirm", fields), "Count": len(fields),
		"Session": session, "AuthToken": session.AuthToken, "AuthTokenMasked": maskToken(session.AuthToken),
	})
}

func (w *wizardState) handleConfirm(rw http.ResponseWriter, r *http.Request) {
	if !w.checkToken(r) {
		http.Error(rw, "invalid or missing token", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	count, _ := strconv.Atoi(r.FormValue("confirm_count"))
	fields := parseIndexedEndpoints(r, "confirm", count)
	endpoints := make([]config.Endpoint, len(fields))
	for i, f := range fields {
		endpoints[i] = f.toConfigEndpoint()
	}

	session := readSessionFields(r)
	cfg := config.Config{
		SchemaVersion:      config.CurrentSchemaVersion,
		Description:        session.Description,
		Metadata:           session.Metadata,
		LogLevel:           session.LogLevel,
		ConnectURL:         session.ConnectURL,
		ConnectCACertFile:  session.ConnectCACertFile,
		ProxyURL:           session.ProxyURL,
		HeartbeatInterval:  session.HeartbeatInterval,
		HeartbeatTolerance: session.HeartbeatTolerance,
		Credential:         config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": session.AuthToken}},
		Endpoints:          endpoints,
	}
	renderErr := func(msg string) {
		w.render(rw, "confirm.html", map[string]any{
			"Token": w.token, "Endpoints": wrapEndpoints("confirm", fields), "Count": len(fields),
			"Session": session, "AuthToken": session.AuthToken, "AuthTokenMasked": maskToken(session.AuthToken),
			"Error": msg,
		})
	}

	if err := config.Validate(cfg); err != nil {
		renderErr("invalid configuration: " + err.Error())
		return
	}

	// Validate with a real connection attempt before writing anything —
	// confirmed live that without this, a bad authtoken (or a broken
	// endpoint) still got written and reported "done", and the actual
	// failure only ever surfaced later in the log file once the real
	// service tried to connect, invisible from the wizard's own UI. On
	// failure, re-render this same confirm screen with the real error
	// rather than writing config.yaml at all — nothing on disk to clean
	// up, so the installer can just fix the value and resubmit.
	testCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := tunnel.TestConnect(testCtx, cfg, w.creds, w.factory, w.log); err != nil {
		renderErr("could not connect with this configuration: " + err.Error())
		return
	}

	if err := local.Write(w.configPath, cfg); err != nil {
		renderErr("failed to write config: " + err.Error())
		return
	}

	w.render(rw, "done.html", nil)
	select {
	case w.done <- nil:
	default:
	}
}

func (w *wizardState) render(rw http.ResponseWriter, name string, data any) {
	if err := templates.ExecuteTemplate(rw, name, data); err != nil {
		w.log.Error("render template", "template", name, "error", err)
	}
}

func maskToken(tok string) string {
	if len(tok) <= 8 {
		return "••••••••"
	}
	return tok[:4] + "…" + tok[len(tok)-4:]
}
