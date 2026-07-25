package main

import (
	"net"
	"os"
)

// runAsWindowsServiceIfApplicable never applies on Linux — systemd
// supervises this same binary declaratively via the unit file (no active
// handshake protocol the way Windows' SCM requires), so runForeground in
// main.go is already the real Linux service entrypoint.
func runAsWindowsServiceIfApplicable() (handled bool, err error) {
	return false, nil
}

// notifyReady tells systemd the service is ready, if this unit uses
// Type=notify (NOTIFY_SOCKET set) — a small, dependency-free write to the
// unix socket systemd provides. No-ops under Type=simple (the default),
// where NOTIFY_SOCKET is unset.
func notifyReady() {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return
	}
	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("READY=1"))
}

func defaultLogPath() string {
	if p := os.Getenv("FLEETCONNECT_LOG_PATH"); p != "" {
		return p
	}
	return "/var/log/fleetconnect/fleetconnect.log"
}

// defaultConfigPath is the fixed, well-known path documented in §4.2/§11.
func defaultConfigPath() string {
	if p := os.Getenv("FLEETCONNECT_CONFIG"); p != "" {
		return p
	}
	return "/etc/fleetconnect/config.yaml"
}
