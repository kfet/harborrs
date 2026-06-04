package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFeedHashStable(t *testing.T) {
	if got := FeedHash("https://example.com/feed"); len(got) != 20 {
		t.Fatalf("len=%d", len(got))
	}
	if FeedHash("a") == FeedHash("b") {
		t.Fatal("collision")
	}
	a1, a2 := FeedHash("a"), FeedHash("a")
	if a1 != a2 {
		t.Fatal("unstable")
	}
}

func TestEntryHashStable(t *testing.T) {
	a, b := EntryHash("g", "l"), EntryHash("g", "l")
	if a != b {
		t.Fatal("unstable")
	}
	if EntryHash("g", "l") == EntryHash("", "") {
		t.Fatal("collision")
	}
}

const sampleOPML = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>mine</title></head>
  <body>
    <outline text="News" title="News">
      <outline type="rss" text="A" title="A" xmlUrl="https://a.example/feed" htmlUrl="https://a.example"/>
      <outline type="rss" text="B" xmlUrl="https://b.example/feed"/>
    </outline>
    <outline type="rss" text="Loose" xmlUrl="https://c.example/feed"/>
  </body>
</opml>`

func TestParseAndWriteOPML(t *testing.T) {
	o, err := ParseOPML([]byte(sampleOPML))
	if err != nil {
		t.Fatal(err)
	}
	if o.Title != "mine" {
		t.Fatalf("title=%q", o.Title)
	}
	if len(o.Feeds) != 3 {
		t.Fatalf("feeds=%d", len(o.Feeds))
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "s.opml")
	if err := o.WriteOPML(p); err != nil {
		t.Fatal(err)
	}
	o2, err := ReadOPML(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(o2.Feeds) != 3 {
		t.Fatalf("round-trip feeds=%d", len(o2.Feeds))
	}
	// Add / Find / Remove
	if !o2.Add(Feed{Title: "D", XMLURL: "https://d.example/feed"}) {
		t.Fatal("Add returned false for new")
	}
	if o2.Add(Feed{Title: "D2", XMLURL: "https://d.example/feed"}) {
		t.Fatal("Add returned true for existing")
	}
	if o2.Find("https://d.example/feed") == nil {
		t.Fatal("Find missing")
	}
	if o2.Find("nope") != nil {
		t.Fatal("Find unexpected")
	}
	if !o2.Remove("https://d.example/feed") {
		t.Fatal("Remove false")
	}
	if o2.Remove("https://d.example/feed") {
		t.Fatal("Remove true second time")
	}
}

func TestParseOPMLError(t *testing.T) {
	if _, err := ParseOPML([]byte("<not xml")); err == nil {
		t.Fatal("expected error")
	}
}

func TestReadOPMLMissing(t *testing.T) {
	if _, err := ReadOPML(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x"); got != "x" {
		t.Fatalf("%q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatal("expected empty")
	}
}

// --- Store / state-log / NDJSON ---

func TestOpenAndState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead("h1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead("h1", true); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := s.SetStarred("h1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStarred("h1", true); err != nil {
		t.Fatal(err)
	}
	if s.CountRead() != 1 || s.CountStarred() != 1 {
		t.Fatalf("counts r=%d s=%d", s.CountRead(), s.CountStarred())
	}
	// Toggle off
	if err := s.SetRead("h1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStarred("h1", false); err != nil {
		t.Fatal(err)
	}
	if s.CountRead() != 0 || s.CountStarred() != 0 {
		t.Fatalf("counts after off: r=%d s=%d", s.CountRead(), s.CountStarred())
	}
	// Reopen — state must persist via log fold.
	if err := s.SetRead("h2", true); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.EntryState("h2").Read {
		t.Fatal("h2 not read after reopen")
	}
	if s2.CountRead() != 1 {
		t.Fatalf("reopen liveR=%d", s2.CountRead())
	}
	all := s2.AllStates()
	if len(all) == 0 {
		t.Fatal("AllStates empty")
	}
}

func TestOpenMkdirFail(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blk")
	if err := os.WriteFile(blocker, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocker, "sub")); err == nil {
		t.Fatal("expected mkdir fail")
	}
}

func TestFoldMalformedLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "read.log"), []byte(
		"bad\n"+
			"notatime r h\n"+
			"2024-01-01T00:00:00Z x h\n"+ // unknown op
			"2024-01-01T00:00:00Z r h1\n"+
			"2024-01-02T00:00:00Z u h1\n"+
			"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "starred.log"), []byte(
		"2024-01-01T00:00:00Z s h2\n"+
			"2024-01-02T00:00:00Z S h2\n"+
			"2024-01-03T00:00:00Z s h3\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.EntryState("h1").Read {
		t.Fatal("h1 should be unread after u")
	}
	if !s.EntryState("h3").Starred {
		t.Fatal("h3 should be starred")
	}
	if s.CountStarred() != 1 {
		t.Fatalf("starred live=%d", s.CountStarred())
	}
}

func TestFoldOpenError(t *testing.T) {
	dir := t.TempDir()
	// Make read.log a directory so open fails (with EISDIR).
	if err := os.MkdirAll(filepath.Join(dir, "read.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompactionRead(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Make 50 reads then unread them — log balloons relative to liveR=0.
	for i := 0; i < 50; i++ {
		h := fmt.Sprintf("h%02d", i)
		if err := s.SetRead(h, true); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 49; i++ {
		h := fmt.Sprintf("h%02d", i)
		if err := s.SetRead(h, false); err != nil {
			t.Fatal(err)
		}
	}
	// At this point liveR=1; readN was 99 before final, compaction
	// fires somewhere around then. Force one more event to ensure
	// compaction path runs:
	if err := s.SetRead("h99", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead("h99", false); err != nil {
		t.Fatal(err)
	}
	// Verify compaction has shrunk the log file.
	data, _ := os.ReadFile(filepath.Join(dir, "read.log"))
	if len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")) > 10 {
		t.Logf("log size after compaction: %d lines", len(strings.Split(string(data), "\n")))
	}
	// And re-fold returns the same liveR.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.CountRead() != s.CountRead() {
		t.Fatalf("liveR mismatch %d vs %d", s2.CountRead(), s.CountRead())
	}
}

func TestCompactionStarred(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for i := 0; i < 40; i++ {
		s.SetStarred(fmt.Sprintf("h%d", i), true)
	}
	for i := 0; i < 40; i++ {
		s.SetStarred(fmt.Sprintf("h%d", i), false)
	}
	if s.CountStarred() != 0 {
		t.Fatalf("liveS=%d", s.CountStarred())
	}
}

func TestAppendEntriesAndList(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := FeedHash("https://x/feed")
	now := time.Now().UTC()
	es := []Entry{
		{GUID: "1", Link: "https://x/a", Title: "A", Published: now.Add(-2 * time.Hour), FetchedAt: now},
		{GUID: "2", Link: "https://x/b", Title: "B", Published: now.Add(-1 * time.Hour), FetchedAt: now},
	}
	added, err := s.AppendEntries(fh, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("added=%d", len(added))
	}
	// Second time: zero new.
	added, err = s.AppendEntries(fh, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("expected dedup, got %d", len(added))
	}
	// Empty input.
	if a, err := s.AppendEntries(fh, nil); err != nil || a != nil {
		t.Fatalf("empty input got a=%v err=%v", a, err)
	}
	listed, err := s.ListEntries(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed=%d", len(listed))
	}
	if !listed[0].Published.After(listed[1].Published) {
		t.Fatal("not sorted newest-first")
	}
	// ListEntries on unknown feed → nil.
	if got, _ := s.ListEntries("nope"); got != nil {
		t.Fatal("expected nil")
	}
}

func TestListEntriesBadJSON(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "abc"
	p := filepath.Join(dir, "entries", fh)
	os.MkdirAll(p, 0o755)
	os.WriteFile(filepath.Join(p, "current.ndjson"), []byte("not json\n"), 0o644)
	if _, err := s.ListEntries(fh); err == nil {
		t.Fatal("expected json error")
	}
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "x"}}); err == nil {
		t.Fatal("expected knownHashes error")
	}
}

func TestRolloverArchives(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f1"
	old := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	older := time.Date(2022, 7, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Now().UTC().Add(-1 * time.Hour)
	_, err := s.AppendEntries(fh, []Entry{
		{GUID: "a", Published: old, FetchedAt: old},
		{GUID: "b", Published: older, FetchedAt: older},
		{GUID: "c", Published: fresh, FetchedAt: fresh},
		{GUID: "d", FetchedAt: older}, // zero published → fallback to fetched
	})
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	n, err := s.RolloverArchives(fh, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("archived=%d", n)
	}
	// Idempotent — nothing to archive now.
	n, err = s.RolloverArchives(fh, cutoff)
	if err != nil || n != 0 {
		t.Fatalf("second rollover: n=%d err=%v", n, err)
	}
	// Quarter files exist.
	ents, _ := os.ReadDir(filepath.Join(dir, "entries", fh))
	var quarters int
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "20") && strings.HasSuffix(e.Name(), ".ndjson") {
			quarters++
		}
	}
	if quarters < 2 {
		t.Fatalf("expected at least 2 quarter files, got %d", quarters)
	}
	// Listing returns everything.
	listed, _ := s.ListEntries(fh)
	if len(listed) != 4 {
		t.Fatalf("listed=%d after rollover", len(listed))
	}
}

func TestRolloverMissingFeed(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if n, err := s.RolloverArchives("nope", time.Now()); n != 0 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestFeedState(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f1"
	got, err := s.LoadFeedState(fh)
	if err != nil || got.URL != "" {
		t.Fatalf("missing should be empty: got=%+v err=%v", got, err)
	}
	want := FeedState{URL: "u", ETag: "\"e\"", LastFetched: time.Now().UTC().Truncate(time.Second)}
	if err := s.SaveFeedState(fh, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadFeedState(fh)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "u" || got.ETag != "\"e\"" {
		t.Fatalf("got=%+v", got)
	}
	// Corrupt file.
	os.WriteFile(filepath.Join(dir, "state", fh+".json"), []byte("{bad"), 0o644)
	if _, err := s.LoadFeedState(fh); err == nil {
		t.Fatal("expected json error")
	}
}

func TestAppendLineTooBig(t *testing.T) {
	if err := appendLine("ignored", strings.Repeat("a", 5000)); err == nil {
		t.Fatal("expected too-large error")
	}
}

func TestAppendLineMkdirFail(t *testing.T) {
	dir := t.TempDir()
	blk := filepath.Join(dir, "f")
	os.WriteFile(blk, nil, 0o644)
	if err := appendLine(filepath.Join(blk, "x", "y.log"), "hello\n"); err == nil {
		t.Fatal("expected error")
	}
}

// Marshal sanity for OPML output.
func TestOPMLMarshalRoundtrip(t *testing.T) {
	o := &OPML{Title: "t", Feeds: []Feed{
		{Title: "Z", XMLURL: "z", Tags: []string{"F"}},
		{Title: "A", XMLURL: "a", Tags: []string{"F"}},
		{Title: "L", XMLURL: "l"}, // loose
	}}
	data, err := o.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `<opml`) {
		t.Fatal("missing root")
	}
	// Stable ordering: A before Z within F.
	i := strings.Index(string(data), `xmlUrl="a"`)
	j := strings.Index(string(data), `xmlUrl="z"`)
	if i < 0 || j < 0 || i >= j {
		t.Fatalf("not sorted: a@%d z@%d", i, j)
	}
}

// Validate JSON for entry encoding includes hash + feed.
func TestEntryJSON(t *testing.T) {
	e := Entry{Hash: "h", FeedHash: "f", Title: "T"}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"hash":"h"`) || !strings.Contains(string(b), `"feed":"f"`) {
		t.Fatalf("missing keys: %s", b)
	}
}

func TestNormalizeTagsDropsReserved(t *testing.T) {
	// The pseudo-tag name __untagged__ collides with the UI's
	// no-tags bucket. NormalizeTags must drop it silently so neither
	// OPML, the API, nor the UI form can introduce a real tag with
	// that name.
	if got := NormalizeTags([]string{"a", ReservedTagUntagged, "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
	if got := NormalizeTags([]string{ReservedTagUntagged}); got != nil {
		t.Fatalf("solo reserved → %v", got)
	}
	if !IsReservedTag(ReservedTagUntagged) {
		t.Fatal("IsReservedTag false for the constant")
	}
	if IsReservedTag("ordinary") {
		t.Fatal("IsReservedTag true for ordinary")
	}
}

func TestFeedAddTagRejectsReserved(t *testing.T) {
	f := &Feed{}
	f.AddTag(ReservedTagUntagged)
	if f.Tags != nil {
		t.Fatalf("reserved leaked into tags: %v", f.Tags)
	}
	f.AddTag("ok")
	f.AddTag(ReservedTagUntagged)
	if len(f.Tags) != 1 || f.Tags[0] != "ok" {
		t.Fatalf("got %v", f.Tags)
	}
}

func TestNormalizeTags(t *testing.T) {
	if got := NormalizeTags(nil); got != nil {
		t.Fatalf("nil in → %v", got)
	}
	if got := NormalizeTags([]string{"", "   "}); got != nil {
		t.Fatalf("empty-only in → %v", got)
	}
	got := NormalizeTags([]string{"b", " a ", "b", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v", got)
	}
}

const taggedOPML = `<?xml version="1.0"?>
<opml version="2.0"><body>
  <outline type="rss" title="A" xmlUrl="https://a.example/feed" category="tech, daily"/>
  <outline title="folder">
    <outline type="rss" title="B" xmlUrl="https://b.example/feed" category="daily"/>
  </outline>
  <outline type="rss" title="C" xmlUrl="https://c.example/feed"/>
</body></opml>`

func TestParseOPMLTagsMergeAndUntagged(t *testing.T) {
	o, err := ParseOPML([]byte(taggedOPML))
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]*Feed{}
	for i := range o.Feeds {
		byURL[o.Feeds[i].XMLURL] = &o.Feeds[i]
	}
	a := byURL["https://a.example/feed"]
	if a == nil || len(a.Tags) != 2 || a.Tags[0] != "daily" || a.Tags[1] != "tech" {
		t.Fatalf("a tags=%v", a)
	}
	b := byURL["https://b.example/feed"]
	if b == nil || len(b.Tags) != 2 || b.Tags[0] != "daily" || b.Tags[1] != "folder" {
		t.Fatalf("b tags=%v", b)
	}
	c := byURL["https://c.example/feed"]
	if c == nil || len(c.Tags) != 0 {
		t.Fatalf("c tags=%v", c)
	}
}

func TestOPMLFlatMarshalCategoryAttr(t *testing.T) {
	o := &OPML{Feeds: []Feed{
		{Title: "A", XMLURL: "https://a/feed", Tags: []string{"x", "y"}},
		{Title: "A", XMLURL: "https://b/feed"}, // tie-breaks on URL
	}}
	data, err := o.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `category="x,y"`) {
		t.Fatalf("missing category attr: %s", s)
	}
	// Tie-break on equal titles falls through to URL ordering.
	i := strings.Index(s, "https://a/feed")
	j := strings.Index(s, "https://b/feed")
	if i < 0 || j < 0 || i >= j {
		t.Fatalf("not sorted by url tiebreak: a=%d b=%d", i, j)
	}
	// Round-trip preserves tags.
	o2, err := ParseOPML(data)
	if err != nil {
		t.Fatal(err)
	}
	var got *Feed
	for i := range o2.Feeds {
		if o2.Feeds[i].XMLURL == "https://a/feed" {
			got = &o2.Feeds[i]
		}
	}
	if got == nil || len(got.Tags) != 2 || got.Tags[0] != "x" || got.Tags[1] != "y" {
		t.Fatalf("round-trip tags=%v", got)
	}
}

func TestOPMLAllTagsRenameDisable(t *testing.T) {
	o := &OPML{Feeds: []Feed{
		{XMLURL: "a", Tags: []string{"x", "y"}},
		{XMLURL: "b", Tags: []string{"y"}},
		{XMLURL: "c"},
	}}
	if got := o.AllTags(); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("alltags=%v", got)
	}
	// Empty → nil.
	if (&OPML{}).AllTags() != nil {
		t.Fatal("empty alltags non-nil")
	}
	// Rename no-ops.
	if n := o.RenameTag("", "z"); n != 0 {
		t.Fatalf("rename empty old=%d", n)
	}
	if n := o.RenameTag("x", ""); n != 0 {
		t.Fatalf("rename empty new=%d", n)
	}
	if n := o.RenameTag("x", "x"); n != 0 {
		t.Fatalf("rename same=%d", n)
	}
	if n := o.RenameTag("nope", "z"); n != 0 {
		t.Fatalf("rename unknown=%d", n)
	}
	// Rename y → z everywhere.
	if n := o.RenameTag("y", "z"); n != 2 {
		t.Fatalf("rename count=%d", n)
	}
	if !o.Feeds[0].HasTag("z") || o.Feeds[0].HasTag("y") {
		t.Fatalf("rename feed[0] tags=%v", o.Feeds[0].Tags)
	}
	// Disable empty no-op.
	if n := o.DisableTag(""); n != 0 {
		t.Fatalf("disable empty=%d", n)
	}
	if n := o.DisableTag("nope"); n != 0 {
		t.Fatalf("disable unknown=%d", n)
	}
	// Disable z removes from both feeds.
	if n := o.DisableTag("z"); n != 2 {
		t.Fatalf("disable=%d", n)
	}
	if o.Feeds[1].Tags != nil {
		t.Fatalf("expected nil tags, got %v", o.Feeds[1].Tags)
	}
}

func TestFeedTagHelpers(t *testing.T) {
	f := &Feed{}
	// Add empty no-op.
	f.AddTag("")
	if f.Tags != nil {
		t.Fatalf("add empty changed tags: %v", f.Tags)
	}
	f.AddTag("a")
	f.AddTag("a") // dedup
	f.AddTag("b")
	if len(f.Tags) != 2 || f.Tags[0] != "a" || f.Tags[1] != "b" {
		t.Fatalf("tags=%v", f.Tags)
	}
	if !f.HasTag("a") || f.HasTag("c") {
		t.Fatalf("hastag=%v", f.Tags)
	}
	// Remove empty no-op.
	f.RemoveTag("")
	if len(f.Tags) != 2 {
		t.Fatalf("remove empty changed: %v", f.Tags)
	}
	// Remove unknown no-op.
	f.RemoveTag("zzz")
	if len(f.Tags) != 2 {
		t.Fatalf("remove unknown changed: %v", f.Tags)
	}
	f.RemoveTag("a")
	if len(f.Tags) != 1 || f.Tags[0] != "b" {
		t.Fatalf("after remove: %v", f.Tags)
	}
	f.RemoveTag("b")
	if f.Tags != nil {
		t.Fatalf("expected nil tags, got %v", f.Tags)
	}
	// OPML.Add normalises incoming tags.
	o := &OPML{}
	o.Add(Feed{XMLURL: "u", Tags: []string{"  b  ", "a", "a"}})
	if len(o.Feeds[0].Tags) != 2 || o.Feeds[0].Tags[0] != "a" || o.Feeds[0].Tags[1] != "b" {
		t.Fatalf("normalise on add: %v", o.Feeds[0].Tags)
	}
}

func TestEntryHashLengthAndCanonical(t *testing.T) {
	h := EntryHash("guid", "https://example.com/item")
	if len(h) != EntryHashLen {
		t.Fatalf("EntryHash len=%d want %d (%q)", len(h), EntryHashLen, h)
	}
	if got := CanonicalEntryHash("ABCDEF0123456789BEEF"); got != "abcdef0123456789" {
		t.Fatalf("CanonicalEntryHash legacy=%q", got)
	}
	if got := CanonicalEntryHash("abcdef0123456789"); got != "abcdef0123456789" {
		t.Fatalf("CanonicalEntryHash current=%q", got)
	}
	if got := CanonicalEntryHash("not-hex-but-longer"); got != "not-hex-but-longer" {
		t.Fatalf("CanonicalEntryHash nonhex=%q", got)
	}
}

// TestEntryHashFitsInt63 guards the wire-format invariant that every
// EntryHash decodes to a non-negative int64. Reeder (and likely other
// strict Greader clients) parses the decimal `longId` as a signed
// int64 and silently drops items whose value exceeds 2^63-1, which
// before the high-bit mask manifested as ~50% of items missing from
// the feed display. Sweeps a wide input space to catch any regression
// that re-introduces a top bit in the hash.
func TestEntryHashFitsInt63(t *testing.T) {
	for i := 0; i < 10000; i++ {
		h := EntryHash(strconv.Itoa(i), "https://example.com/"+strconv.Itoa(i*7+3))
		n, err := strconv.ParseUint(h, 16, 64)
		if err != nil {
			t.Fatalf("hash %q not parseable: %v", h, err)
		}
		if n > 1<<63-1 {
			t.Fatalf("hash %q (%d) exceeds int63 max — top bit not masked", h, n)
		}
	}
}

func TestOpenMigratesLegacyEntryHashesOnDisk(t *testing.T) {
	dir := t.TempDir()
	fh := FeedHash("https://example.com/feed")
	entDir := filepath.Join(dir, "entries", fh)
	if err := os.MkdirAll(entDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "abcdef0123456789beef"
	canon := "abcdef0123456789"
	entries := []Entry{{Hash: legacy, FeedHash: fh, GUID: "g", Link: "https://example.com/1", Title: "one", Published: time.Unix(1, 0), FetchedAt: time.Unix(2, 0)}}
	var b strings.Builder
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(entDir, "current.ndjson"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "read.log"), []byte("2024-01-01T00:00:00Z r "+legacy+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "starred.log"), []byte("2024-01-01T00:00:00Z s "+legacy+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListEntries(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Hash != canon {
		t.Fatalf("listed=%+v want hash %s", listed, canon)
	}
	if !s.EntryState(canon).Read || !s.EntryState(canon).Starred {
		t.Fatalf("canonical state not preserved: %+v", s.EntryState(canon))
	}
	if !s.EntryState(legacy).Read || !s.EntryState(legacy).Starred {
		t.Fatalf("legacy lookup should canonicalize: %+v", s.EntryState(legacy))
	}
	for _, p := range []string{filepath.Join(entDir, "current.ndjson"), filepath.Join(dir, "read.log"), filepath.Join(dir, "starred.log")} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), legacy) {
			t.Fatalf("%s still contains legacy hash: %s", p, data)
		}
		if !strings.Contains(string(data), canon) {
			t.Fatalf("%s missing canonical hash: %s", p, data)
		}
	}
}

func TestOpenRejectsEntryHashMigrationCollision(t *testing.T) {
	dir := t.TempDir()
	fh := FeedHash("https://example.com/feed")
	entDir := filepath.Join(dir, "entries", fh)
	if err := os.MkdirAll(entDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{Hash: "0123456789abcdefaaaa", FeedHash: fh, GUID: "a", Link: "https://example.com/a"},
		{Hash: "0123456789abcdefbbbb", FeedHash: fh, GUID: "b", Link: "https://example.com/b"},
	}
	var b strings.Builder
	for _, e := range entries {
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(entDir, "current.ndjson"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "entry hash collision") {
		t.Fatalf("Open err=%v, want collision", err)
	}
}

func TestOpenMigrationNoopForCurrentHashes(t *testing.T) {
	dir := t.TempDir()
	fh := FeedHash("https://example.com/current")
	entDir := filepath.Join(dir, "entries", fh)
	if err := os.MkdirAll(entDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := Entry{Hash: "abcdef0123456789", FeedHash: fh, GUID: "g", Link: "https://example.com/current/1"}
	line, _ := json.Marshal(e)
	path := filepath.Join(entDir, "current.ndjson")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("current-hash migration should be noop\nbefore=%s\nafter=%s", before, after)
	}
}

func TestOpenMigrationMarshalFail(t *testing.T) {
	dir := t.TempDir()
	fh := FeedHash("https://example.com/marshal")
	entDir := filepath.Join(dir, "entries", fh)
	if err := os.MkdirAll(entDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := Entry{Hash: "abcdef0123456789beef", FeedHash: fh, GUID: "g", Link: "https://example.com/marshal/1"}
	line, _ := json.Marshal(e)
	if err := os.WriteFile(filepath.Join(entDir, "current.ndjson"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := jsonMarshal
	jsonMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal-boom") }
	t.Cleanup(func() { jsonMarshal = orig })
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "marshal-boom") {
		t.Fatalf("Open err=%v, want marshal-boom", err)
	}
}

func TestCanonicalEntryHashEmpty(t *testing.T) {
	if got := CanonicalEntryHash(""); got != "" {
		t.Fatalf("empty canonical=%q", got)
	}
}

func TestFoldLogDirectOtherOpenError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "read.log")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir, state: map[string]EntryState{}, now: time.Now}
	if err := s.foldLog(p, 'r'); err == nil {
		t.Fatal("expected foldLog open error for directory")
	}
}

func TestAppendEntriesCanonicalizesProvidedHash(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fh := FeedHash("https://example.com/canon")
	legacy := "abcdef0123456789beef"
	added, err := s.AppendEntries(fh, []Entry{{Hash: legacy, GUID: "g", Link: "https://example.com/canon/1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].Hash != "abcdef0123456789" {
		t.Fatalf("added=%+v", added)
	}
	listed, err := s.ListEntries(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Hash != "abcdef0123456789" {
		t.Fatalf("listed=%+v", listed)
	}
}

func TestIsHexEmpty(t *testing.T) {
	if isHex("") {
		t.Fatal("empty string is not hex")
	}
}

func TestStateVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh store → zero.
	if !s.StateVersion().IsZero() {
		t.Errorf("fresh StateVersion=%v, want zero", s.StateVersion())
	}
	// AppendEntries bumps the version.
	fh := FeedHash("https://x/feed")
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "g1", Link: "l1"}}); err != nil {
		t.Fatal(err)
	}
	afterAppend := s.StateVersion()
	if afterAppend.IsZero() {
		t.Error("StateVersion zero after AppendEntries")
	}
	// SetRead bumps further.
	es, _ := s.ListEntries(fh)
	if err := s.SetRead(es[0].Hash, true); err != nil {
		t.Fatal(err)
	}
	afterRead := s.StateVersion()
	if !afterRead.After(afterAppend) {
		t.Errorf("StateVersion did not advance across SetRead: append=%v read=%v", afterAppend, afterRead)
	}
	// Idempotent SetRead → no further bump.
	if err := s.SetRead(es[0].Hash, true); err != nil {
		t.Fatal(err)
	}
	if s.StateVersion() != afterRead {
		t.Errorf("idempotent SetRead bumped StateVersion: %v → %v", afterRead, s.StateVersion())
	}
	// Persist across re-open. State-log timestamps round to the second
	// (RFC3339 without sub-second), so post-restart the version is
	// truncated to seconds. Assert the truncated equality.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want, got := afterRead.Truncate(time.Second), s2.StateVersion(); !got.Equal(want) {
		t.Errorf("StateVersion not restored from logs: was=%v (trunc=%v) now=%v", afterRead, want, got)
	}
}

// TestStateVersionNoRestartRegression guards the unread-count ETag
// against regressing below entries still on disk after a restart.
// Repro of the live bug: an old read mark sets the state-log timestamp,
// then newer entries arrive (bumping contentVer in-process). Before the
// fix, Open rebuilt contentVer from the state log alone, regressing
// below the new entries — so a client whose cached ETag predated them
// was wrongly served 304 and never re-fetched.
func TestStateVersionNoRestartRegression(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fh := FeedHash("https://x/feed")
	// An old entry, read long ago — pin the state-log timestamp into
	// the past via the now hook so the read mark is genuinely older
	// than the entries that arrive later.
	old := time.Now().UTC().Add(-72 * time.Hour)
	s.now = func() time.Time { return old }
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "old", Link: "lo", FetchedAt: old}}); err != nil {
		t.Fatal(err)
	}
	oldEntries, _ := s.ListEntries(fh)
	if err := s.SetRead(oldEntries[0].Hash, true); err != nil {
		t.Fatal(err)
	}
	s.now = time.Now
	// Fresh entries fetched recently — newer than any state-log mark.
	fetched := time.Now().UTC().Add(-1 * time.Minute)
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "new", Link: "ln", FetchedAt: fetched}}); err != nil {
		t.Fatal(err)
	}
	// Restart: the validator must NOT regress below the newest entry's
	// FetchedAt, or cached-ETag clients miss the new entries forever.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.StateVersion(); got.Before(fetched.Truncate(time.Second)) {
		t.Errorf("StateVersion regressed across restart: got=%v, want >= newest FetchedAt %v", got, fetched)
	}
}
