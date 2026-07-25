package tunnel

import (
	"errors"
	"fmt"
	"testing"
)

// fakeNgrokError implements ngrok.Error (error + Code() string) so classify
// can be tested without a real SDK-produced error.
type fakeNgrokError struct{ code string }

func (e fakeNgrokError) Error() string { return fmt.Sprintf("fake ngrok error %s", e.code) }
func (e fakeNgrokError) Code() string  { return e.code }

func TestClassifyPlainErrorIsRetryable(t *testing.T) {
	if got := classify(errors.New("connection refused")); got != retryable {
		t.Errorf("plain error: got %v, want retryable", got)
	}
}

func TestClassifyNilIsRetryable(t *testing.T) {
	if got := classify(nil); got != retryable {
		t.Errorf("nil error: got %v, want retryable", got)
	}
}

func TestClassifyKnownFatalCodes(t *testing.T) {
	// A representative sample from each category documented in
	// fatalCodes, cross-checked against https://ngrok.com/docs/errors/reference.
	for _, code := range []string{
		"ERR_NGROK_105", // authtoken format
		"ERR_NGROK_107", // authtoken revoked/invalid
		"ERR_NGROK_307", // address not reserved
		"ERR_NGROK_108", // account session limit
		"ERR_NGROK_103", // account suspended
		"ERR_NGROK_312", // TLS endpoint creation failed
	} {
		if got := classify(fakeNgrokError{code: code}); got != fatal {
			t.Errorf("code %s: got %v, want fatal", code, got)
		}
	}
}

func TestClassifyUnknownNgrokCodeIsRetryable(t *testing.T) {
	// A well-formed ngrok.Error whose code isn't in the fatal allowlist
	// (e.g. a transient server-side error) should still be retried
	// normally, not treated as fatal.
	if got := classify(fakeNgrokError{code: "ERR_NGROK_999999"}); got != retryable {
		t.Errorf("unrecognized ngrok code: got %v, want retryable", got)
	}
}

func TestClassifyWrappedNgrokError(t *testing.T) {
	// classify uses errors.As, so a wrapped ngrok.Error must still be
	// detected — this is exactly the shape Manager's own error returns
	// take (e.g. fmt.Errorf("endpoint %q: forward: %w", label, err)).
	wrapped := fmt.Errorf("endpoint %q: forward: %w", "pos-1", fakeNgrokError{code: "ERR_NGROK_107"})
	if got := classify(wrapped); got != fatal {
		t.Errorf("wrapped fatal ngrok error: got %v, want fatal", got)
	}
}
