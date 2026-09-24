package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// recorder captures what verify/launch were called with, and lets a test
// script either function's return value.
type recorder struct {
	mu sync.Mutex

	verifyCalls []string
	verifyErr   error

	launchCalls []string
	launchErr   error

	diagnoseCalls  []string
	diagnoseResult string
}

func (r *recorder) verify(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verifyCalls = append(r.verifyCalls, path)
	return r.verifyErr
}

func (r *recorder) launch(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launchCalls = append(r.launchCalls, path)
	return r.launchErr
}

func (r *recorder) diagnose(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.diagnoseCalls = append(r.diagnoseCalls, path)
	return r.diagnoseResult
}

func TestApplyDownloadsVerifiesAndLaunches(t *testing.T) {
	const body = "fake installer bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	rec := &recorder{}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	if err := a.Apply(context.Background(), srv.URL); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(rec.verifyCalls) != 1 {
		t.Fatalf("expected exactly 1 verify call, got %d", len(rec.verifyCalls))
	}
	if len(rec.launchCalls) != 1 {
		t.Fatalf("expected exactly 1 launch call, got %d", len(rec.launchCalls))
	}
	if rec.verifyCalls[0] != rec.launchCalls[0] {
		t.Errorf("verify and launch should see the same downloaded path: verify=%q launch=%q", rec.verifyCalls[0], rec.launchCalls[0])
	}

	data, err := os.ReadFile(rec.verifyCalls[0])
	if err == nil {
		t.Errorf("downloaded file %q should have been removed after Apply returned, but still exists with content %q", rec.verifyCalls[0], data)
	}
}

func TestApplyRefusesToLaunchOnVerifyFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("unsigned garbage"))
	}))
	defer srv.Close()

	rec := &recorder{verifyErr: errors.New("signature status \"NotSigned\", expected \"Valid\"")}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	err := a.Apply(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected Apply to fail when verification fails")
	}
	if len(rec.launchCalls) != 0 {
		t.Fatalf("launch must never be called after a failed verification, but was called %d time(s)", len(rec.launchCalls))
	}
}

func TestApplyFailsOnNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rec := &recorder{}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	err := a.Apply(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected Apply to fail on a 404 response")
	}
	if len(rec.verifyCalls) != 0 || len(rec.launchCalls) != 0 {
		t.Error("verify/launch must never be called when the download itself failed")
	}
}

func TestApplyFailsOnUnreachableURL(t *testing.T) {
	rec := &recorder{}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	// Port 0 on localhost is never a live listener.
	err := a.Apply(context.Background(), "http://127.0.0.1:0/installer.msi")
	if err == nil {
		t.Fatal("expected Apply to fail against an unreachable URL")
	}
}

func TestApplyPropagatesLaunchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake installer bytes"))
	}))
	defer srv.Close()

	rec := &recorder{launchErr: errors.New("starting msiexec: access is denied")}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	if err := a.Apply(context.Background(), srv.URL); err == nil {
		t.Fatal("expected Apply to surface a launch failure")
	}
	if len(rec.verifyCalls) != 1 {
		t.Errorf("verify should still have been called once before the launch failure, got %d calls", len(rec.verifyCalls))
	}
}

func TestApplyEnrichesLaunchFailureWithDiagnosis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake installer bytes"))
	}))
	defer srv.Close()

	rec := &recorder{
		launchErr:      errors.New("starting msiexec: access is denied"),
		diagnoseResult: "Trojan:Win32/Wacatac.B!ml",
	}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	err := a.Apply(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected Apply to surface a launch failure")
	}
	if !strings.Contains(err.Error(), "Trojan:Win32/Wacatac.B!ml") {
		t.Errorf("expected error to include the diagnosed threat name, got: %v", err)
	}
	if len(rec.diagnoseCalls) != 1 {
		t.Errorf("expected diagnose to be called once, got %d calls", len(rec.diagnoseCalls))
	}
}

func TestApplyFallsBackToGenericHintWhenDiagnosisEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake installer bytes"))
	}))
	defer srv.Close()

	rec := &recorder{launchErr: errors.New("starting msiexec: access is denied")}
	a := New(nil, rec.verify, rec.launch, rec.diagnose)

	err := a.Apply(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected Apply to surface a launch failure")
	}
	if !strings.Contains(err.Error(), avHint) {
		t.Errorf("expected error to fall back to the generic AV hint, got: %v", err)
	}
	if len(rec.diagnoseCalls) != 1 {
		t.Errorf("expected diagnose to be called once, got %d calls", len(rec.diagnoseCalls))
	}
}

// parseVerifyOutput is the platform-agnostic half of signature verification
// (see verify_windows.go) — these tests exercise the actual safety logic
// (does the thumbprint have to match, not just the status) without needing
// a real Windows machine or a real signed file.
func TestParseVerifyOutput(t *testing.T) {
	const thumbprint = "AABBCCDDEEFF00112233445566778899AABBCCDD"

	for _, tc := range []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{
			name:   "valid status, matching thumbprint",
			output: "Valid\n" + thumbprint,
			want:   thumbprint,
		},
		{
			name:   "valid status, matching thumbprint, different case",
			output: "Valid\n" + "aabbccddeeff00112233445566778899aabbccdd",
			want:   thumbprint,
		},
		{
			name:    "valid status, wrong thumbprint",
			output:  "Valid\n" + "0000000000000000000000000000000000000000",
			want:    thumbprint,
			wantErr: true,
		},
		{
			name:    "valid status, no thumbprint at all (shouldn't happen, but fail closed)",
			output:  "Valid\n",
			want:    thumbprint,
			wantErr: true,
		},
		{
			name:    "not signed",
			output:  "NotSigned\n",
			want:    thumbprint,
			wantErr: true,
		},
		{
			name:    "signed by someone else entirely, still reports Valid",
			output:  "Valid\n" + "FFEEDDCCBBAA00998877665544332211FFEEDDCC",
			want:    thumbprint,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := parseVerifyOutput(tc.output, tc.want)
			if (err != nil) != tc.wantErr {
				t.Errorf("parseVerifyOutput(%q, %q) = %v, wantErr %v", tc.output, tc.want, err, tc.wantErr)
			}
		})
	}
}
