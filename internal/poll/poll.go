// Package poll fetches RSS/Atom feeds with conditional GETs.
//
// Cadence is driven by the pull-side Refresher (see refresher.go), not by
// per-feed timers — v0.4.18 removed the adaptive scheduler that pinned
// well-behaved feeds to once-per-24h after a string of 304s. Poll itself
// is a single-shot operation: one HTTP request, one persist of the
// updated FeedState. The only per-feed throttle that survives in Poll
// is RetryAfter, set when a feed returned 429 / 503 with a Retry-After
// header; subsequent Poll calls within the window return early without
// touching the network.
package poll

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kfet/harborrs/internal/store"
	"github.com/mmcdole/gofeed"
)

// UserAgent is the HTTP User-Agent harborrs sends on every feed fetch.
const UserAgent = "harborrs/0.1 (+https://github.com/kfet/harborrs)"

// Poller fetches feeds and writes results to a Store.
type Poller struct {
	Store  *store.Store
	Client *http.Client
	Parser *gofeed.Parser
	Now    func() time.Time
	// MaxBodyBytes caps how much body we read per feed (default 10MiB).
	MaxBodyBytes int64
}

// New builds a Poller with sensible defaults. Store is required.
func New(s *store.Store) *Poller {
	return &Poller{
		Store:        s,
		Client:       &http.Client{Timeout: 30 * time.Second, Transport: feedTransport()},
		Parser:       gofeed.NewParser(),
		Now:          time.Now,
		MaxBodyBytes: 10 * 1024 * 1024,
	}
}

// feedTransport builds the HTTP transport used for feed fetches. It
// deliberately forces HTTP/1.1 and disables connection keep-alive:
//
//   - HTTP/2: some CDNs (observed with Akamai-fronted feeds such as
//     CBC) accept the connection but then reset Go's reused HTTP/2
//     streams with INTERNAL_ERROR for requests originating from
//     datacenter IPs — so HTTP/2 polls fail ~100% while a one-shot
//     `curl` (HTTP/1.1, fresh connection) from the same host succeeds.
//     Setting TLSNextProto to a non-nil empty map disables HTTP/2.
//   - Keep-alive: feed polling makes one request per host per cycle and
//     cycles are a minute apart, so pooled connections are never reused
//     within their idle lifetime anyway. Closing each connection mirrors
//     the per-invocation `curl` behaviour that these CDNs tolerate and
//     avoids serving stale/poisoned connections on the next cycle.
//
// Proxy settings are taken from the environment (HTTP_PROXY / HTTPS_PROXY
// / NO_PROXY), so a feed that is only reachable from a residential egress
// can be routed through a proxy without code changes.
func feedTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableKeepAlives:     true,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// ErrCooldown is returned when Poll is called for a feed whose
// RetryAfter window has not yet elapsed. No HTTP request is made and
// no state is mutated.
var ErrCooldown = errors.New("poll: feed in 429/503 cooldown window")

// Poll fetches one feed and returns the number of new entries appended.
// State (ETag, Last-Modified, LastFetched, ErrorCount, RetryAfter) is
// persisted regardless of outcome — unless the feed is in a 429/503
// cooldown, in which case Poll returns (0, ErrCooldown) immediately.
func (p *Poller) Poll(ctx context.Context, feedURL string) (int, error) {
	fh := store.FeedHash(feedURL)
	st, err := p.Store.LoadFeedState(fh)
	if err != nil {
		return 0, fmt.Errorf("load state: %w", err)
	}
	if st.URL == "" {
		st.URL = feedURL
	}
	now := p.Now().UTC()
	if !st.RetryAfter.IsZero() && now.Before(st.RetryAfter) {
		return 0, ErrCooldown
	}
	st.LastFetched = now

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
		// 304 — clear error state; no scheduling side-effect.
		st.ErrorCount = 0
		st.LastError = ""
		st.RetryAfter = time.Time{}
		return 0, p.Store.SaveFeedState(fh, st)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// fall through
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		// Honour Retry-After when present; otherwise apply a short
		// default cooldown so a misbehaving server isn't hammered
		// every Refresher cycle.
		d := parseRetryAfter(resp.Header.Get("Retry-After"), now)
		if d <= 0 {
			d = defaultCooldown
		}
		st.ErrorCount++
		err := fmt.Errorf("http %d", resp.StatusCode)
		st.LastError = err.Error()
		st.RetryAfter = now.Add(d)
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

	parsed, err := p.Parser.ParseString(sanitizeXML(string(body)))
	if err != nil {
		return 0, p.recordErr(fh, &st, err)
	}

	entries := make([]store.Entry, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		// Decode HTML entities once at ingestion in *text* fields only
		// (Title, Author.Name). Summary / Content are HTML and stay raw.
		e := store.Entry{
			FeedHash:  fh,
			GUID:      it.GUID,
			Link:      it.Link,
			Title:     html.UnescapeString(it.Title),
			Summary:   it.Description,
			Content:   it.Content,
			FetchedAt: now,
		}
		if it.Author != nil {
			e.Author = html.UnescapeString(it.Author.Name)
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

	// Success — refresh conditional headers, reset error state.
	st.ETag = resp.Header.Get("ETag")
	st.LastModified = resp.Header.Get("Last-Modified")
	st.ErrorCount = 0
	st.LastError = ""
	st.RetryAfter = time.Time{}
	if err := p.Store.SaveFeedState(fh, st); err != nil {
		return len(added), err
	}
	return len(added), nil
}

// ResetCooldown clears any RetryAfter cooldown on a feed and persists.
// Used by `harborrs poll-once` which must force a poll of every feed
// regardless of cooldown state.
func (p *Poller) ResetCooldown(feedURL string) error {
	fh := store.FeedHash(feedURL)
	st, err := p.Store.LoadFeedState(fh)
	if err != nil {
		return err
	}
	if st.RetryAfter.IsZero() {
		return nil
	}
	st.RetryAfter = time.Time{}
	return p.Store.SaveFeedState(fh, st)
}

func (p *Poller) recordErr(fh string, st *store.FeedState, e error) error {
	st.ErrorCount++
	st.LastError = e.Error()
	if saveErr := p.Store.SaveFeedState(fh, *st); saveErr != nil {
		return saveErr
	}
	return e
}

// defaultCooldown is applied on 429/503 responses that omit Retry-After.
// Short enough that a transient blip doesn't pin a feed for hours, long
// enough that we don't retry on the very next 1-minute Refresher tick.
const defaultCooldown = 15 * time.Minute

// parseRetryAfter parses a Retry-After header (seconds or HTTP-date) and
// returns a duration from "now". Returns 0 if unparseable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// sanitizeXML removes byte values that are illegal in XML 1.0 — the C0
// control characters other than tab (0x09), LF (0x0A) and CR (0x0D).
// Go's encoding/xml (under gofeed) aborts on the first such byte, so a
// single stray U+0008 anywhere in an upstream feed would otherwise drop
// every item in it. We work at the byte level: in every ASCII-superset
// encoding (UTF-8, ISO-8859-x, Windows-125x) these byte values never
// appear inside a multi-byte sequence, so removing them cannot corrupt
// valid text. A UTF-16 document (identified by its BOM) is left
// untouched, since there low bytes are legitimate payload.
func sanitizeXML(s string) string {
	if len(s) >= 2 && ((s[0] == 0xFE && s[1] == 0xFF) || (s[0] == 0xFF && s[1] == 0xFE)) {
		return s
	}
	// Fast path: most feeds are clean — scan before allocating.
	bad := -1
	for i := 0; i < len(s); i++ {
		if illegalXMLByte(s[i]) {
			bad = i
			break
		}
	}
	if bad < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	b = append(b, s[:bad]...)
	for i := bad; i < len(s); i++ {
		if !illegalXMLByte(s[i]) {
			b = append(b, s[i])
		}
	}
	return string(b)
}

func illegalXMLByte(c byte) bool {
	return c < 0x20 && c != 0x09 && c != 0x0A && c != 0x0D
}
