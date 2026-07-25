// Package agent is the lifecycle boundary the service wrapper calls into —
// New/Start/Stop/Status, with no knowledge of Windows service or systemd
// specifics on either side of it.
package agent

import (
	"context"
	"fmt"
	"log/slog"

	"fleet-connector/internal/config"
	"fleet-connector/internal/credentials"
	"fleet-connector/internal/logging"
	"fleet-connector/internal/tunnel"
)

type App struct {
	manager *tunnel.Manager
}

// New resolves cfg from src (defaults + validation, see config.Resolve) and
// builds the tunnel Manager. It does not connect anything yet — call
// Start for that. level is adjusted from cfg.LogLevel once cfg is resolved
// — the logger necessarily has to exist before config.yaml is read, so
// logging.New hands back a *slog.LevelVar for exactly this purpose.
func New(ctx context.Context, src config.Source, creds credentials.Provider, log *slog.Logger, level *slog.LevelVar) (*App, error) {
	cfg, err := config.Resolve(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	if cfg.LogLevel != "" {
		lvl, err := logging.ParseLevel(cfg.LogLevel)
		if err != nil {
			return nil, fmt.Errorf("log_level: %w", err)
		}
		level.Set(lvl)
	}
	mgr, err := tunnel.NewManager(cfg, creds, tunnel.DefaultFactory, log)
	if err != nil {
		return nil, fmt.Errorf("new tunnel manager: %w", err)
	}
	return &App{manager: mgr}, nil
}

func (a *App) Start(ctx context.Context) error {
	return a.manager.Start(ctx)
}

func (a *App) Stop(ctx context.Context) error {
	return a.manager.Stop(ctx)
}

func (a *App) Status() tunnel.Status {
	return a.manager.StatusSnapshot()
}

// StopRequested reports ngrok-dashboard-initiated stop commands (see
// tunnel.Manager.StopRequested) for the platform service wrapper to
// observe and act on.
func (a *App) StopRequested() <-chan struct{} {
	return a.manager.StopRequested()
}
