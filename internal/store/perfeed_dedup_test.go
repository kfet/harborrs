package store

import (
	"testing"
	"time"
)

// TestAppendEntriesDedupIsPerFeed asserts that two distinct feeds may
// each independently carry an entry whose hash collides with one from
// another feed (e.g. same GUID+link in a shared-content syndication).
// The pre-refactor `knownHashes(feedHash)` scanned only the feed's own
// dir; the post-refactor dedup must keep the same scope.
func TestAppendEntriesDedupIsPerFeed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Same GUID+link → same EntryHash across two different feeds.
	e := Entry{GUID: "shared", Link: "https://x/", Published: now, FetchedAt: now}

	addedA, err := s.AppendEntries("feed-a", []Entry{e})
	if err != nil || len(addedA) != 1 {
		t.Fatalf("feed-a: added=%d err=%v", len(addedA), err)
	}
	addedB, err := s.AppendEntries("feed-b", []Entry{e})
	if err != nil {
		t.Fatalf("feed-b: %v", err)
	}
	if len(addedB) != 1 {
		t.Fatalf("feed-b: added=%d, want 1 (dedup must be per-feed)", len(addedB))
	}
	if got := len(s.IndexedEntries("feed-a")); got != 1 {
		t.Fatalf("feed-a indexed=%d", got)
	}
	if got := len(s.IndexedEntries("feed-b")); got != 1 {
		t.Fatalf("feed-b indexed=%d", got)
	}
}
