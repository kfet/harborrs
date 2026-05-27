package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUnreadCountersAppendAndFlag walks the per-feed unread counter
// through the full lifecycle: new-entry → ++, SetRead(true) → --,
// SetRead(false) → ++, drained → map cleared.
func TestUnreadCountersAppendAndFlag(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fh := "feed"
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	es := []Entry{
		{GUID: "a", Published: t0, FetchedAt: t0},
		{GUID: "b", Published: t1, FetchedAt: t1},
	}
	added, err := s.AppendEntries(fh, es)
	if err != nil || len(added) != 2 {
		t.Fatalf("append: %v added=%d", err, len(added))
	}
	count, newest := s.UnreadCount(fh)
	if count != 2 {
		t.Fatalf("count=%d want 2", count)
	}
	if newest != t1.UnixMicro() {
		t.Fatalf("newest=%d want %d", newest, t1.UnixMicro())
	}
	// Mark b read → counter --, newest collapses back to a.
	hb := added[1].Hash
	ha := added[0].Hash
	if err := s.SetRead(hb, true); err != nil {
		t.Fatal(err)
	}
	count, newest = s.UnreadCount(fh)
	if count != 1 || newest != t0.UnixMicro() {
		t.Fatalf("after-b-read: count=%d newest=%d want 1/%d", count, newest, t0.UnixMicro())
	}
	// Idempotent: re-mark read changes nothing.
	if err := s.SetRead(hb, true); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.UnreadCount(fh); c != 1 {
		t.Fatalf("idempotent set-read changed count: %d", c)
	}
	// Mark a read → 0, map drained.
	if err := s.SetRead(ha, true); err != nil {
		t.Fatal(err)
	}
	count, newest = s.UnreadCount(fh)
	if count != 0 || newest != 0 {
		t.Fatalf("drained: count=%d newest=%d", count, newest)
	}
	// Mark a unread again → ++.
	if err := s.SetRead(ha, false); err != nil {
		t.Fatal(err)
	}
	count, newest = s.UnreadCount(fh)
	if count != 1 || newest != t0.UnixMicro() {
		t.Fatalf("re-unread: count=%d newest=%d", count, newest)
	}
}

// TestRecountUnreadAtOpen ensures Open rebuilds the unread counter
// from the on-disk ndjson + read.log fold.
func TestRecountUnreadAtOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fh := "f"
	t0 := time.Now().UTC()
	added, _ := s.AppendEntries(fh, []Entry{
		{GUID: "a", Published: t0, FetchedAt: t0},
		{GUID: "b", Published: t0, FetchedAt: t0},
	})
	_ = s.SetRead(added[0].Hash, true)
	// Reopen.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	count, _ := s2.UnreadCount(fh)
	if count != 1 {
		t.Fatalf("recount: count=%d want 1", count)
	}
}

// TestAppendEntriesConcurrentDedup spins up many goroutines all trying
// to append overlapping entries; the byHash index must keep each hash
// unique and unreadN must not be double-counted. Run enough goroutines
// that the write-lock re-check branch (RLock snapshot → racing Lock
// commits) reliably fires.
func TestAppendEntriesConcurrentDedup(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	now := time.Now().UTC()
	es := make([]Entry, 20)
	for i := range es {
		es[i] = Entry{GUID: string(rune('a' + i)), Published: now, FetchedAt: now}
	}
	const goroutines = 32
	done := make(chan struct{}, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			_, _ = s.AppendEntries(fh, es)
			done <- struct{}{}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
	if count, _ := s.UnreadCount(fh); count != len(es) {
		t.Fatalf("count=%d want %d", count, len(es))
	}
	if got := len(s.IndexedEntries(fh)); got != len(es) {
		t.Fatalf("indexed=%d want %d", got, len(es))
	}
}

// TestBuildIndexDedupOnDisk seeds entries/<fh>/current.ndjson with two
// records that share the same hash (legacy bug shape) and asserts the
// index keeps only the first.
func TestBuildIndexDedupOnDisk(t *testing.T) {
	dir := t.TempDir()
	fh := "feed"
	feedDir := filepath.Join(dir, "entries", fh)
	if err := os.MkdirAll(feedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := Entry{Hash: EntryHash("g", ""), GUID: "g", FeedHash: fh, Published: now, FetchedAt: now}
	line, _ := jsonMarshal(e)
	line = append(line, '\n')
	// Two identical lines.
	if err := os.WriteFile(filepath.Join(feedDir, "current.ndjson"), append(append([]byte(nil), line...), line...), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.IndexedEntries(fh)); got != 1 {
		t.Fatalf("dedup failed: indexed=%d want 1", got)
	}
}
