package observe

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNopIsSafe(t *testing.T) {
	var o Observer = Nop{}
	o.Observe("fh", Observation{Outcome: Success})
	o.Sample("fh", []byte("x"))
}

func TestDiskObserverAppendsNDJSON(t *testing.T) {
	dir := t.TempDir()
	o := NewDiskObserver(dir)
	o.Observe("feed1", Observation{Outcome: Success, Status: 200, NewEntries: 3, Resolvers: []string{"strip-control-chars"}})
	o.Observe("feed1", Observation{Outcome: ParseError, Status: 200, Err: "boom", Sample: true})

	evs := readLog(t, filepath.Join(dir, "observe", "feed1.ndjson"))
	if len(evs) != 2 {
		t.Fatalf("want 2 records, got %d", len(evs))
	}
	if evs[0].Outcome != Success || evs[0].NewEntries != 3 {
		t.Fatalf("rec0=%+v", evs[0])
	}
	if evs[1].Outcome != ParseError || evs[1].Err != "boom" || !evs[1].Sample {
		t.Fatalf("rec1=%+v", evs[1])
	}
	// Time is auto-stamped.
	if evs[0].Time.IsZero() {
		t.Fatal("expected auto-stamped time")
	}
}

func TestDiskObserverSampleOverwritesAndTruncates(t *testing.T) {
	dir := t.TempDir()
	o := NewDiskObserver(dir)
	o.MaxSampleBytes = 4

	o.Sample("feed1", []byte("first body"))
	o.Sample("feed1", []byte("SECOND"))

	got, err := os.ReadFile(filepath.Join(dir, "observe", "feed1.sample"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "SECO" {
		t.Fatalf("sample=%q, want truncated 'SECO'", got)
	}
	// no leftover tmp
	ents, _ := os.ReadDir(filepath.Join(dir, "observe"))
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover tmp: %s", e.Name())
		}
	}
}

func TestDiskObserverPerFeedFiles(t *testing.T) {
	dir := t.TempDir()
	o := NewDiskObserver(dir)
	o.Observe("a", Observation{Outcome: Success})
	o.Observe("b", Observation{Outcome: HTTPError, Status: 500})
	if _, err := os.Stat(filepath.Join(dir, "observe", "a.ndjson")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "observe", "b.ndjson")); err != nil {
		t.Fatal(err)
	}
}

func readLog(t *testing.T, path string) []Observation {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Observation
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev Observation
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, ev)
	}
	return out
}

func TestObserveSwallowsMkdirError(t *testing.T) {
	dir := t.TempDir()
	// Make <dir>/observe a regular file so MkdirAll of it fails; Observe
	// must swallow the error (no panic, nothing written).
	if err := os.WriteFile(filepath.Join(dir, "observe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := NewDiskObserver(dir)
	o.Observe("feed", Observation{Outcome: Success}) // must not panic
}

func TestObserveSwallowsOpenFileError(t *testing.T) {
	dir := t.TempDir()
	// Make the target .ndjson a directory so the append OpenFile fails
	// while the parent MkdirAll succeeds.
	logDir := filepath.Join(dir, "observe", "feed.ndjson")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	o := NewDiskObserver(dir)
	o.Observe("feed", Observation{Outcome: Success}) // must not panic
}

func TestSampleDefaultCapWhenUnset(t *testing.T) {
	dir := t.TempDir()
	// Zero-value MaxSampleBytes must fall back to the default cap rather
	// than truncating to zero.
	o := &DiskObserver{dir: dir}
	o.Sample("feed", []byte("keep me"))
	got, err := os.ReadFile(filepath.Join(dir, "observe", "feed.sample"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me" {
		t.Fatalf("sample=%q", got)
	}
}

func TestSampleSwallowsMkdirError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "observe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := NewDiskObserver(dir)
	o.Sample("feed", []byte("body")) // must not panic
}

func TestSampleSwallowsWriteError(t *testing.T) {
	dir := t.TempDir()
	// Make the tmp target a directory so WriteFile fails after MkdirAll
	// of the parent succeeds.
	tmp := filepath.Join(dir, "observe", "feed.sample.tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	o := NewDiskObserver(dir)
	o.Sample("feed", []byte("body")) // must not panic; rename also no-ops
}
