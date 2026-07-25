package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"fleet-connector/internal/config"
	"fleet-connector/internal/credentials"
)

// fakeSource is a scriptable config.Source — App.New's only external
// dependency besides the credentials.Provider it's handed directly.
type fakeSource struct {
	cfg config.Config
	err error
}

func (f fakeSource) Load(context.Context) (config.Config, error) { return f.cfg, f.err }

func validAppConfig() config.Config {
	return config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Credential:    config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "tok"}},
		Endpoints:     []config.Endpoint{{Upstream: config.Upstream{URL: "localhost:8080"}}},
	}
}

func TestNewSucceedsWithValidConfig(t *testing.T) {
	level := new(slog.LevelVar)
	a, err := New(context.Background(), fakeSource{cfg: validAppConfig()}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("New returned a nil App")
	}
}

func TestNewPropagatesSourceLoadError(t *testing.T) {
	level := new(slog.LevelVar)
	_, err := New(context.Background(), fakeSource{err: errors.New("boom")}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level)
	if err == nil {
		t.Fatal("expected New to propagate a Source.Load error")
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	cfg := validAppConfig()
	cfg.Credential.Provider = "" // config.Validate requires this
	level := new(slog.LevelVar)
	_, err := New(context.Background(), fakeSource{cfg: cfg}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level)
	if err == nil {
		t.Fatal("expected New to reject a config that fails config.Validate")
	}
}

func TestNewLeavesLevelAtDefaultWhenLogLevelUnset(t *testing.T) {
	level := new(slog.LevelVar) // zero value is LevelInfo
	if _, err := New(context.Background(), fakeSource{cfg: validAppConfig()}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level); err != nil {
		t.Fatalf("New: %v", err)
	}
	if level.Level() != slog.LevelInfo {
		t.Errorf("level = %v, want %v (untouched default) when config.LogLevel is empty", level.Level(), slog.LevelInfo)
	}
}

func TestNewAdjustsLevelFromConfig(t *testing.T) {
	cfg := validAppConfig()
	cfg.LogLevel = "debug"
	level := new(slog.LevelVar)
	if _, err := New(context.Background(), fakeSource{cfg: cfg}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level); err != nil {
		t.Fatalf("New: %v", err)
	}
	if level.Level() != slog.LevelDebug {
		t.Errorf("level = %v, want %v after config.LogLevel=\"debug\"", level.Level(), slog.LevelDebug)
	}
}

func TestAppStopBeforeStartIsNoop(t *testing.T) {
	level := new(slog.LevelVar)
	a, err := New(context.Background(), fakeSource{cfg: validAppConfig()}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start should be a no-op, got: %v", err)
	}
}

func TestAppStopRequestedNotClosedBeforeAnyRPC(t *testing.T) {
	level := new(slog.LevelVar)
	a, err := New(context.Background(), fakeSource{cfg: validAppConfig()}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	select {
	case <-a.StopRequested():
		t.Fatal("StopRequested should not be closed before any StopAgentMethod RPC")
	default:
	}
}

func TestAppStatusBeforeStartDoesNotPanic(t *testing.T) {
	level := new(slog.LevelVar)
	a, err := New(context.Background(), fakeSource{cfg: validAppConfig()}, credentials.StaticProvider{}, slog.New(slog.DiscardHandler), level)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = a.Status() // just must not panic before Start has ever been called
}
