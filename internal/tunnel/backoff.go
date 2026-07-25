package tunnel

import (
	"math/rand"
	"time"
)

// backoff is exponential with full jitter, capped at max. fatal-classified
// errors wait a full extra multiple of the current step on top, since
// retrying rapidly against e.g. a revoked authtoken wastes cycles without
// ops having changed anything.
type backoff struct {
	base    time.Duration
	max     time.Duration
	current time.Duration
}

func newBackoff(base, max time.Duration) *backoff {
	return &backoff{base: base, max: max, current: base}
}

// Next returns the wait duration for the upcoming retry and advances the
// internal step. Never returns zero.
func (b *backoff) Next(class errClass) time.Duration {
	wait := b.current
	if class == fatal {
		wait *= 4
		if wait > b.max {
			wait = b.max
		}
	}

	b.current *= 2
	if b.current > b.max {
		b.current = b.max
	}

	// Full jitter: a random duration in [0, wait), floor of 1s so a
	// pathologically small base never produces a near-zero wait.
	jittered := time.Duration(rand.Int63n(int64(wait)))
	if jittered < time.Second {
		jittered = time.Second
	}
	return jittered
}

// Reset forgets any accumulated backoff after a successful connect.
func (b *backoff) Reset() {
	b.current = b.base
}
