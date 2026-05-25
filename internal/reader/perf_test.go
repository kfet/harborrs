package reader

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/store"
)

// BenchmarkReederLikeSync replays the Reeder access-log shape observed
// in prod (~12 stream/items/ids + ~6 stream/items/contents per sync over
// ~60 feeds / ~2000 entries) against the Reader handler chain so we can
// measure the per-call latency before/after the in-memory entry index.
func BenchmarkReederLikeSync(b *testing.B) {
	const numFeeds = 60
	const entriesPerFeed = 33

	dir := b.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	op := &memOPMLBench{}
	now := time.Now().UTC()
	for f := 0; f < numFeeds; f++ {
		u := "https://feed.example/" + strconv.Itoa(f) + ".xml"
		op.opml.Feeds = append(op.opml.Feeds, store.Feed{
			XMLURL: u,
			Title:  "Feed " + strconv.Itoa(f),
			Tags:   []string{"folder" + strconv.Itoa(f%5)},
		})
		fh := store.FeedHash(u)
		es := make([]store.Entry, entriesPerFeed)
		// ~6KB of synthetic HTML per entry — close to a typical article.
		body := strings.Repeat("<p>lorem ipsum dolor sit amet, consectetur adipiscing elit.</p>", 80)
		for i := 0; i < entriesPerFeed; i++ {
			es[i] = store.Entry{
				GUID:      "g" + strconv.Itoa(f) + "-" + strconv.Itoa(i),
				Link:      "https://feed.example/" + strconv.Itoa(f) + "/" + strconv.Itoa(i),
				Title:     "Entry " + strconv.Itoa(i),
				Content:   body,
				Published: now.Add(-time.Duration(f*entriesPerFeed+i) * time.Minute),
				FetchedAt: now,
			}
		}
		if _, err := st.AppendEntries(fh, es); err != nil {
			b.Fatal(err)
		}
	}

	srv := &Server{Store: st, OPML: op, Now: time.Now, MaxPage: 200}
	mux := http.NewServeMux()
	// Skip auth wrapper — that's not what we're measuring. Mount
	// handlers directly so the benchmark targets the data path.
	mux.HandleFunc("/reader/api/0/stream/items/ids", srv.handleItemsIDs)
	mux.HandleFunc("/reader/api/0/stream/items/contents", srv.handleItemsContents)
	mux.HandleFunc("/reader/api/0/unread-count", srv.handleUnreadCount)

	// Build a representative set of 100 long-form ids to fetch via
	// stream/items/contents. We pull them straight from the index so
	// the benchmark setup doesn't depend on the handler under test.
	allEntries := st.IndexedEntries(store.FeedHash(op.opml.Feeds[0].XMLURL))
	contentIDs := make([]string, 0, 100)
	for _, e := range allEntries {
		contentIDs = append(contentIDs, itemLongID(e.Hash))
		if len(contentIDs) >= 100 {
			break
		}
	}
	idsForm := url.Values{"s": {streamReadingList}, "n": {"200"}}.Encode()
	contentForm := url.Values{"i": contentIDs}.Encode()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 12 stream/items/ids
		for j := 0; j < 12; j++ {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/reader/api/0/stream/items/ids",
				strings.NewReader(idsForm))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_ = req.ParseForm()
			mux.ServeHTTP(rr, req)
		}
		// 6 stream/items/contents
		for j := 0; j < 6; j++ {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/reader/api/0/stream/items/contents",
				strings.NewReader(contentForm))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_ = req.ParseForm()
			mux.ServeHTTP(rr, req)
		}
		// 1 unread-count
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/reader/api/0/unread-count", nil)
		_ = req.ParseForm()
		mux.ServeHTTP(rr, req)
	}
}

// memOPMLBench is a benchmark-local OPMLProvider implementation —
// reader_test.go already defines a memOPML for unit tests, but it's in
// the same package so we use a distinct name to avoid collision.
type memOPMLBench struct {
	opml store.OPML
}

func (m *memOPMLBench) Load() (*store.OPML, error) { c := m.opml; return &c, nil }
func (m *memOPMLBench) Save(o *store.OPML) error   { m.opml = *o; return nil }

// BenchmarkListVsIndexed contrasts the disk-backed ListEntries path
// (what the Reader handlers used pre-index) against the new in-memory
// IndexedEntries path on the same store. Run with -benchtime=1s to
// confirm the order-of-magnitude speed-up.
func BenchmarkListVsIndexed(b *testing.B) {
	dir := b.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	const numFeeds = 60
	const entriesPerFeed = 33
	now := time.Now().UTC()
	feeds := make([]string, numFeeds)
	for f := 0; f < numFeeds; f++ {
		u := "https://feed.example/" + strconv.Itoa(f) + ".xml"
		fh := store.FeedHash(u)
		feeds[f] = fh
		es := make([]store.Entry, entriesPerFeed)
		body := strings.Repeat("<p>lorem ipsum</p>", 80)
		for i := 0; i < entriesPerFeed; i++ {
			es[i] = store.Entry{
				GUID:      "g" + strconv.Itoa(f) + "-" + strconv.Itoa(i),
				Title:     "Entry",
				Content:   body,
				Published: now.Add(-time.Duration(f*entriesPerFeed+i) * time.Minute),
				FetchedAt: now,
			}
		}
		if _, err := st.AppendEntries(fh, es); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("ListEntries_AllFeeds", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, fh := range feeds {
				_, _ = st.ListEntries(fh)
			}
		}
	})
	b.Run("IndexedEntries_AllFeeds", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, fh := range feeds {
				_ = st.IndexedEntries(fh)
			}
		}
	})
}
