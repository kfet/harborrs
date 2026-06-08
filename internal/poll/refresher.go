// refresher.go — pull-driven refresh.
//
// v0.4.18 replaces the per-feed adaptive scheduler with a single-flight
// Refresher. Anything that wants fresh entries calls Trigger() — the API
// middleware on `/reader/api/0/*` and `/ui/*` does this fire-and-forget
// on every request, and a background ticker does it once per minute (or
// whatever HARB_REFRESH_INTERVAL says).
//
// Trigger is non-blocking: if no cycle is in flight it spawns one; if
// one is already in flight, the call is a no-op. There is no queue:
// many concurrent Triggers collapse to at most one extra cycle, but
// since the in-flight cycle will already see whatever's currently in
// the OPML on its next iteration, that's fine.
//
// A cycle iterates every feed in the current OPML and calls Poll for
// each. To keep a large OPML from polling one-feed-at-a-time, feeds are
// fanned out across a BOUNDED worker pool (default 8, override via
// HARB_POLL_CONCURRENCY or the Concurrency field) — bounded so a
// 1000-feed OPML can't open 1000 sockets/fds or saturate the uplink at
// once. Per-feed 429/503 cooldown is honoured inside Poll via the
// RetryAfter field on FeedState — those feeds are skipped without a
// network round-trip. Feed ordering within a cycle is not guaranteed;
// nothing depends on it.
package poll

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FeedsFunc supplies the current list of feed URLs to refresh. Called
// once at the start of every cycle, so OPML edits made between cycles
// take effect on the next cycle without restart.
type FeedsFunc func() []string

// Refresher coordinates pull-driven refresh cycles.
//
// Construct via NewRefresher; call Trigger() from anything that wants
// fresh data and optionally call Start() to run a background ticker.
// Stop() cancels the background ticker, marks the Refresher stopped so
// no further cycles can spawn, and waits for any in-flight cycle to
// finish. Stop is idempotent.
//
// Lifetime / context model: every cycle runs under a long-lived
// Refresher-owned context (`baseCtx`) — *not* the caller's context
// passed to Trigger. The caller's ctx is irrelevant because cycles
// outlive HTTP requests: the middleware fires Trigger from a request
// handler, but the request finishes (and its ctx is cancelled) well
// before the cycle's HTTP fetches do. Cancelling those mid-flight on
// every UI click would defeat the whole point of the refresher.
type Refresher struct {
	Poller *Poller
	Feeds  FeedsFunc

	// Concurrency caps how many feeds a single cycle polls in
	// parallel. <=0 selects the env/default value (see
	// pollConcurrency / HARB_POLL_CONCURRENCY / DefaultPollConcurrency).
	Concurrency int

	// inFlight is 1 iff a cycle goroutine is currently running.
	// Single-flight admission control: only the CAS winner spawns a
	// goroutine; everyone else returns immediately.
	inFlight atomic.Int32

	// cyclesStarted counts cycles ever spawned. Test-only observation.
	cyclesStarted atomic.Int64

	// mu guards stopped + baseCancel. Held briefly in Trigger to
	// rule out a wg.Add-after-wg.Wait race vs Stop, and in Stop to
	// flip the stopped flag before cancelling and waiting.
	mu         sync.Mutex
	stopped    bool
	baseCtx    context.Context
	baseCancel context.CancelFunc
	wg         sync.WaitGroup

	// tickerCancel stops the background ticker started by Start.
	// Separate from baseCancel because Stop must cancel the ticker
	// AND any in-flight cycle, but cycles use baseCtx not the ticker
	// context.
	tickerCancel context.CancelFunc
}

// NewRefresher wires a Refresher around a Poller + feed provider.
func NewRefresher(p *Poller, feeds FeedsFunc) *Refresher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Refresher{
		Poller:     p,
		Feeds:      feeds,
		baseCtx:    ctx,
		baseCancel: cancel,
	}
}

// Trigger requests a refresh cycle. Non-blocking. If a cycle is already
// in flight, or the Refresher has been Stop'd, it returns immediately.
//
// The ctx argument is accepted for signature ergonomy (HTTP middleware
// passes req.Context()) but deliberately ignored — cycles outlive HTTP
// requests, so they run under the Refresher-owned baseCtx instead. See
// the type comment for why.
func (r *Refresher) Trigger(_ context.Context) {
	// CAS first — cheap fast path for the common case (already in
	// flight). Only take the mutex if we actually intend to spawn,
	// to keep the contention surface tiny.
	if !r.inFlight.CompareAndSwap(0, 1) {
		return
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		// Release the in-flight slot so a later (post-restart? no)
		// Trigger can win — but we are stopped, so this is purely
		// defensive cleanup.
		r.inFlight.Store(0)
		return
	}
	// Holding mu across wg.Add is what makes the race against Stop
	// safe: Stop takes the same mutex before calling wg.Wait, so any
	// wg.Add that wins this critical section happens-before Stop's
	// Wait observation.
	r.wg.Add(1)
	ctx := r.baseCtx
	r.mu.Unlock()
	r.cyclesStarted.Add(1)
	go r.cycle(ctx)
}

// cycle runs one refresh pass over every feed in the current OPML,
// fanning the per-feed Poll calls out across a bounded worker pool.
//
// Bounding: at most `concurrency` Polls run at once, gated by a
// buffered-channel semaphore. Cancellation: once ctx is cancelled we
// stop dispatching new feeds; workers already running drain (their own
// Poll honours ctx through the request context), and we wait for them
// before returning so inFlight/wg only clear once the cycle is truly
// quiescent.
func (r *Refresher) cycle(ctx context.Context) {
	defer r.wg.Done()
	defer r.inFlight.Store(0)
	if r.Feeds == nil {
		return
	}
	urls := r.Feeds()
	if len(urls) == 0 {
		return
	}
	concurrency := r.concurrency()
	sem := make(chan struct{}, concurrency)
	var workers sync.WaitGroup
	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		// Acquire a slot, but abort promptly if ctx is cancelled
		// while we're blocked waiting for one.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		workers.Add(1)
		go func(feedURL string) {
			defer workers.Done()
			defer func() { <-sem }()
			_, _ = r.Poller.Poll(ctx, feedURL)
		}(u)
	}
	// Let any in-flight workers drain before the cycle is considered
	// done — this is what makes Stop()'s wg.Wait observe a fully
	// quiescent cycle even on mid-cycle cancellation.
	workers.Wait()
}

// concurrency resolves the effective per-cycle parallelism: the
// explicit Concurrency field when positive, else the env/default.
func (r *Refresher) concurrency() int {
	if r.Concurrency > 0 {
		return r.Concurrency
	}
	return pollConcurrencyFromEnv()
}

// Start launches the background ticker. ticker fires Trigger every
// `interval` (defaults to refreshIntervalFromEnv if zero). Returns
// immediately; the goroutine runs until Stop or the supplied ctx is
// cancelled.
//
// `ctx` here scopes the *ticker* lifetime, not individual cycles —
// cycles always run under the Refresher's baseCtx, so a Start-ctx
// cancellation stops new ticks but lets the in-flight cycle finish.
func (r *Refresher) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = refreshIntervalFromEnv()
	}
	tctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.tickerCancel = cancel
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		// Kick one cycle immediately on Start so a fresh process
		// doesn't sit idle for a full interval before its first poll.
		r.Trigger(tctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-tctx.Done():
				return
			case <-t.C:
				r.Trigger(tctx)
			}
		}
	}()
}

// Stop marks the Refresher stopped (so no further cycles can spawn),
// cancels the background ticker (if started) and the long-lived cycle
// context, and waits for any in-flight cycle to finish. Idempotent.
func (r *Refresher) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	tickerCancel := r.tickerCancel
	r.tickerCancel = nil
	baseCancel := r.baseCancel
	r.mu.Unlock()
	if tickerCancel != nil {
		tickerCancel()
	}
	if baseCancel != nil {
		baseCancel()
	}
	r.wg.Wait()
}

// CyclesStarted returns the total number of cycles ever spawned. Test
// observation only; production callers have no business depending on
// this counter.
func (r *Refresher) CyclesStarted() int64 { return r.cyclesStarted.Load() }

// DefaultRefreshInterval is the cadence used when nothing else is
// configured. Picked to be aggressive enough that new entries show up
// within ~1 minute of publication on well-behaved feeds, but slow
// enough that a 1000-feed OPML doesn't keep the network saturated.
const DefaultRefreshInterval = 1 * time.Minute

// refreshIntervalFromEnv returns HARB_REFRESH_INTERVAL parsed as a
// Go duration, or DefaultRefreshInterval if unset/invalid.
func refreshIntervalFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("HARB_REFRESH_INTERVAL"))
	if v == "" {
		return DefaultRefreshInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return DefaultRefreshInterval
	}
	return d
}

// DefaultPollConcurrency is how many feeds a cycle polls in parallel
// when nothing overrides it. Eight is a deliberately conservative cap:
// enough to hide per-feed latency on a typical multi-hundred-feed OPML,
// but low enough that a 1000-feed OPML can't blow through the process
// fd limit or saturate a home uplink with simultaneous fetches.
const DefaultPollConcurrency = 8

// pollConcurrencyFromEnv returns HARB_POLL_CONCURRENCY parsed as a
// positive integer, or DefaultPollConcurrency if unset/invalid. Mirrors
// the HARB_REFRESH_INTERVAL pattern above.
func pollConcurrencyFromEnv() int {
	v := strings.TrimSpace(os.Getenv("HARB_POLL_CONCURRENCY"))
	if v == "" {
		return DefaultPollConcurrency
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return DefaultPollConcurrency
	}
	return n
}

// TriggerMiddleware wraps an http.Handler so that every request whose
// path matches one of the supplied prefixes fires Refresher.Trigger
// before the handler runs. Trigger is non-blocking, so this does not
// add request latency. The response itself reflects whatever is in
// the store right now — any newly-fetched entries show up on the
// client's next sync.
//
// Pass nil/empty prefixes to trigger on every request.
func TriggerMiddleware(r *Refresher, next http.Handler, prefixes ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r != nil && pathMatches(req.URL.Path, prefixes) {
			r.Trigger(req.Context())
		}
		next.ServeHTTP(w, req)
	})
}

func pathMatches(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
