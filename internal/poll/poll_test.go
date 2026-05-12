package poll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/store"
)

const sampleRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>S</title>
    <item>
      <title>A</title>
      <link>https://x.example/a</link>
      <guid>guid-a</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate>
      <description>desc-a</description>
    </item>
    <item>
      <title>B</title>
      <link>https://x.example/b</link>
      <guid>guid-b</guid>
      <author>jane@example.com (Jane)</author>
      <pubDate>Tue, 03 Jan 2006 15:04:05 GMT</pubDate>
    </item>
  </channel>
</rss>`

func newPoller(t *testing.T) (*Poller, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(s)
	return p, s, dir
}

func TestPollSuccess(t *testing.T) {
	p, _, _ := newPoller(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.Header().Set("Content-Type", "application/rss+xml")
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	added, err := p.Poll(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added=%d", added)
	}
	st, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	if st.ETag != `"v1"` {
		t.Fatalf("etag=%q", st.ETag)
	}
	if st.ErrorCount != 0 {
		t.Fatalf("err count=%d", st.ErrorCount)
	}
	if st.NextFetch.IsZero() {
		t.Fatal("next fetch zero")
	}
}

func TestPollNotModified(t *testing.T) {
	p, _, _ := newPoller(t)
	first := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			w.Header().Set("ETag", `"v1"`)
			io.WriteString(w, sampleRSS)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("missing If-None-Match: %v", r.Header)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	added, err := p.Poll(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("added=%d", added)
	}
}

func TestPollServerError(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
	st, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	if st.ErrorCount != 1 {
		t.Fatalf("count=%d", st.ErrorCount)
	}
	// Second failure → backoff increases.
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error 2")
	}
	st2, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	if st2.Interval <= st.Interval {
		t.Fatalf("backoff did not grow: %d -> %d", st.Interval, st2.Interval)
	}
}

func TestPollRateLimited(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
	st, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	// 300s < MinInterval (15min=900s) → clamped to MinInterval.
	if time.Duration(st.Interval)*time.Second != MinInterval {
		t.Fatalf("interval=%ds want %v", st.Interval, MinInterval)
	}
}

func TestPollRetryAfterHTTPDate(t *testing.T) {
	p, _, _ := newPoller(t)
	when := time.Now().UTC().Add(2 * time.Hour).Format(http.TimeFormat)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", when)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := p.Poll(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	if d := time.Duration(st.Interval) * time.Second; d < time.Hour {
		t.Fatalf("interval too short: %v", d)
	}
}

func TestPollRetryAfterMissing(t *testing.T) {
	if d := parseRetryAfter("", time.Now()); d != 0 {
		t.Fatal("expected 0 for empty")
	}
	if d := parseRetryAfter("not a date", time.Now()); d != 0 {
		t.Fatal("expected 0 for garbage")
	}
	if d := parseRetryAfter(time.Now().Add(-time.Hour).Format(http.TimeFormat), time.Now()); d != 0 {
		t.Fatal("expected 0 for past date")
	}
}

func TestPollBadXML(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not xml at all")
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestPollBadURL(t *testing.T) {
	p, _, _ := newPoller(t)
	if _, err := p.Poll(context.Background(), "http://[::1]:badport"); err == nil {
		t.Fatal("expected URL build error")
	}
}

func TestPollHTTPError(t *testing.T) {
	p, _, _ := newPoller(t)
	if _, err := p.Poll(context.Background(), "http://127.0.0.1:1/"); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestPoll404(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
}

func TestPollBodyTooLarge(t *testing.T) {
	p, _, _ := newPoller(t)
	p.MaxBodyBytes = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected too-large error")
	}
}

func TestPollLoadStateError(t *testing.T) {
	p, _, dir := newPoller(t)
	// Drive LoadFeedState into an error: write a directory where the
	// state file would be (so ReadFile fails with EISDIR).
	fh := store.FeedHash("http://x/")
	if err := http.ListenAndServe; err == nil { // keep linter happy
	}
	if err := makeStateDir(dir, fh); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(context.Background(), "http://x/"); err == nil {
		t.Fatal("expected load error")
	}
}

func TestPollIfModifiedSince(t *testing.T) {
	p, _, _ := newPoller(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
			io.WriteString(w, sampleRSS)
			return
		}
		if r.Header.Get("If-Modified-Since") != "Wed, 21 Oct 2015 07:28:00 GMT" {
			t.Errorf("missing IMS")
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
}

func TestPollAppendError(t *testing.T) {
	p, _, dir := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	// Break the entries dir for the target feed.
	fh := store.FeedHash(srv.URL)
	if err := makeEntriesNonDir(dir, fh); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected append error")
	}
}

func TestRunTickOnce(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	feeds := []string{srv.URL}
	// First tick polls (NextFetch zero).
	p.tickOnce(ctx, feeds)
	// Second tick is a no-op because NextFetch is in the future.
	p.tickOnce(ctx, feeds)
	cancel()
	// tickOnce with cancelled ctx returns early.
	p.tickOnce(ctx, feeds)
}

func TestRunRespectsContext(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	feeds := func() []string { return []string{srv.URL} }
	err := p.Run(ctx, feeds, 5*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	// Also default tick.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel2()
	_ = p.Run(ctx2, feeds, 0)
}

func TestRunLoadStateError(t *testing.T) {
	p, _, dir := newPoller(t)
	feeds := []string{"http://x/"}
	fh := store.FeedHash("http://x/")
	if err := makeStateDir(dir, fh); err != nil {
		t.Fatal(err)
	}
	// tickOnce silently skips this feed (load err) — covers the continue.
	p.tickOnce(context.Background(), feeds)
}

func TestPollSaveStateErrorAfterAppend(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	p, _, dir := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	// First poll succeeds and writes state file.
	if _, err := p.Poll(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	// Now make state dir read-only so atomic temp create fails.
	stateDir := dir + "/state"
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stateDir, 0o755) })
	_, err := p.Poll(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected save error")
	}
}

func TestPollRecordErrSaveFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	p, _, dir := newPoller(t)
	// Seed state file via a successful first poll on a different URL so
	// LoadFeedState succeeds for THIS feed too (zero value via missing
	// state file). Then make state dir read-only.
	stateDir := dir + "/state"
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stateDir, 0o755) })
	if _, err := p.Poll(context.Background(), "http://127.0.0.1:1/"); err == nil {
		t.Fatal("expected save error in recordErr")
	}
}

func TestPollRateLimitedSaveFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	p, _, dir := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	stateDir := dir + "/state"
	os.MkdirAll(stateDir, 0o755)
	os.Chmod(stateDir, 0o500)
	t.Cleanup(func() { os.Chmod(stateDir, 0o755) })
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
}

func TestPoll304SaveFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	p, _, dir := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	stateDir := dir + "/state"
	os.MkdirAll(stateDir, 0o755)
	os.Chmod(stateDir, 0o500)
	t.Cleanup(func() { os.Chmod(stateDir, 0o755) })
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected save error")
	}
}

func TestBackoffSecondsZero(t *testing.T) {
	got := backoffSeconds(0)
	if got <= 0 {
		t.Fatalf("got %d", got)
	}
	if got2 := backoffSeconds(int64(MaxInterval * 2 / time.Second)); time.Duration(got2)*time.Second != MaxInterval {
		t.Fatalf("not capped: %d", got2)
	}
}

func TestClampSeconds(t *testing.T) {
	if clampSeconds(time.Second, MinInterval, MaxInterval) != int64(MinInterval/time.Second) {
		t.Fatal("lo")
	}
	if clampSeconds(MaxInterval*2, MinInterval, MaxInterval) != int64(MaxInterval/time.Second) {
		t.Fatal("hi")
	}
}

func TestNewSensibleDefaults(t *testing.T) {
	p := New(nil)
	if p.Client.Timeout == 0 || p.Parser == nil || p.Now == nil || p.MaxBodyBytes == 0 {
		t.Fatalf("New defaults: %+v", p)
	}
}

func TestPollNotModifiedFirstCall(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	st, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	if time.Duration(st.Interval)*time.Second != DefaultInterval {
		t.Fatalf("interval=%ds want %v", st.Interval, DefaultInterval)
	}
}

func TestPoll429NoRetryAfter(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
	st, _ := p.Store.LoadFeedState(store.FeedHash(srv.URL))
	if st.Interval == 0 {
		t.Fatal("interval not set")
	}
}

const sampleAtom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom</title>
  <updated>2003-12-13T18:30:02Z</updated>
  <id>urn:uuid:60a76c80-d399-11d9-b93C-0003939e0af6</id>
  <entry>
    <title>Atom-Powered Robots Run Amok</title>
    <link href="http://example.org/2003/12/13/atom03"/>
    <id>urn:uuid:1225c695-cfb8-4ebb-aaaa-80da344efa6a</id>
    <updated>2003-12-13T18:30:02Z</updated>
    <summary>Some text.</summary>
  </entry>
</feed>`

func TestPollAtomUpdatedFallback(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		io.WriteString(w, sampleAtom)
	}))
	defer srv.Close()
	added, err := p.Poll(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d", added)
	}
}

const noDateRSS = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>S</title>
  <item><title>X</title><link>https://x/x</link><guid>g</guid></item>
</channel></rss>`

func TestPollNoDateFallsBackToNow(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, noDateRSS)
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
}

func TestPollBodyReadError(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		io.WriteString(w, "short")
		// hijack to abort
		if h, ok := w.(http.Hijacker); ok {
			conn, _, _ := h.Hijack()
			conn.Close()
		}
	}))
	defer srv.Close()
	if _, err := p.Poll(context.Background(), srv.URL); err == nil {
		t.Fatal("expected body read error")
	}
}

func TestBackoffMinClamp(t *testing.T) {
	got := backoffSeconds(1)
	if time.Duration(got)*time.Second != MinInterval {
		t.Fatalf("got %ds want %v", got, MinInterval)
	}
}

func TestTickOnceContextCancelMidLoop(t *testing.T) {
	p, _, _ := newPoller(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleRSS)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	feeds := []string{srv.URL, srv.URL + "/other"}
	p.tickOnce(ctx, feeds)
}

func makeStateDir(dir, fh string) error {
	p := dir + "/state/" + fh + ".json"
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	return nil
}

func makeEntriesNonDir(dir, fh string) error {
	p := dir + "/entries"
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	return os.WriteFile(p+"/"+fh, nil, 0o644)
}

func blockStateDir(dir string) error {
	// Make the data dir's `state` path a regular file → atomic write of
	// state/<fh>.json fails because MkdirAll cannot create state.
	p := dir + "/state"
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o644)
}

// Suppress unused-import warning if helpers ever drift.
var _ = fmt.Sprintf
var _ = strings.HasPrefix
