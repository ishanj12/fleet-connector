// Package local implements the default config.Source: a strictly
// non-interactive reader of a config.yaml file already written by
// gen-config, the wizard, or a hand-edit. It never prompts.
package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"fleet-connector/internal/config"
)

// Source reads Config from a fixed file path. --config/FLEETCONNECT_CONFIG
// only override the file's path, never its contents.
type Source struct {
	Path string
}

func New(path string) *Source {
	return &Source{Path: path}
}

func (s *Source) Load(_ context.Context) (config.Config, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return config.Config{}, fmt.Errorf("read config %q: %w", s.Path, err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return config.Config{}, fmt.Errorf("parse config %q: %w", s.Path, err)
	}
	return cfg, nil
}

// Write renders cfg as YAML to path, the shared writer used by gen-config,
// the wizard, and the platform install-time config writers. Creates path's
// parent directory if it doesn't already exist — neither the MSI nor the
// .deb/.rpm packages create it themselves, since config.yaml is rendered
// at install/wizard time, not shipped as a package file (§10, §11).
func Write(path string, cfg config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}
