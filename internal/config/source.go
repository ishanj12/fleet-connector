package config

import "context"

// Source is implemented by whatever produces a Config for this install —
// today only internal/config/local.Source, but the seam a future
// central-fetch Source plugs into with zero other changes.
type Source interface {
	Load(ctx context.Context) (Config, error)
}

// Resolve is the one funnel every Source passes through — defaults and
// validation live here only, never duplicated in individual Source
// implementations.
func Resolve(ctx context.Context, src Source) (Config, error) {
	cfg, err := src.Load(ctx)
	if err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
