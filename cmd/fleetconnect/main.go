// fleetconnect is the entrypoint: parse mode, wire deps, call agent.App or
// the gen-config subcommand. On Windows started by the SCM, main defers to
// service_windows.go's handler; otherwise (a manual/interactive run, or
// the real Linux service entrypoint under systemd — see §9) it runs in
// the foreground here.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fleet-connector/internal/agent"
	"fleet-connector/internal/config/local"
	"fleet-connector/internal/credentials"
	"fleet-connector/internal/logging"
	"fleet-connector/internal/tunnel"
	"fleet-connector/internal/update"
	"fleet-connector/internal/version"
	"fleet-connector/internal/wizard"
)

// serviceName is a build-time template value — the adopting customer
// rebrands this as their own product's service name (§1's Preface). It
// also doubles as the Windows Event Log source name.
const serviceName = "FleetConnector"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "gen-config" {
		if err := runGenConfig(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fleetconnect gen-config:", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "edit-config" {
		if err := runEditConfig(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fleetconnect edit-config:", err)
			os.Exit(1)
		}
		return
	}

	if handled, err := runAsWindowsServiceIfApplicable(); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleetconnect:", err)
			os.Exit(1)
		}
		return
	}

	runForeground()
}

func runForeground() {
	configPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	flag.Parse()

	platformHandler, err := logging.NewPlatformHandler(serviceName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleetconnect:", err)
		os.Exit(1)
	}
	defer platformHandler.Close()
	log, level := logging.New(defaultLogPath(), platformHandler)
	update.ReportPreviousAttempt(update.DefaultMarkerPath(), version.Version, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := ensureConfig(ctx, *configPath, log); err != nil {
		if ctx.Err() != nil {
			// A stop signal arrived while the wizard was still waiting on
			// the installer (ensureConfig/wizard.Serve return ctx.Err() in
			// that case) — a routine stop, not a startup failure. Exiting
			// non-zero here would misreport an ordinary `systemctl stop`
			// (or an install-time package upgrade) as a crash, the same
			// distinction the steady-state loop below already makes
			// correctly for the identical signal once the tunnel is up.
			log.Info("stopped before setup completed")
			return
		}
		fmt.Fprintln(os.Stderr, "fleetconnect:", err)
		os.Exit(1)
	}

	src := local.New(*configPath)
	app, err := agent.New(ctx, src, credentials.StaticProvider{}, log, level)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleetconnect:", err)
		os.Exit(1)
	}

	if err := app.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fleetconnect: start:", err)
		os.Exit(1)
	}
	log.Info("started", "status", app.Status())
	notifyReady()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("stopping")
			stopAndExit(app, log)
			return
		case <-app.StopRequested():
			// Dashboard-initiated stop (RPCHandler's StopAgentMethod, see
			// tunnel/rpc.go, which already logs the trigger itself). A
			// clean, zero-exit-code return here is already correct under
			// systemd's default Restart=on-failure (§9) — unlike
			// Windows' SCM (§8), no extra signaling is needed to avoid a
			// crash-restart.
			stopAndExit(app, log)
			return
		case <-ticker.C:
			log.Info("status", "status", app.Status())
		}
	}
}

func stopAndExit(app *agent.App, log *slog.Logger) {
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		fmt.Fprintln(os.Stderr, "fleetconnect: stop:", err)
		os.Exit(1)
	}
}

// ensureConfig launches the wizard if configPath doesn't already exist
// (§6) and blocks until it writes one, or ctx is canceled. If a file
// already exists, this is a no-op — the wizard only ever runs once, on an
// install's very first start.
func ensureConfig(ctx context.Context, configPath string, log *slog.Logger) error {
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %q: %w", configPath, err)
	}
	return wizard.Serve(ctx, configPath, credentials.StaticProvider{}, tunnel.DefaultFactory, log)
}
