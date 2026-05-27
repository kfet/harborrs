// Package poll fetches RSS/Atom feeds with conditional GETs and adaptive
// scheduling, persisting state via the store package.
package poll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kfet/harborrs/internal/store"
	"github.com/mmcdole/gofeed"
)

// Defaults for the scheduler.
const (
	DefaultInterval    = 1 * time.Hour
	MinInterval        = 15 * time.Minute
	MaxInterval        = 24 * time.Hour
	UserAgent          = "harborrs/0.1 (+https://github.com/kfet/harborrs)"
	DefaultConcurrency = 8
)

// Poller fetches feeds and writes results to a Store.
type Poller struct {
	Store  *store.Store
	Client *http.Client
	// Parser is retained as a default/sentinel only. Poll never uses
	// the shared instance — gofeed.Parser is not goroutine-safe when
	// shared across concurrent feeds, so each Poll call builds a
	// fresh gofeed.NewParser(). Production code should not read this
	// field.
	Parser *gofeed.Parser
	Now    func() time.Time
	// MaxBodyBytes caps how much body we read per feed (default 10MiB).
	MaxBodyBytes int64
	// Concurrency bounds the number of feeds polled in parallel by
	// tickOnce. Zero or negative → DefaultConcurrency.
	Concurrency int
}

// New builds a Poller with sensible defaults. Store is required.
func New(s *store.Store) *Poller {
	return &Poller{
		Store:        s,
		Client:       &http.Client{Timeout: 30 * time.Second},
		Parser:       gofeed.NewParser(),
		Now:          time.Now,
		MaxBodyBytes: 10 * 1024 * 1024,
		Concurrency:  DefaultConcurrency,
	}
}

// Poll fetches one feed and returns the number of new entries appended.
// State (etag, last-modified, interval, error-count, next-fetch) is
// persisted regardless of outcome.
func (p *Poller) Poll(ctx context.Context, feedURL string) (int, error) {
	fh := store.FeedHash(feedURL)
	st, err := p.Store.LoadFeedState(fh)
	if err != nil {
		return 0, fmt.Errorf("load state: %w", err)
	}
	if st.URL == "" {
		st.URL = feedURL
	}
	st.LastFetched = p.Now().UTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return 0, p.recordErr(fh, &st, err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")
	if st.ETag != "" {
		req.Header.Set("If-None-Match", st.ETag)
	}
	if st.LastModified != "" {
		req.Header.Set("If-Modified-Since", st.LastModified)
	}

	resp, err := p.Client.Do(req)
	if err != nil {
		return 0, p.recordErr(fh, &st, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		// 304 — bump interval, persist, done.
		st.ErrorCount = 0
		st.LastError = ""
		// Multiplicative bump; clamp also handles the initial-zero case.
		bumped := time.Duration(st.Interval) * time.Second * 3 / 2
		if bumped == 0 {
			bumped = DefaultInterval
		}
		st.Interval = clampSeconds(bumped, MinInterval, MaxInterval)
		st.NextFetch = p.Now().UTC().Add(time.Duration(st.Interval) * time.Second)
		return 0, p.Store.SaveFeedState(fh, st)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// fall through
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		// Honour Retry-After when present.
		retry := parseRetryAfter(resp.Header.Get("Retry-After"), p.Now())
		err := fmt.Errorf("http %d", resp.StatusCode)
		st.ErrorCount++
		st.LastError = err.Error()
		if retry > 0 {
			st.Interval = clampSecondsInt64(int64(retry/time.Second), MinInterval, MaxInterval)
		} else {
			st.Interval = backoffSeconds(st.Interval)
		}
		st.NextFetch = p.Now().UTC().Add(time.Duration(st.Interval) * time.Second)
		if saveErr := p.Store.SaveFeedState(fh, st); saveErr != nil {
			return 0, saveErr
		}
		return 0, err
	default:
		return 0, p.recordErr(fh, &st, fmt.Errorf("http %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, p.MaxBodyBytes+1))
	if err != nil {
		return 0, p.recordErr(fh, &st, err)
	}
	if int64(len(body)) > p.MaxBodyBytes {
		return 0, p.recordErr(fh, &st, errors.New("body too large"))
	}

	parsed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		return 0, p.recordErr(fh, &st, err)
	}

	now := p.Now().UTC()
	entries := make([]store.Entry, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		e := store.Entry{
			FeedHash:  fh,
			GUID:      it.GUID,
			Link:      it.Link,
			Title:     it.Title,
			Summary:   it.Description,
			Content:   it.Content,
			FetchedAt: now,
		}
		if it.Author != nil {
			e.Author = it.Author.Name
		}
		if it.PublishedParsed != nil {
			e.Published = it.PublishedParsed.UTC()
		} else {
			e.Published = now
		}
		e.Hash = store.EntryHash(e.GUID, e.Link)
		entries = append(entries, e)
	}

	added, err := p.Store.AppendEntries(fh, entries)
	if err != nil {
		return 0, p.recordErr(fh, &st, err)
	}

	// Success — refresh conditional headers, reset error state, set
	// next-fetch.
	st.ETag = resp.Header.Get("ETag")
	st.LastModified = resp.Header.Get("Last-Modified")
	st.ErrorCount = 0
	st.LastError = ""
	if st.Interval == 0 {
		st.Interval = int64(DefaultInterval / time.Second)
	}
	st.NextFetch = p.Now().UTC().Add(time.Duration(st.Interval) * time.Second)
	if err := p.Store.SaveFeedState(fh, st); err != nil {
		return len(added), err
	}
	return len(added), nil
}

func (p *Poller) recordErr(fh string, st *store.FeedState, e error) error {
	st.ErrorCount++
	st.LastError = e.Error()
	st.Interval = backoffSeconds(st.Interval)
	st.NextFetch = p.Now().UTC().Add(time.Duration(st.Interval) * time.Second)
	if saveErr := p.Store.SaveFeedState(fh, *st); saveErr != nil {
		return saveErr
	}
	return e
}

func backoffSeconds(curr int64) int64 {
	d := time.Duration(curr) * time.Second
	if d <= 0 {
		d = DefaultInterval
	}
	d *= 2
	if d < MinInterval {
		d = MinInterval
	}
	if d > MaxInterval {
		d = MaxInterval
	}
	return int64(d / time.Second)
}

func clampSeconds(d, lo, hi time.Duration) int64 {
	if d < lo {
		d = lo
	}
	if d > hi {
		d = hi
	}
	return int64(d / time.Second)
}

func clampSecondsInt64(s int64, lo, hi time.Duration) int64 {
	d := time.Duration(s) * time.Second
	return clampSeconds(d, lo, hi)
}

// parseRetryAfter parses a Retry-After header (seconds or HTTP-date) and
// returns a duration from "now". Returns 0 if unparseable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// Run drives the scheduler: every tick, poll any feed whose NextFetch has
// passed. Returns when ctx is done. Feeds is taken once from opml.
// New feeds added later require a restart (v0.1).
func (p *Poller) Run(ctx context.Context, feeds func() []string, tick time.Duration) error {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		p.tickOnce(ctx, feeds())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (p *Poller) tickOnce(ctx context.Context, urls []string) {
	now := p.Now().UTC()
	conc := p.Concurrency
	if conc <= 0 {
		conc = DefaultConcurrency
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
loop:
	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		fh := store.FeedHash(u)
		st, err := p.Store.LoadFeedState(fh)
		if err != nil {
			continue
		}
		if !st.NextFetch.IsZero() && st.NextFetch.After(now) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _ = p.Poll(ctx, u)
		}(u)
	}
	wg.Wait()
}
