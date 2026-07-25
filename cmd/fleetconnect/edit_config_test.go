package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fleet-connector/internal/config"
	"fleet-connector/internal/config/local"
)

// fakeEditor writes a small shell script that just overwrites whatever
// path it's given (argv[1]) with fixture's contents — standing in for an
// interactive $EDITOR session in a non-interactive test.
func fakeEditor(t *testing.T, fixture string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "fake-editor.sh")
	script := "#!/bin/sh\ncp " + fixture + " \"$1\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake editor: %v", err)
	}
	return scriptPath
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

const validConfigYAML = `schema_version: 1
description: updated-desc
credential:
  provider: static
  params:
    authtoken: tok_original
endpoints:
  - name: pos-1
    upstream:
      url: localhost:8080
`

func TestEditConfigCommitsValidEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := local.Write(path, config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Description:   "original-desc",
		Credential:    config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "tok_original"}},
		Endpoints:     []config.Endpoint{{Name: "pos-1", Upstream: config.Upstream{URL: "localhost:8080"}}},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	t.Setenv("EDITOR", fakeEditor(t, writeFixture(t, validConfigYAML)))

	if err := runEditConfig([]string{path}); err != nil {
		t.Fatalf("runEditConfig: %v", err)
	}

	got, err := local.New(path).Load(t.Context())
	if err != nil {
		t.Fatalf("load result: %v", err)
	}
	if got.Description != "updated-desc" {
		t.Errorf("Description = %q, want %q", got.Description, "updated-desc")
	}
}

func TestEditConfigNoOpWhenUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Description:   "same",
		Credential:    config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "tok"}},
		Endpoints:     []config.Endpoint{{Upstream: config.Upstream{URL: "localhost:8080"}}},
	}
	if err := local.Write(path, seed); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	// The fake "editor" here is a no-op: it copies the file back onto
	// itself, so runEditConfig should detect no diff and skip writing.
	scriptPath := filepath.Join(t.TempDir(), "noop-editor.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write noop editor: %v", err)
	}
	t.Setenv("EDITOR", scriptPath)

	if err := runEditConfig([]string{path}); err != nil {
		t.Fatalf("runEditConfig: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Error("file content changed even though the editor made no edit")
	}
}

func TestEditConfigRejectsInvalidYAMLAndLeavesFileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Description:   "original",
		Credential:    config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "tok"}},
		Endpoints:     []config.Endpoint{{Upstream: config.Upstream{URL: "localhost:8080"}}},
	}
	if err := local.Write(path, seed); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Fixture is malformed YAML (bad indentation) — first attempt.
	badFixture := writeFixture(t, "schema_version: 1\n  description: [unterminated\n")
	t.Setenv("EDITOR", fakeEditor(t, badFixture))

	// Answering "n" to the retry prompt aborts instead of looping forever.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString("n\n"); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	if err := runEditConfig([]string{path}); err == nil {
		t.Fatal("expected an error for invalid YAML, got nil")
	}

	got, err := local.New(path).Load(t.Context())
	if err != nil {
		t.Fatalf("load result: %v", err)
	}
	if got.Description != "original" {
		t.Errorf("Description = %q, want file left untouched at %q", got.Description, "original")
	}
}

func TestEditConfigAbortsOnClosedStdinInsteadOfLoopingForever(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Description:   "original",
		Credential:    config.CredentialRef{Provider: "static", Params: map[string]string{"authtoken": "tok"}},
		Endpoints:     []config.Endpoint{{Upstream: config.Upstream{URL: "localhost:8080"}}},
	}
	if err := local.Write(path, seed); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	badFixture := writeFixture(t, "schema_version: 1\n  description: [unterminated\n")
	t.Setenv("EDITOR", fakeEditor(t, badFixture))

	// stdin closed immediately (EOF, no bytes at all) — simulates a
	// non-interactive invocation (e.g. over ssh with no pty). Without the
	// EOF-aborts fix, ReadString's ("", io.EOF) was read as an empty
	// line and treated as "yes, retry", which would reopen the same
	// always-invalid fixture forever.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	done := make(chan error, 1)
	go func() { done <- runEditConfig([]string{path}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for invalid YAML with closed stdin, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runEditConfig did not return — likely looping on the retry prompt")
	}
}

func TestEditConfigMissingFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	if err := runEditConfig([]string{path}); err == nil {
		t.Fatal("expected an error editing a nonexistent config, got nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("edit-config should not create a new file when the target doesn't already exist")
	}
}
