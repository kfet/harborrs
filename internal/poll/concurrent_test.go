package poll

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/store"
)

// TestTickOnceFansOutConcurrently spins up N httptest servers, each
// blocking on a per-feed gate, and asserts that tickOnce makes ~conc
// requests in flight at once (rather than serialising).
func TestTickOnceFansOutConcurrently(t *testing.T) {
	const (
		nFeeds = 6
		conc   = 4
	)
	var inFlight int32
	var maxInFlight int32
	releaseAll := make(chan struct{})
	rss := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>S</title></channel></rss>`
	hdl := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxInFlight)
			if n <= cur || atomic.CompareAndSwapInt32(&maxInFlight, cur, n) {
				break
			}
		}
		<-releaseAll
		atomic.AddInt32(&inFlight, -1)
		w.Header().Set("Content-Type", "application/rss+xml")
		io.WriteString(w, rss)
	})
	srv := httptest.NewServer(hdl)
	defer srv.Close()

	p, _, _ := newPoller(t)
	p.Concurrency = conc
	urls := make([]string, nFeeds)
	for i := 0; i < nFeeds; i++ {
		// Distinct URLs share the same backing server but get distinct
		// feedHashes (and so distinct state files / dedup keys).
		urls[i] = srv.URL + "/feed-" + string(rune('a'+i))
	}

	done := make(chan struct{})
	go func() {
		p.tickOnce(context.Background(), urls)
		close(done)
	}()

	// Wait until at least `conc` are blocked in the handler.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&maxInFlight) >= int32(conc) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := atomic.LoadInt32(&maxInFlight)
	if got < int32(conc) {
		t.Fatalf("max in flight = %d, want >= %d", got, conc)
	}
	if got > int32(conc) {
		t.Fatalf("max in flight = %d, want <= %d (bound)", got, conc)
	}
	close(releaseAll)
	<-done

	// All feeds should have state recorded.
	for _, u := range urls {
		if _, err := p.Store.LoadFeedState(store.FeedHash(u)); err != nil {
			t.Fatalf("missing state for %s: %v", u, err)
		}
	}
}

// TestTickOnceContextCancel verifies tickOnce returns promptly when
// ctx is cancelled while goroutines are mid-flight.
func TestTickOnceContextCancel(t *testing.T) {
	releaseAll := make(chan struct{})
	hdl := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-releaseAll
	})
	srv := httptest.NewServer(hdl)
	defer srv.Close()
	p, _, _ := newPoller(t)
	// Concurrency=1 + multiple feeds → after the first goroutine takes
	// the slot, the for-loop blocks on `sem <-`. Cancelling ctx then
	// trips the `case <-ctx.Done()` break-loop arm.
	p.Concurrency = 1
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
		// Unblock any in-flight handlers so tickOnce returns.
		close(releaseAll)
	}()
	urls := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c", srv.URL + "/d"}
	p.tickOnce(ctx, urls)
	// Reaching here means tickOnce returned; the test would deadlock
	// otherwise.
}

// TestTickOnceConcurrencyDefault exercises the Concurrency<=0 branch.
func TestTickOnceConcurrencyDefault(t *testing.T) {
	p, _, _ := newPoller(t)
	p.Concurrency = 0
	p.tickOnce(context.Background(), nil)
}
