package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen == "" || c.UI.Theme == "" {
		t.Fatalf("bad defaults: %+v", c)
	}
}

func TestLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	c := Default()
	c.Listen = ":7000"
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":7000" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadBadJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte("{bad"), 0o644)
	if _, err := Load(p); err == nil {
		t.Fatal("expected err")
	}
}

func TestLoadOtherError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.MkdirAll(p, 0o755)
	if _, err := Load(p); err == nil {
		t.Fatal("expected err")
	}
}

func TestLoadEmptyDefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte("{}"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen == "" || c.UI.Theme == "" {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

// TestFileOPMLRoundtrip / TestFileOPMLLoadError removed: FileOPML
// moved out of config — the in-memory subscriptions.opml lives in
// internal/subs and is exercised there.

func TestSaveMarshalIsAlwaysOK(t *testing.T) {
	// MarshalIndent doesn't error on Config values; smoke-test that Save
	// surfaces atomic write errors.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blk")
	os.MkdirAll(blocker, 0o755)
	if err := Save(blocker, Default()); err == nil {
		t.Fatal("expected atomic err")
	}
}

func TestSaveMarshalError(t *testing.T) {
	orig := jsonMarshalIndent
	t.Cleanup(func() { jsonMarshalIndent = orig })
	jsonMarshalIndent = func(any, string, string) ([]byte, error) {
		return nil, &osPathError{err: "boom"}
	}
	if err := Save("/tmp/ignored", Default()); err == nil {
		t.Fatal("expected marshal err")
	}
}

type osPathError struct{ err string }

func (e *osPathError) Error() string { return e.err }

func TestLoadExplicitEmpties(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte(`{"listen":"", "ui":{"theme":""}}`), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen == "" || c.UI.Theme == "" {
		t.Fatalf("defaults not re-applied: %+v", c)
	}
}
