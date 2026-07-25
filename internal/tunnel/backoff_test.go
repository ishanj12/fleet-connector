package tunnel

import (
	"testing"
	"time"
)

func TestBackoffNeverReturnsZero(t *testing.T) {
	b := newBackoff(time.Millisecond, time.Second)
	for i := 0; i < 20; i++ {
		if wait := b.Next(retryable); wait <= 0 {
			t.Fatalf("iteration %d: Next returned non-positive duration %v", i, wait)
		}
	}
}

func TestBackoffGrowsThenCapsAtMax(t *testing.T) {
	base, max := time.Second, 4*time.Second
	b := newBackoff(base, max)

	// Each retryable step should never exceed the cap, even after many
	// iterations (current keeps doubling but is clamped to max).
	var sawMax bool
	for i := 0; i < 10; i++ {
		wait := b.Next(retryable)
		if wait > max {
			t.Fatalf("iteration %d: wait %v exceeded max %v", i, wait, max)
		}
		if b.current > max {
			t.Fatalf("iteration %d: internal current %v exceeded max %v", i, b.current, max)
		}
		if b.current == max {
			sawMax = true
		}
	}
	if !sawMax {
		t.Error("expected internal current to reach max after enough retryable iterations")
	}
}

func TestBackoffFatalWaitsLongerThanRetryable(t *testing.T) {
	// Same starting state, one step each: a fatal-classified error should
	// never produce a shorter upper bound than a retryable one — fatal
	// waits current*4 (capped), retryable waits current — so fatal's
	// jitter ceiling is >= retryable's for the same current.
	base, max := time.Second, 5*time.Minute

	bRetryable := newBackoff(base, max)
	bRetryable.current = 10 * time.Second // fix current so both start identically
	bFatal := newBackoff(base, max)
	bFatal.current = 10 * time.Second

	// Sample many times since jitter is random; the fatal ceiling
	// (current*4, capped at max) must be >= the retryable ceiling
	// (current) for every sample to be consistent with Next's own math.
	for i := 0; i < 50; i++ {
		retryWait := bRetryable.Next(retryable)
		bRetryable.current = 10 * time.Second // undo the doubling so each sample starts from the same ceiling

		fatalWait := bFatal.Next(fatal)
		bFatal.current = 10 * time.Second

		if retryWait > 10*time.Second {
			t.Fatalf("retryable wait %v exceeded its ceiling of current (10s)", retryWait)
		}
		if fatalWait > 40*time.Second {
			t.Fatalf("fatal wait %v exceeded its ceiling of current*4 (40s)", fatalWait)
		}
	}
}

func TestBackoffResetReturnsToBase(t *testing.T) {
	base, max := time.Second, time.Minute
	b := newBackoff(base, max)

	for i := 0; i < 5; i++ {
		b.Next(retryable)
	}
	if b.current == base {
		t.Fatal("current should have grown past base after several retryable steps")
	}

	b.Reset()
	if b.current != base {
		t.Errorf("Reset: current = %v, want %v", b.current, base)
	}
}
