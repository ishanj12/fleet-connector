package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"fleet-connector/internal/agent"
	"fleet-connector/internal/config/local"
	"fleet-connector/internal/credentials"
	"fleet-connector/internal/logging"
)

// runAsWindowsServiceIfApplicable runs the app under the Windows SCM if
// this process was started by the SCM, returning handled=true if it did
// (the caller should not also run the interactive foreground path).
func runAsWindowsServiceIfApplicable() (handled bool, err error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false, err
	}
	if !isService {
		return false, nil
	}
	return true, svc.Run(serviceName, &windowsServiceHandler{})
}

func notifyReady() {} // no-op on Windows — the SCM's own Status protocol covers this, see Execute below

type windowsServiceHandler struct{}

// Execute wires SCM start/stop/shutdown to App.Start/Stop. It keeps
// checkpoint/wait-hint reporting visible and auditable rather than using a
// generic cross-platform service-wrapper library (see the plan's Appendix
// A) — that fine-grained control is what avoids the SCM's ~30s
// "not responding" dialog during a slow stop.
func (h *windowsServiceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, statusChan chan<- svc.Status) (bool, uint32) {
	statusChan <- svc.Status{State: svc.StartPending}

	// Idempotent: tolerates "already exists" from a prior install/start.
	// Without a compiled message-table DLL, Event Log falls back to
	// EventCreate.exe's generic message file for rendering.
	_ = eventlog.InstallAsEventCreate(serviceName, eventlog.Info|eventlog.Warning|eventlog.Error)

	platformHandler, err := logging.NewPlatformHandler(serviceName)
	if err != nil {
		statusChan <- svc.Status{State: svc.Stopped}
		return false, 1
	}
	defer platformHandler.Close()
	log, level := logging.New(defaultLogPath(), platformHandler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	configPath := defaultConfigPath()
	if err := ensureConfigCheckpointed(ctx, statusChan, configPath, log); err != nil {
		log.Error("ensure config", "error", err)
		statusChan <- svc.Status{State: svc.Stopped}
		return false, 1
	}

	src := local.New(configPath)
	app, err := agent.New(ctx, src, credentials.StaticProvider{}, log, level)
	if err != nil {
		log.Error("new app", "error", err)
		statusChan <- svc.Status{State: svc.Stopped}
		return false, 1
	}
	if err := app.Start(ctx); err != nil {
		log.Error("start", "error", err)
		statusChan <- svc.Status{State: svc.Stopped}
		return false, 1
	}

	statusChan <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Info("service running")

	stopServiceGracefully := func() {
		stopServiceCheckpointed(statusChan, func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if err := app.Stop(stopCtx); err != nil {
				log.Error("stop", "error", err)
			}
		})
	}

	for {
		select {
		case <-app.StopRequested():
			// Dashboard-initiated stop (RPCHandler's StopAgentMethod, see
			// tunnel/rpc.go, which already logs the trigger itself):
			// proactively report our own stop to the SCM rather than
			// waiting for an external SERVICE_CONTROL_STOP. Without this,
			// a self-initiated stop the SCM didn't ask for looks like an
			// unexpected exit, and the service's configured crash-restart
			// policy would relaunch it right back up.
			stopServiceGracefully()
			return false, 0

		case cr := <-r:
			switch cr.Cmd {
			case svc.Interrogate:
				statusChan <- cr.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Info("stop requested via SCM", "cmd", cr.Cmd)
				stopServiceGracefully()
				return false, 0
			}
		}
	}
}

// ensureConfigCheckpointed runs the wizard (via ensureConfig, main.go) if
// config.yaml doesn't exist yet, reporting StartPending with an
// incrementing checkpoint the whole time. Without this, a wizard-driven
// first start — which can take as long as the installer takes to fill out
// a form, nothing like the SCM's ordinary few-second start budget — would
// hit the SCM's start timeout and be treated as a failed launch.
func ensureConfigCheckpointed(ctx context.Context, statusChan chan<- svc.Status, configPath string, log *slog.Logger) error {
	done := make(chan error, 1)
	go func() { done <- ensureConfig(ctx, configPath, log) }()

	checkpoint := uint32(1)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			checkpoint++
			statusChan <- svc.Status{State: svc.StartPending, CheckPoint: checkpoint, WaitHint: 3000}
		}
	}
}

// stopServiceCheckpointed reports StopPending with an incrementing
// checkpoint while doStop runs, avoiding the SCM's ~30s "not responding"
// dialog, then reports Stopped.
func stopServiceCheckpointed(statusChan chan<- svc.Status, doStop func()) {
	checkpoint := uint32(1)
	done := make(chan struct{})
	go func() {
		doStop()
		close(done)
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			statusChan <- svc.Status{State: svc.Stopped}
			return
		case <-ticker.C:
			checkpoint++
			statusChan <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 3000}
		}
	}
}

func defaultLogPath() string {
	if p := os.Getenv("FLEETCONNECT_LOG_PATH"); p != "" {
		return p
	}
	return `C:\ProgramData\FleetConnector\logs\fleetconnect.log`
}

// defaultConfigPath is the fixed, well-known path documented in §4.2/§10.
func defaultConfigPath() string {
	if p := os.Getenv("FLEETCONNECT_CONFIG"); p != "" {
		return p
	}
	return `C:\ProgramData\FleetConnector\config.yaml`
}
