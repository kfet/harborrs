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

// TestFileOPMLLoadFromExistingFile exercises the disk-read success
// path of ensureLoaded: a valid subscriptions.opml already exists when
// the first Load is called, and that data must come back unchanged.
func TestFileOPMLLoadFromExistingFile(t *testing.T) {
	dir := t.TempDir()
	// Write a real OPML to disk via a separate FileOPML, then construct
	// a fresh one over the same path so the first Load actually reads
	// the file (rather than serving an already-populated in-mem state).
	seed := &store.OPML{Feeds: []store.Feed{
		{XMLURL: "https://a.example/feed", Title: "A", Tags: []string{"x"}},
		{XMLURL: "https://b.example/feed", Title: "B"},
	}}
	if err := NewFileOPML(dir).Save(seed); err != nil {
		t.Fatal(err)
	}
	f := NewFileOPML(dir)
	o, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Feeds) != 2 {
		t.Fatalf("feeds=%d, want 2; got %+v", len(o.Feeds), o.Feeds)
	}
	if o.Feeds[0].XMLURL != "https://a.example/feed" || o.Feeds[0].Tags[0] != "x" {
		t.Errorf("feed[0]=%+v", o.Feeds[0])
	}
}

// TestFileOPMLLoadError tests the non-NotExist error surface.
func TestFileOPMLLoadError(t *testing.T) {
	dir := t.TempDir()
	f := &FileOPML{Path: filepath.Join(dir, "sub.opml")}
	// Make path a directory → ReadOPML returns EISDIR error.
	os.MkdirAll(f.Path, 0o755)
	if _, err := f.Load(); err == nil {
		t.Fatal("expected err")
	}
}

// TestFileOPMLReadsOnce asserts the disk file is read exactly once
// across many Load calls: after loading once via the first Load,
// removing the on-disk file must NOT affect subsequent Loads.
func TestFileOPMLReadsOnce(t *testing.T) {
	dir := t.TempDir()
	f := NewFileOPML(dir)
	seed := &store.OPML{Feeds: []store.Feed{{XMLURL: "https://x/feed", Title: "X", Tags: []string{"t"}}}}
	if err := f.Save(seed); err != nil {
		t.Fatal(err)
	}
	// Populate in-mem state via first Load.
	if _, err := f.Load(); err != nil {
		t.Fatal(err)
	}
	// Yank the file from underneath; in-mem must still serve.
	if err := os.Remove(f.Path); err != nil {
		t.Fatal(err)
	}
	o, err := f.Load()
	if err != nil {
		t.Fatalf("Load after file removal: %v (must serve from memory)", err)
	}
	if len(o.Feeds) != 1 || o.Feeds[0].XMLURL != "https://x/feed" {
		t.Fatalf("in-mem load returned %+v, want seeded feed", o.Feeds)
	}
}

// TestFileOPMLLoadIsolation asserts Load returns a defensive deep copy:
// mutations to the returned value must not leak into the in-mem state or
// subsequent Loads.
func TestFileOPMLLoadIsolation(t *testing.T) {
	dir := t.TempDir()
	f := NewFileOPML(dir)
	if err := f.Save(&store.OPML{Feeds: []store.Feed{{XMLURL: "https://x", Tags: []string{"a"}}}}); err != nil {
		t.Fatal(err)
	}
	a, _ := f.Load()
	a.Feeds[0].XMLURL = "MUTATED"
	a.Feeds[0].Tags[0] = "MUTATED"
	a.Feeds = append(a.Feeds, store.Feed{XMLURL: "added"})
	b, _ := f.Load()
	if b.Feeds[0].XMLURL != "https://x" {
		t.Errorf("XMLURL mutation leaked: %q", b.Feeds[0].XMLURL)
	}
	if b.Feeds[0].Tags[0] != "a" {
		t.Errorf("Tags mutation leaked: %v", b.Feeds[0].Tags)
	}
	if len(b.Feeds) != 1 {
		t.Errorf("Feeds slice append leaked: %d entries", len(b.Feeds))
	}
}

// TestFileOPMLSaveFailureKeepsState asserts a failed disk write leaves
// the in-memory state untouched (no torn / partial state). Failure is
// induced by making the target path a directory so the atomic rename
// inside WriteOPML fails; if atomic.WriteFileMode ever changes semantics
// this trigger may need revisiting.
func TestFileOPMLSaveFailureKeepsState(t *testing.T) {
	dir := t.TempDir()
	f := NewFileOPML(dir)
	good := &store.OPML{Feeds: []store.Feed{{XMLURL: "good", Title: "G"}}}
	if err := f.Save(good); err != nil {
		t.Fatal(err)
	}
	// Replace the file path with a directory to force WriteOPML to fail.
	os.Remove(f.Path)
	if err := os.MkdirAll(f.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := &store.OPML{Feeds: []store.Feed{{XMLURL: "bad"}}}
	if err := f.Save(bad); err == nil {
		t.Fatal("expected Save to fail with path-is-dir")
	}
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Feeds) != 1 || got.Feeds[0].XMLURL != "good" {
		t.Errorf("in-mem state corrupted by failed Save: %+v", got.Feeds)
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
