package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/harborrs/internal/store"
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

func TestFileOPMLRoundtrip(t *testing.T) {
	dir := t.TempDir()
	f := NewFileOPML(dir)
	o, err := f.Load()
	if err != nil || len(o.Feeds) != 0 {
		t.Fatalf("empty load: %+v err=%v", o, err)
	}
	o.Feeds = []store.Feed{{XMLURL: "https://x/feed", Title: "X"}}
	if err := f.Save(o); err != nil {
		t.Fatal(err)
	}
	o2, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(o2.Feeds) != 1 {
		t.Fatalf("feeds=%d", len(o2.Feeds))
	}
}

func TestFileOPMLLoadError(t *testing.T) {
	dir := t.TempDir()
	f := &FileOPML{Path: filepath.Join(dir, "sub.opml")}
	// Make path a directory → ReadOPML returns EISDIR error.
	os.MkdirAll(f.Path, 0o755)
	if _, err := f.Load(); err == nil {
		t.Fatal("expected err")
	}
}

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
