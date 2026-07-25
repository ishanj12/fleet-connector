//go:build !windows && !linux

package main

import "os"

// Only windows and linux are shipping targets (§1). These stubs exist
// purely so the module builds and runs natively on a developer's own
// machine (e.g. macOS) without needing a GOOS override for every command.
func runAsWindowsServiceIfApplicable() (handled bool, err error) {
	return false, nil
}

func notifyReady() {}

func defaultLogPath() string {
	if p := os.Getenv("FLEETCONNECT_LOG_PATH"); p != "" {
		return p
	}
	return "fleetconnect.log"
}

func defaultConfigPath() string {
	if p := os.Getenv("FLEETCONNECT_CONFIG"); p != "" {
		return p
	}
	return "config.yaml"
}
