package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// brokenWriter / brokenCloser help drive io errors.
type brokenWriter struct{ err error }

func (b brokenWriter) Write(p []byte) (int, error) { return 0, b.err }
func (b brokenWriter) Close() error                { return nil }

type closeErr struct{ io.Writer }

func (c closeErr) Close() error { return errors.New("close-boom") }

func swapJSONMarshal(t *testing.T, fail bool) {
	t.Helper()
	orig := jsonMarshal
	t.Cleanup(func() { jsonMarshal = orig })
	if fail {
		jsonMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal-boom") }
	}
}

func swapAtomicWrite(t *testing.T, fail bool) {
	t.Helper()
	orig := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = orig })
	if fail {
		atomicWriteFile = func(string, []byte) error { return errors.New("atomic-boom") }
	}
}

func swapOpenAppend(t *testing.T, w io.WriteCloser, err error) {
	t.Helper()
	orig := osOpenAppend
	t.Cleanup(func() { osOpenAppend = orig })
	osOpenAppend = func(string) (io.WriteCloser, error) {
		if err != nil {
			return nil, err
		}
		return w, nil
	}
}

func TestAppendEntries_OpenFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	swapOpenAppend(t, nil, errors.New("open-boom"))
	_, err := s.AppendEntries("fh", []Entry{{GUID: "x"}})
	if err == nil || !strings.Contains(err.Error(), "open-boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestAppendEntries_WriteFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	swapOpenAppend(t, brokenWriter{err: errors.New("write-boom")}, nil)
	_, err := s.AppendEntries("fh", []Entry{{GUID: "x"}})
	if err == nil || !strings.Contains(err.Error(), "write-boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestAppendEntries_MarshalFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	swapJSONMarshal(t, true)
	_, err := s.AppendEntries("fh", []Entry{{GUID: "x"}})
	if err == nil || !strings.Contains(err.Error(), "marshal-boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestAppendEntries_MkdirSurfacesAsKnownHashesError(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "blk"
	os.MkdirAll(filepath.Join(dir, "entries"), 0o755)
	os.WriteFile(filepath.Join(dir, "entries", fh), nil, 0o644)
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "x"}}); err == nil {
		t.Fatal("expected error via knownHashes")
	}
}

func TestKnownHashes_ReadDirError(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// Pre-create entries/<fh> as a *file* (not dir), so ReadDir returns
	// a non-NotExist error (ENOTDIR).
	fh := "rd"
	os.MkdirAll(filepath.Join(dir, "entries"), 0o755)
	os.WriteFile(filepath.Join(dir, "entries", fh), nil, 0o644)
	if _, err := s.knownHashes(fh); err == nil {
		t.Fatal("expected readdir error")
	}
}

func TestListEntries_ReadDirError(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "rd"
	os.MkdirAll(filepath.Join(dir, "entries"), 0o755)
	os.WriteFile(filepath.Join(dir, "entries", fh), nil, 0o644)
	if _, err := s.ListEntries(fh); err == nil {
		t.Fatal("expected error")
	}
}

func TestNonNDJSONIgnored_AndEmptyLine(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	feedDir := filepath.Join(dir, "entries", fh)
	os.MkdirAll(feedDir, 0o755)
	os.WriteFile(filepath.Join(feedDir, "readme.txt"), []byte("ignore me"), 0o644)
	os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte(
		`{"hash":"a","title":"A"}`+"\n\n"+`{"hash":"b","title":"B"}`+"\n"), 0o644)
	got, err := s.ListEntries(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
}

func TestScanEntries_FnError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.ndjson")
	os.WriteFile(p, []byte(`{"hash":"a"}`+"\n"), 0o644)
	want := errors.New("fn-boom")
	err := scanEntries(p, func(Entry) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	// Open error.
	if err := scanEntries(filepath.Join(dir, "missing"), func(Entry) error { return nil }); err == nil {
		t.Fatal("expected error")
	}
}

func TestRollover_OpenFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "a", Published: old, FetchedAt: old}}); err != nil {
		t.Fatal(err)
	}
	swapOpenAppend(t, nil, errors.New("ropen-boom"))
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected open error")
	}
}

func TestRollover_WriteFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "a", Published: old, FetchedAt: old}}); err != nil {
		t.Fatal(err)
	}
	swapOpenAppend(t, brokenWriter{err: errors.New("rwrite-boom")}, nil)
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected write error")
	}
}

func TestRollover_MarshalFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AppendEntries(fh, []Entry{
		{GUID: "a", Published: old, FetchedAt: old},
		{GUID: "b", Published: old, FetchedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	swapJSONMarshal(t, true)
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestRollover_CloseFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "a", Published: old, FetchedAt: old}}); err != nil {
		t.Fatal(err)
	}
	// real file under the hood + custom close error
	pf, e := os.CreateTemp(dir, "wrap")
	if e != nil {
		t.Fatal(e)
	}
	swapOpenAppend(t, closeErr{Writer: pf}, nil)
	defer pf.Close()
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected close error")
	}
}

func TestRollover_AtomicWriteFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AppendEntries(fh, []Entry{{GUID: "a", Published: old, FetchedAt: old}}); err != nil {
		t.Fatal(err)
	}
	swapAtomicWrite(t, true)
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected atomic error")
	}
}

func TestRollover_ScanFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	feedDir := filepath.Join(dir, "entries", fh)
	os.MkdirAll(feedDir, 0o755)
	os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("not json\n"), 0o644)
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected scan error")
	}
}

func TestLoadFeedState_OtherError(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// Make state/fh.json a directory → ReadFile returns EISDIR.
	p := filepath.Join(dir, "state", "fh.json")
	os.MkdirAll(p, 0o755)
	if _, err := s.LoadFeedState("fh"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveFeedState_MarshalFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// Override marshal indent path. SaveFeedState uses json.MarshalIndent
	// directly, but we hooked json.Marshal not MarshalIndent. So instead
	// drive atomicWriteFile fail.
	swapAtomicWrite(t, true)
	if err := s.SaveFeedState("fh", FeedState{}); err == nil {
		t.Fatal("expected atomic error")
	}
}

func TestSetRead_AppendFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	swapOpenAppend(t, nil, errors.New("openboom"))
	if err := s.SetRead("h", true); err == nil {
		t.Fatal("expected error")
	}
	// Also exercise SetStarred path.
	if err := s.SetStarred("h2", true); err == nil {
		t.Fatal("expected error (starred)")
	}
}

func TestCompact_AtomicFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// Swap atomic BEFORE the busy loop so the eventual compaction during it
	// hits our fault.
	swapAtomicWrite(t, true)
	var lastErr error
	for i := 0; i < 100 && lastErr == nil; i++ {
		lastErr = s.SetRead(fmt.Sprintf("h%d", i), true)
		if lastErr == nil {
			lastErr = s.SetRead(fmt.Sprintf("h%d", i), false)
		}
	}
	if lastErr == nil {
		t.Fatal("expected compact atomic error")
	}
}

func TestFoldLog_StarredOpenError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "starred.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected starred error")
	}
}

const nestedOPML = `<?xml version="1.0"?>
<opml version="2.0"><body>
  <outline title="Outer">
    <outline title="Inner">
      <outline type="rss" xmlUrl="https://nested.example/feed"/>
    </outline>
  </outline>
  <outline type="rss" title="Same" xmlUrl="https://a.example/feed"/>
  <outline type="rss" title="Same" xmlUrl="https://b.example/feed"/>
</body></opml>`

func TestOPMLNestedAndTiebreak(t *testing.T) {
	o, err := ParseOPML([]byte(nestedOPML))
	if err != nil {
		t.Fatal(err)
	}
	var nested *Feed
	for i := range o.Feeds {
		if o.Feeds[i].Folder == "Outer/Inner" {
			nested = &o.Feeds[i]
		}
	}
	if nested == nil {
		t.Fatalf("missing nested feed: %+v", o.Feeds)
	}
	// Round-trip and ensure stable.
	data, err := o.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "https://a.example/feed") {
		t.Fatal("missing a.example after marshal")
	}
}

// Marshal failure path on OPML — drive via xml-encoding behaviour: there's
// no straightforward way to fail xml.MarshalIndent for our struct, so we
// rely on the (very small) error-wrapping branch staying covered by the
// jsonMarshal-style hook is N/A here. Mark this as test-only sanity.
func TestOPMLEmpty(t *testing.T) {
	o := &OPML{}
	if _, err := o.Marshal(); err != nil {
		t.Fatal(err)
	}
}

// Hooked marshal/write failures for OPML.

func TestOPMLMarshalFailure(t *testing.T) {
	orig := xmlMarshalIndent
	t.Cleanup(func() { xmlMarshalIndent = orig })
	xmlMarshalIndent = func(any, string, string) ([]byte, error) {
		return nil, errors.New("xml-boom")
	}
	o := &OPML{Feeds: []Feed{{XMLURL: "u"}}}
	if _, err := o.Marshal(); err == nil {
		t.Fatal("expected error")
	}
	if err := o.WriteOPML("/tmp/ignored"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveFeedState_MarshalIndentFail(t *testing.T) {
	orig := jsonMarshalIndent
	t.Cleanup(func() { jsonMarshalIndent = orig })
	jsonMarshalIndent = func(any, string, string) ([]byte, error) {
		return nil, errors.New("mi-boom")
	}
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.SaveFeedState("fh", FeedState{}); err == nil {
		t.Fatal("expected error")
	}
}

// FoldLog returns scanner error: write a huge single line that exceeds the
// per-line buffer (1MiB).
func TestFoldLog_ScannerError(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(filepath.Join(dir, "read.log"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected scanner error")
	}
}

// knownHashes line 301: a feed dir with a non-ndjson file, exercised via
// AppendEntries (which goes through knownHashes).
func TestKnownHashes_NonNDJSONIgnored(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	feedDir := filepath.Join(dir, "entries", fh)
	os.MkdirAll(feedDir, 0o755)
	os.WriteFile(filepath.Join(feedDir, "README"), []byte("hi"), 0o644)
	added, err := s.AppendEntries(fh, []Entry{{GUID: "x"}})
	if err != nil || len(added) != 1 {
		t.Fatalf("added=%v err=%v", added, err)
	}
}

// AppendEntries MkdirAll fail: make entries/ a regular file so MkdirAll on
// entries/<fh> fails with ENOTDIR. knownHashes must still succeed (it sees
// the parent as non-dir → ENOTDIR via ReadDir → non-NotExist error). Hmm,
// that would return error before MkdirAll. So instead: pre-create
// entries/<fh> as a writable dir, then make `entries` read-only? Too racy.
// Simplest reliable trick: hook os.MkdirAll? Add a hook.

// Rollover keep-marshal fail: pass only "keep" entries that survive
// archiving (i.e. newer than cutoff). The first jsonMarshal call is in the
// archive loop; if there are no archive items, the function returns early
// (no archives → no rewrite). To exercise line 427 we need both old and
// new entries, and a marshal hook that fails only on the keep call.
func TestRollover_KeepMarshalFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Now().UTC().Add(-1 * time.Hour)
	if _, err := s.AppendEntries(fh, []Entry{
		{GUID: "a", Published: old, FetchedAt: old},
		{GUID: "b", Published: fresh, FetchedAt: fresh},
	}); err != nil {
		t.Fatal(err)
	}
	orig := jsonMarshal
	t.Cleanup(func() { jsonMarshal = orig })
	calls := 0
	jsonMarshal = func(v any) ([]byte, error) {
		calls++
		// First call within rollover is the archive marshal (1 old entry).
		// Second call is the keep-loop marshal — fail that one.
		if calls >= 2 {
			return nil, errors.New("keep-boom")
		}
		return orig(v)
	}
	// Cutoff between old and fresh: "a" archived, "b" kept.
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	_, err := s.RolloverArchives(fh, cutoff)
	t.Logf("rollover calls=%d err=%v", calls, err)
	if err == nil {
		t.Fatal("expected keep-marshal error")
	}
}

// appendLine: ensure WriteString failure path runs. Drive via SetRead with
// a hooked osOpenAppend returning brokenWriter.
func TestAppendLine_WriteFail(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	swapOpenAppend(t, brokenWriter{err: errors.New("ws-boom")}, nil)
	if err := s.SetRead("h", true); err == nil {
		t.Fatal("expected write error")
	}
}

// FoldLog open with non-NotExist error: create read.log with mode 0o000 and
// drop a non-root euid context by trying to open it. On most macOS/Linux test
// envs running as user, that produces EACCES from os.Open.
func TestFoldLog_OpenPermDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses perms")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "read.log")
	if err := os.WriteFile(p, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o644) })
	if _, err := Open(dir); err == nil {
		t.Fatal("expected perm error")
	}
}

// AppendEntries reaches MkdirAll only when knownHashes succeeds. To trigger
// MkdirAll failure we precreate per-feed dir as regular dir (knownHashes
// returns empty set), but make the *grandparent* `entries` chmod-locked.
func TestAppendEntries_MkdirFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses perms")
	}
	dir := t.TempDir()
	s, _ := Open(dir)
	// Pre-create entries dir, then make it read-only so MkdirAll on a new
	// subdir fails.
	entriesDir := filepath.Join(dir, "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(entriesDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(entriesDir, 0o755) })
	if _, err := s.AppendEntries("newfh", []Entry{{GUID: "x"}}); err == nil {
		t.Fatal("expected mkdir error")
	}
}

// Rollover stat error: chmod 0 the feed dir so Stat on current.ndjson fails
// with EACCES (depends on platform — Linux/macOS user mode).
func TestRollover_StatPermDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses perms")
	}
	dir := t.TempDir()
	s, _ := Open(dir)
	fh := "f"
	feedDir := filepath.Join(dir, "entries", fh)
	if err := os.MkdirAll(feedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	curPath := filepath.Join(feedDir, "current.ndjson")
	if err := os.WriteFile(curPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(feedDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(feedDir, 0o755) })
	if _, err := s.RolloverArchives(fh, time.Now()); err == nil {
		t.Fatal("expected stat error")
	}
}
