package reader

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/store"
)

// TestPerfBudget is a CI regression guard for the Reader API hot path.
//
// It rebuilds the same ~60-feed × ~33-entry fixture as the benchmarks
// in perf_test.go, runs the two shapes that dominate a real sync
// (Reeder-like 12+6+1 burst, and an all-feeds IndexedEntries sweep)
// under testing.Benchmark, and asserts the measured ns/op stays under
// a budget set at ~2.5–7× the M1 baseline.
//
// Baselines measured on darwin/arm64 (Apple M1) at v0.4.11+gzip:
//
//	BenchmarkReederLikeSync                  ~7.7  ms/op (post-index)
//	BenchmarkListVsIndexed/IndexedEntries    ~0.07 ms/op
//
// Budgets chosen with headroom so noise on a loaded laptop doesn't
// flap the gate, but a real regression (e.g. someone reintroducing a
// per-request disk scan, or bypassing the index) trips it:
//
//	reederLikeBudget   = 25 ms   (~3× baseline)
//	indexedSweepBudget = 0.5 ms  (~7× baseline; tiny absolute, noisy in %)
//
// Auto-skips when:
//   - testing.Short() is set,
//   - the race detector is enabled (inflates timings 5–10×),
//   - GOARCH is not amd64/arm64 (other arches aren't perf-pinned).
//
// `make all` runs `go test -race`, so this gate is effectively
// opt-in for CI lanes that drop -race; running it locally is just
// `go test -run TestPerfBudget ./internal/reader/`.
func TestPerfBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("perf guard skipped under -short")
	}
	if raceEnabled {
		t.Skip("perf guard skipped under -race (timings inflated 5–10×)")
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
		// perf-pinned.
	default:
		t.Skipf("perf guard not pinned for GOARCH=%s", runtime.GOARCH)
	}

	const (
		reederLikeBudget   = 25 * time.Millisecond
		indexedSweepBudget = 500 * time.Microsecond
	)

	t.Run("ReederLikeSync", func(t *testing.T) {
		res := testing.Benchmark(benchReederLikeSync)
		got := time.Duration(res.NsPerOp())
		t.Logf("reeder-like sync: %s/op (N=%d, budget=%s)", got, res.N, reederLikeBudget)
		if got > reederLikeBudget {
			t.Fatalf("reeder-like sync ns/op %s exceeds budget %s — perf regression?",
				got, reederLikeBudget)
		}
	})

	t.Run("IndexedEntriesAllFeeds", func(t *testing.T) {
		res := testing.Benchmark(benchIndexedSweep)
		got := time.Duration(res.NsPerOp())
		t.Logf("indexed all-feeds sweep: %s/op (N=%d, budget=%s)",
			got, res.N, indexedSweepBudget)
		if got > indexedSweepBudget {
			t.Fatalf("indexed sweep ns/op %s exceeds budget %s — index bypassed?",
				got, indexedSweepBudget)
		}
	})
}

// guardFixture seed — fixed so the synthetic content is byte-identical
// run-to-run and the gate doesn't depend on wall-clock or process PID.
const guardSeed = 0x6861726276727273

// buildGuardStore materialises the standard ~60×33 fixture used by both
// guard sub-benchmarks. Identical to perf_test.go's setup in shape; we
// duplicate (rather than share) to keep that file's bench surface
// untouched and so the guard fixture can evolve independently.
func buildGuardStore(tb testing.TB) (*store.Store, *memOPMLBench) {
	tb.Helper()
	const numFeeds = 60
	const entriesPerFeed = 33

	dir := tb.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		tb.Fatal(err)
	}
	op := &memOPMLBench{}

	// Deterministic clock: a fixed instant, not time.Now(). The
	// fixture's Published / FetchedAt thus depend only on (f, i).
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Deterministic body: ~6 KB of pseudo-random words seeded from a
	// fixed RNG so the byte layout is reproducible.
	r := rand.New(rand.NewSource(guardSeed))
	var bodyB strings.Builder
	bodyB.Grow(6144)
	words := []string{"lorem", "ipsum", "dolor", "sit", "amet", "consectetur",
		"adipiscing", "elit", "sed", "do", "eiusmod", "tempor"}
	for bodyB.Len() < 6144 {
		bodyB.WriteString("<p>")
		for k := 0; k < 16; k++ {
			bodyB.WriteString(words[r.Intn(len(words))])
			bodyB.WriteByte(' ')
		}
		bodyB.WriteString("</p>")
	}
	body := bodyB.String()

	for f := 0; f < numFeeds; f++ {
		u := fmt.Sprintf("https://feed.example/%d.xml", f)
		op.opml.Feeds = append(op.opml.Feeds, store.Feed{
			XMLURL: u,
			Title:  "Feed " + strconv.Itoa(f),
			Tags:   []string{"folder" + strconv.Itoa(f%5)},
		})
		fh := store.FeedHash(u)
		es := make([]store.Entry, entriesPerFeed)
		for i := 0; i < entriesPerFeed; i++ {
			es[i] = store.Entry{
				GUID:      "g" + strconv.Itoa(f) + "-" + strconv.Itoa(i),
				Link:      fmt.Sprintf("https://feed.example/%d/%d", f, i),
				Title:     "Entry " + strconv.Itoa(i),
				Content:   body,
				Published: base.Add(-time.Duration(f*entriesPerFeed+i) * time.Minute),
				FetchedAt: base,
			}
		}
		if _, err := st.AppendEntries(fh, es); err != nil {
			tb.Fatal(err)
		}
	}
	return st, op
}

// benchReederLikeSync mirrors BenchmarkReederLikeSync in perf_test.go
// but with deterministic fixture data and an explicit fixed Now so the
// gate is reproducible. Kept separate so perf_test.go's bench surface
// is untouched.
func benchReederLikeSync(b *testing.B) {
	st, op := buildGuardStore(b)
	srv := &Server{
		Store:   st,
		OPML:    op,
		Now:     func() time.Time { return time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC) },
		MaxPage: 200,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/reader/api/0/stream/items/ids", srv.handleItemsIDs)
	mux.HandleFunc("/reader/api/0/stream/items/contents", srv.handleItemsContents)
	mux.HandleFunc("/reader/api/0/unread-count", srv.handleUnreadCount)

	all := st.IndexedEntries(store.FeedHash(op.opml.Feeds[0].XMLURL))
	contentIDs := make([]string, 0, 100)
	for _, e := range all {
		contentIDs = append(contentIDs, itemLongID(e.Hash))
		if len(contentIDs) >= 100 {
			break
		}
	}
	idsForm := url.Values{"s": {streamReadingList}, "n": {"200"}}.Encode()
	contentForm := url.Values{"i": contentIDs}.Encode()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 12; j++ {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST",
				"/reader/api/0/stream/items/ids",
				strings.NewReader(idsForm))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_ = req.ParseForm()
			mux.ServeHTTP(rr, req)
		}
		for j := 0; j < 6; j++ {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST",
				"/reader/api/0/stream/items/contents",
				strings.NewReader(contentForm))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_ = req.ParseForm()
			mux.ServeHTTP(rr, req)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/reader/api/0/unread-count", nil)
		_ = req.ParseForm()
		mux.ServeHTTP(rr, req)
	}
}

// benchIndexedSweep measures the cost of an all-feeds IndexedEntries
// sweep — the cheap path the Reader handlers rely on. If anyone
// silently swaps it back to store.ListEntries (disk-backed), the
// budget here trips immediately.
func benchIndexedSweep(b *testing.B) {
	st, op := buildGuardStore(b)
	feeds := make([]string, 0, len(op.opml.Feeds))
	for _, f := range op.opml.Feeds {
		feeds = append(feeds, store.FeedHash(f.XMLURL))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, fh := range feeds {
			_ = st.IndexedEntries(fh)
		}
	}
}
