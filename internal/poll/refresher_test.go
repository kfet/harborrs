package poll

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/store"
)

// newRefresher builds a poller+refresher pointed at a single in-process
// httptest feed server. blockHit, when non-nil, is closed once on the
// first request; subsequent requests block on releaseHit so the
// caller can pin a cycle "in flight".
func newRefresher(t *testing.T, feeds int, hits *atomic.Int64, releaseHit chan struct{}) (*Refresher, []string, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if releaseHit != nil {
			<-releaseHit
		}
		io.WriteString(w, sampleRSS)
	}))
	urls := make([]string, feeds)
	for i := range urls {
		// Distinct query strings → distinct feed hashes.
		urls[i] = srv.URL + "/?f=" + string(rune('a'+i))
	}
	r := NewRefresher(p, func() []string { return urls })
	return r, urls, func() { srv.Close(); _ = s }
}

// waitIdle blocks until no cycle is in flight, or t.Fatal on timeout.
// Tests use this instead of Stop() when they need the cycle to
// actually complete before assertions — Stop cancels baseCtx which
// short-circuits cycle goroutines.
func waitIdle(t *testing.T, r *Refresher) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.inFlight.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.inFlight.Load() != 0 {
		t.Fatal("cycle did not become idle")
	}
}

func TestRefresherTriggerSingleFlight(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	r, _, cleanup := newRefresher(t, 1, &hits, release)
	defer cleanup()

	// Trigger many times concurrently while the first cycle's HTTP
	// request is blocked. Exactly one cycle should be spawned.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Trigger(context.Background())
		}()
	}
	wg.Wait()
	// Give the cycle goroutine a moment to actually issue its HTTP req.
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits during in-flight cycle = %d, want 1", hits.Load())
	}
	if got := r.CyclesStarted(); got != 1 {
		t.Fatalf("CyclesStarted=%d, want 1", got)
	}
	close(release)
	r.Stop()
}

func TestRefresherCycle429Skipped(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(s)
	fixed := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { return fixed }

	var aHits, bHits atomic.Int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aHits.Add(1)
		io.WriteString(w, sampleRSS)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHits.Add(1)
		io.WriteString(w, sampleRSS)
	}))
	defer srvB.Close()

	// Pre-seed feed B in cooldown.
	fhB := store.FeedHash(srvB.URL)
	cool := store.FeedState{URL: srvB.URL, RetryAfter: fixed.Add(time.Hour)}
	if err := s.SaveFeedState(fhB, cool); err != nil {
		t.Fatal(err)
	}

	r := NewRefresher(p, func() []string { return []string{srvA.URL, srvB.URL} })
	r.Trigger(context.Background())
	waitIdle(t, r)
	r.Stop()

	if aHits.Load() != 1 {
		t.Fatalf("feed A hits = %d, want 1", aHits.Load())
	}
	if bHits.Load() != 0 {
		t.Fatalf("feed B (in cooldown) was polled %d times, want 0", bHits.Load())
	}
}

func TestRefresherStartTickerFires(t *testing.T) {
	r, _, cleanup := newRefresher(t, 1, nil, nil)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, 5*time.Millisecond)
	// Wait long enough for the initial Start cycle + at least one tick.
	deadline := time.Now().Add(500 * time.Millisecond)
	for r.CyclesStarted() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	r.Stop()
	if got := r.CyclesStarted(); got < 2 {
		t.Fatalf("CyclesStarted=%d, want ≥2 (initial + tick)", got)
	}
}

func TestRefresherStartDefaultInterval(t *testing.T) {
	r, _, cleanup := newRefresher(t, 1, nil, nil)
	defer cleanup()
	// Override env to a tiny duration; the zero-interval path picks
	// up refreshIntervalFromEnv.
	t.Setenv("HARBORRS_REFRESH_INTERVAL", "5ms")
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, 0)
	deadline := time.Now().Add(500 * time.Millisecond)
	for r.CyclesStarted() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	r.Stop()
	if got := r.CyclesStarted(); got < 2 {
		t.Fatalf("CyclesStarted=%d", got)
	}
}

func TestRefreshIntervalFromEnv(t *testing.T) {
	t.Setenv("HARBORRS_REFRESH_INTERVAL", "")
	if got := refreshIntervalFromEnv(); got != DefaultRefreshInterval {
		t.Fatalf("empty→%v", got)
	}
	t.Setenv("HARBORRS_REFRESH_INTERVAL", "not-a-duration")
	if got := refreshIntervalFromEnv(); got != DefaultRefreshInterval {
		t.Fatalf("garbage→%v", got)
	}
	t.Setenv("HARBORRS_REFRESH_INTERVAL", "-5s")
	if got := refreshIntervalFromEnv(); got != DefaultRefreshInterval {
		t.Fatalf("negative→%v", got)
	}
	t.Setenv("HARBORRS_REFRESH_INTERVAL", "2s")
	if got := refreshIntervalFromEnv(); got != 2*time.Second {
		t.Fatalf("2s→%v", got)
	}
}

func TestRefresherStopIdempotent(t *testing.T) {
	r, _, cleanup := newRefresher(t, 0, nil, nil)
	defer cleanup()
	r.Stop() // never started
	r.Stop() // double-stop is safe
}

func TestRefresherTriggerAfterStopIsNoop(t *testing.T) {
	r, _, cleanup := newRefresher(t, 1, nil, nil)
	defer cleanup()
	r.Stop()
	before := r.CyclesStarted()
	for i := 0; i < 10; i++ {
		r.Trigger(context.Background())
	}
	if r.CyclesStarted() != before {
		t.Fatalf("Trigger spawned a cycle after Stop: %d→%d", before, r.CyclesStarted())
	}
}

func TestRefresherCycleNilFeedsFunc(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(dir)
	p := New(s)
	r := NewRefresher(p, nil)
	r.Trigger(context.Background())
	r.Stop()
}

func TestRefresherCycleSurvivesRequestContextCancel(t *testing.T) {
	// Caller-supplied ctx is irrelevant: cancelling it before Trigger
	// must NOT short-circuit the cycle, because cycles run under
	// baseCtx (Refresher-owned), not the caller's ctx.
	dir := t.TempDir()
	s, _ := store.Open(dir)
	p := New(s)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	r := NewRefresher(p, func() []string {
		return []string{srv.URL + "/a", srv.URL + "/b"}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // dead on arrival
	r.Trigger(ctx)
	waitIdle(t, r)
	r.Stop()
	if hits.Load() != 2 {
		t.Fatalf("hits=%d, want 2 (caller-ctx cancel must not affect cycle)", hits.Load())
	}
}

func TestRefresherStopMidCycleCancelsBaseCtx(t *testing.T) {
	// A cycle in progress sees its ctx cancel when Stop is called,
	// and bails between feeds.
	dir := t.TempDir()
	s, _ := store.Open(dir)
	p := New(s)
	release := make(chan struct{})
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	r := NewRefresher(p, func() []string {
		return []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}
	})
	r.Trigger(context.Background())
	// Wait until the first feed's HTTP request is in flight.
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Stop will cancel baseCtx; the in-flight Poll may complete (we
	// don't cancel its body read mid-stream because httptest closes
	// cleanly), but the loop's ctx.Err() check will short-circuit
	// before feeds b and c.
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if got := hits.Load(); got >= 3 {
		t.Fatalf("hits=%d, expected <3 (Stop should have cut the cycle short)", got)
	}
}

func TestTriggerMiddlewarePrefix(t *testing.T) {
	r, _, cleanup := newRefresher(t, 1, nil, nil)
	defer cleanup()
	var served atomic.Int64
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	h := TriggerMiddleware(r, next, "/reader/api/0/", "/ui/")

	// Matching prefix → Trigger fires.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/reader/api/0/subscription/list", nil)
	h.ServeHTTP(rec, req)
	if served.Load() != 1 {
		t.Fatalf("next not called: %d", served.Load())
	}
	// Wait for the spawned cycle to actually run so it doesn't race
	// with the test's exit.
	deadline := time.Now().Add(500 * time.Millisecond)
	for r.CyclesStarted() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.CyclesStarted() != 1 {
		t.Fatalf("CyclesStarted=%d after first request", r.CyclesStarted())
	}
	r.Stop()

	// Non-matching prefix → no Trigger.
	r2, _, cleanup2 := newRefresher(t, 1, nil, nil)
	defer cleanup2()
	h2 := TriggerMiddleware(r2, next, "/reader/api/0/")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/status", nil)
	h2.ServeHTTP(rec2, req2)
	time.Sleep(20 * time.Millisecond)
	if r2.CyclesStarted() != 0 {
		t.Fatalf("expected no trigger on /status, got %d", r2.CyclesStarted())
	}
	r2.Stop()
}

func TestTriggerMiddlewareConcurrentRequestsSingleCycle(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	r, _, cleanup := newRefresher(t, 1, &hits, release)
	defer cleanup()
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})
	h := TriggerMiddleware(r, next, "/x/")
	// First request kicks off a cycle which blocks on releaseHit.
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "/x/a", nil))
	// Wait until the cycle's HTTP request is actually in flight.
	deadline := time.Now().Add(500 * time.Millisecond)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Now fire many more requests; none should spawn another cycle.
	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x/b", nil))
	}
	if got := r.CyclesStarted(); got != 1 {
		t.Fatalf("CyclesStarted=%d while one in flight, want 1", got)
	}
	close(release)
	r.Stop()
}

func TestTriggerMiddlewareEmptyPrefixesMatchAll(t *testing.T) {
	r, _, cleanup := newRefresher(t, 0, nil, nil)
	defer cleanup()
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})
	h := TriggerMiddleware(r, next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/anything", nil))
	deadline := time.Now().Add(500 * time.Millisecond)
	for r.CyclesStarted() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.CyclesStarted() != 1 {
		t.Fatalf("CyclesStarted=%d, want 1 (empty prefixes match all)", r.CyclesStarted())
	}
	r.Stop()
}

func TestTriggerMiddlewareNilRefresherIsNoop(t *testing.T) {
	var served bool
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { served = true })
	h := TriggerMiddleware(nil, next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/anything", nil))
	if !served {
		t.Fatal("next not invoked")
	}
}
