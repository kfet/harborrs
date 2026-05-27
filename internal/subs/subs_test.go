package subs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kfet/harborrs/internal/store"
)

func TestOpenMissingFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "x.opml"))
	if err != nil {
		t.Fatal(err)
	}
	if s.OPML() == nil || len(s.OPML().Feeds) != 0 {
		t.Fatalf("missing-file: got %+v", s.OPML())
	}
}

func TestOpenAndMutate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "subs.opml")
	// Seed an OPML on disk.
	seed := &store.OPML{Feeds: []store.Feed{
		{Title: "a", XMLURL: "https://a/", Tags: []string{"x"}},
	}}
	if err := seed.WriteOPML(p); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.OPML().Feeds) != 1 {
		t.Fatalf("open: %+v", s.OPML())
	}

	// Mutate should clone, apply, persist, and swap.
	live := s.OPML()
	if err := s.Mutate(func(op *store.OPML) {
		op.Feeds = append(op.Feeds, store.Feed{XMLURL: "https://b/", Title: "b"})
	}); err != nil {
		t.Fatal(err)
	}
	// Old pointer must be untouched.
	if len(live.Feeds) != 1 {
		t.Fatalf("live mutated: %d", len(live.Feeds))
	}
	if len(s.OPML().Feeds) != 2 {
		t.Fatalf("post-mutate: %+v", s.OPML())
	}
	// Reopen — disk must reflect the new state.
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.OPML().Feeds) != 2 {
		t.Fatalf("disk reload: %+v", s2.OPML())
	}
}

func TestOpenParseError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.opml")
	if err := os.WriteFile(p, []byte("not xml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestMutateWriteError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.opml")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the file with a directory at the same name to break the
	// atomic-write rename.
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	before := s.OPML()
	err = s.Mutate(func(op *store.OPML) {
		op.Feeds = append(op.Feeds, store.Feed{XMLURL: "https://x/"})
	})
	if err == nil {
		t.Fatal("expected write error")
	}
	if s.OPML() != before {
		t.Fatal("live pointer changed despite write failure")
	}
}

func TestMutateConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "subs.opml")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// 8 goroutines each add one feed; final pointer must contain 8.
	var wg sync.WaitGroup
	const N = 8
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Mutate(func(op *store.OPML) {
				op.Feeds = append(op.Feeds, store.Feed{
					XMLURL: "https://feed/" + string(rune('a'+i)),
					Title:  "f",
				})
			})
			if err != nil {
				t.Errorf("mutate: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(s.OPML().Feeds); got != N {
		t.Fatalf("final feeds=%d want %d", got, N)
	}
}

// TestOPMLImmutableContract documents that OPML() returns a pointer
// that must not be mutated — Mutate is the only sanctioned writer. We
// can't enforce this at compile time, so we exercise the documented
// invariant: two concurrent OPML() readers see consistent snapshots.
func TestOPMLImmutableContract(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "subs.opml")
	s, _ := Open(p)
	_ = s.Mutate(func(op *store.OPML) {
		op.Feeds = []store.Feed{{XMLURL: "u"}}
	})
	a := s.OPML()
	b := s.OPML()
	if a != b {
		t.Fatal("OPML() should return the same atomic pointer between mutations")
	}
	_ = s.Mutate(func(op *store.OPML) {
		op.Feeds = append(op.Feeds, store.Feed{XMLURL: "u2"})
	})
	c := s.OPML()
	if c == a {
		t.Fatal("post-Mutate pointer should differ from pre")
	}
}

// --- Test-helper constructor coverage ---

func TestNewForTestAndInPlaceMutate(t *testing.T) {
	// nil initial → empty.
	s := NewForTest(nil)
	if s.OPML() == nil || len(s.OPML().Feeds) != 0 {
		t.Fatalf("nil-init: %+v", s.OPML())
	}
	// Real init: in-place Mutate must keep the same pointer.
	o := &store.OPML{Feeds: []store.Feed{{XMLURL: "u"}}}
	s = NewForTest(o)
	before := s.OPML()
	if before != o {
		t.Fatal("NewForTest didn't pin the supplied pointer")
	}
	if err := s.Mutate(func(op *store.OPML) {
		op.Feeds = append(op.Feeds, store.Feed{XMLURL: "u2"})
	}); err != nil {
		t.Fatal(err)
	}
	if s.OPML() != before {
		t.Fatal("inPlace Mutate swapped the pointer")
	}
	if len(s.OPML().Feeds) != 2 {
		t.Fatalf("after-mutate: %+v", s.OPML())
	}
}

func TestSetWriteHookInjectsErrorAndRestores(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "subs.opml")
	s, _ := Open(p)
	boom := errors.New("boom")
	restore := s.SetWriteHook(func(*store.OPML, string) error { return boom })
	err := s.Mutate(func(op *store.OPML) {
		op.Feeds = []store.Feed{{XMLURL: "u"}}
	})
	if err != boom {
		t.Fatalf("err=%v want %v", err, boom)
	}
	restore()
	if err := s.Mutate(func(op *store.OPML) {
		op.Feeds = []store.Feed{{XMLURL: "u2"}}
	}); err != nil {
		t.Fatalf("post-restore: %v", err)
	}
}
