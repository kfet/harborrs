package config

import (
	"strconv"
	"testing"

	"github.com/kfet/harborrs/internal/store"
)

// BenchmarkFileOPMLLoad measures the per-Load cost. Pre-rework
// this was a fresh disk read + XML parse on every call. Post-rework
// it should be a deep copy of the in-mem slice only.
func BenchmarkFileOPMLLoad(b *testing.B) {
	dir := b.TempDir()
	f := NewFileOPML(dir)
	feeds := make([]store.Feed, 60)
	for i := range feeds {
		feeds[i] = store.Feed{
			XMLURL:  "https://example.com/feed/" + strconv.Itoa(i),
			Title:   "Feed " + strconv.Itoa(i),
			HTMLURL: "https://example.com/site/" + strconv.Itoa(i),
			Tags:    []string{"tag-a", "tag-b"},
		}
	}
	if err := f.Save(&store.OPML{Feeds: feeds}); err != nil {
		b.Fatal(err)
	}
	// Populate in-mem state so we measure steady-state Load cost.
	if _, err := f.Load(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o, err := f.Load()
		if err != nil {
			b.Fatal(err)
		}
		if len(o.Feeds) != 60 {
			b.Fatal("short load")
		}
	}
}

// BenchmarkFileOPMLLoadColdDisk simulates the pre-rework behaviour by
// invalidating the in-mem snapshot before every Load. This is the cost
// each request was paying before the in-mem authoritative rework.
func BenchmarkFileOPMLLoadColdDisk(b *testing.B) {
	dir := b.TempDir()
	f := NewFileOPML(dir)
	feeds := make([]store.Feed, 60)
	for i := range feeds {
		feeds[i] = store.Feed{
			XMLURL:  "https://example.com/feed/" + strconv.Itoa(i),
			Title:   "Feed " + strconv.Itoa(i),
			HTMLURL: "https://example.com/site/" + strconv.Itoa(i),
			Tags:    []string{"tag-a", "tag-b"},
		}
	}
	if err := f.Save(&store.OPML{Feeds: feeds}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.mu.Lock()
		f.cur = nil
		f.mu.Unlock()
		o, err := f.Load()
		if err != nil {
			b.Fatal(err)
		}
		if len(o.Feeds) != 60 {
			b.Fatal("short load")
		}
	}
}
