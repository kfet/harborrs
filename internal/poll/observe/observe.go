// Package observe records the outcome of every feed poll so an
// out-of-process fixer can diagnose breakage and write resolver sidecars.
//
// It is pure observability — harborrs neither reacts to nor schedules
// anything from these records. The contract is one-directional:
//
//	harborrs  ──writes──▶  <data-dir>/observe/<feedHash>.ndjson   (outcomes)
//	                       <data-dir>/observe/<feedHash>.sample   (last bad body)
//	fixer     ──reads───▶  the above, diagnoses, then
//	          ──writes──▶  <data-dir>/resolvers/<feedHash>.json   (Specs)
//
// The two processes share only the filesystem. harborrs never imports the
// fixer and the fixer never links harborrs.
package observe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Outcome classifies how a poll ended. The fixer keys its heuristics off
// these — e.g. a run of ParseError with a saved sample is its cue to act.
type Outcome string

const (
	Success     Outcome = "success"      // 2xx, parsed, entries appended
	NotModified Outcome = "not-modified" // 304
	Cooldown    Outcome = "cooldown"     // skipped: in 429/503 window
	HTTPError   Outcome = "http-error"   // non-2xx (incl. 429/503)
	FetchError  Outcome = "fetch-error"  // transport failure, no response
	ParseError  Outcome = "parse-error"  // body fetched but gofeed rejected it
	TooLarge    Outcome = "too-large"    // body exceeded MaxBodyBytes
)

// Observation is one poll outcome. JSON tags are terse because these
// accumulate one line per feed per poll.
type Observation struct {
	Time        time.Time `json:"t"`
	Outcome     Outcome   `json:"outcome"`
	Status      int       `json:"status,omitempty"`
	ContentType string    `json:"ct,omitempty"`
	Bytes       int       `json:"bytes,omitempty"`
	NewEntries  int       `json:"new,omitempty"`
	Err         string    `json:"err,omitempty"`
	// Resolvers names the chain that ran for this poll, so the fixer can
	// tell "broke with no resolvers" from "still broken despite resolvers".
	Resolvers []string `json:"resolvers,omitempty"`
	// Sample is true when a failing-body sample was written alongside this
	// observation (see DiskObserver.Sample).
	Sample bool `json:"sample,omitempty"`
}

// Observer records poll outcomes. Implementations must be safe for
// concurrent use — the Refresher polls feeds in parallel.
type Observer interface {
	// Observe records one outcome for a feed.
	Observe(feedHash string, ev Observation)
	// Sample persists the latest failing body for a feed (overwriting any
	// previous sample), so the fixer has raw material to diagnose. body is
	// truncated to a sane cap by the implementation. Callers set
	// Observation.Sample=true when they also call this.
	Sample(feedHash string, body []byte)
}

// Nop is an Observer that discards everything. Used when observability is
// disabled and as a safe default for nil-checks.
type Nop struct{}

func (Nop) Observe(string, Observation) {}
func (Nop) Sample(string, []byte)       {}

// DiskObserver writes observations as NDJSON under <dir>/observe and keeps
// the most recent failing-body sample per feed. Appends are serialised by
// a mutex; this is the cold path (one write per poll), so contention is a
// non-issue and crash-torn lines are avoided.
type DiskObserver struct {
	dir string
	// MaxSampleBytes caps a saved sample (default 256 KiB). Enough for a
	// fixer to see the breakage, bounded so a giant feed can't fill disk.
	MaxSampleBytes int

	mu sync.Mutex
}

// NewDiskObserver returns a DiskObserver rooted at dir (the data dir). The
// observe/ subdir is created lazily on first write.
func NewDiskObserver(dir string) *DiskObserver {
	return &DiskObserver{dir: dir, MaxSampleBytes: 256 * 1024}
}

func (d *DiskObserver) logPath(feedHash string) string {
	return filepath.Join(d.dir, "observe", feedHash+".ndjson")
}

func (d *DiskObserver) samplePath(feedHash string) string {
	return filepath.Join(d.dir, "observe", feedHash+".sample")
}

// Observe appends one NDJSON line. Errors are swallowed: observability
// must never break a poll. A failed write simply loses that record.
func (d *DiskObserver) Observe(feedHash string, ev Observation) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	// Observation is our own flat struct; Marshal cannot fail.
	line, _ := json.Marshal(ev)
	line = append(line, '\n')

	d.mu.Lock()
	defer d.mu.Unlock()
	path := d.logPath(feedHash)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(line)
	_ = f.Close()
}

// Sample overwrites the feed's failing-body sample, truncated to
// MaxSampleBytes. Best-effort: errors are swallowed.
func (d *DiskObserver) Sample(feedHash string, body []byte) {
	cap := d.MaxSampleBytes
	if cap <= 0 {
		cap = 256 * 1024
	}
	if len(body) > cap {
		body = body[:cap]
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	path := d.samplePath(feedHash)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

var _ Observer = (*DiskObserver)(nil)
var _ Observer = Nop{}
