package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"fleet-connector/internal/config"
	"fleet-connector/internal/config/local"
)

// runEditConfig opens an already-deployed config.yaml directly in the
// user's editor — the same visudo/crontab -e/kubectl-edit pattern: edit
// the real file's contents in place, then validate before committing, so
// a typo never lands as a broken config. This is the CLI counterpart to
// hand-editing the YAML directly (§2's documented ops/support path),
// just with the validation step built in rather than left to the next
// service restart to discover.
//
// Applying the change still needs a real process restart (systemctl
// restart/service restart) — a dashboard-initiated restart only recycles
// the SDK connection using the config already loaded in memory at
// process startup; it never re-reads config.yaml from disk.
func runEditConfig(args []string) error {
	path := "config.yaml"
	if len(args) > 0 {
		path = args[0]
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}

	tmp, err := os.CreateTemp("", "fleetconnect-edit-*.yaml")
	if err != nil {
		return fmt.Errorf("create scratch file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(original); err != nil {
		tmp.Close()
		return fmt.Errorf("write scratch file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write scratch file: %w", err)
	}

	editor := resolveEditor()

	for {
		cmd := exec.Command(editor[0], append(editor[1:], tmpPath)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("launching editor %q: %w", strings.Join(editor, " "), err)
		}

		edited, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("read edited config: %w", err)
		}

		if bytes.Equal(original, edited) {
			fmt.Println("no changes made")
			return nil
		}

		var cfg config.Config
		parseErr := yaml.Unmarshal(edited, &cfg)
		var validateErr error
		if parseErr == nil {
			validateErr = config.Validate(cfg)
		}
		if parseErr == nil && validateErr == nil {
			if err := local.Write(path, cfg); err != nil {
				return fmt.Errorf("write config %q: %w", path, err)
			}
			fmt.Println("config updated — restart the service (e.g. `systemctl restart fleetconnect` on Linux, or restart the Windows service) to apply it; a dashboard-initiated restart reuses the config already loaded in memory and will NOT pick this up")
			return nil
		}

		problem := parseErr
		if problem == nil {
			problem = validateErr
		}
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", problem)
		if !promptRetry() {
			return fmt.Errorf("edit aborted, %q left unchanged", path)
		}
	}
}

// resolveEditor honors $VISUAL/$EDITOR (the same convention every other
// "edit this file directly" CLI tool uses — visudo, crontab -e, git
// commit), splitting on whitespace to allow e.g. EDITOR="code --wait".
// Falls back to a sensible per-platform default when neither is set.
func resolveEditor() []string {
	for _, envVar := range []string{"VISUAL", "EDITOR"} {
		if v := os.Getenv(envVar); v != "" {
			return strings.Fields(v)
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad.exe"}
	}
	for _, candidate := range []string{"nano", "vi"} {
		if p, err := exec.LookPath(candidate); err == nil {
			return []string{p}
		}
	}
	return []string{"vi"}
}

// promptRetry asks whether to reopen the editor after an invalid edit —
// visudo's exact behavior on a syntax error, rather than silently
// discarding a field installer's typo'd edit with no way back in. A
// read error (most commonly stdin already at EOF, e.g. this command
// invoked non-interactively) aborts rather than defaulting to retry —
// otherwise a non-interactive invocation with a permanently-broken edit
// would spin reopening the editor forever.
func promptRetry() bool {
	fmt.Fprint(os.Stderr, "Edit again? [Y/n] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}
