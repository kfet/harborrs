package reader

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/store"
)

// memOPML is an in-memory OPMLProvider. Failure modes can be injected via
// loadErr / saveErr.
type memOPML struct {
	mu      sync.Mutex
	opml    store.OPML
	loadErr error
	saveErr error
}

func (m *memOPML) Load() (*store.OPML, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	cp := m.opml
	cp.Feeds = append([]store.Feed{}, m.opml.Feeds...)
	return &cp, nil
}
func (m *memOPML) Save(o *store.OPML) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.opml = *o
	m.opml.Feeds = append([]store.Feed{}, o.Feeds...)
	return nil
}

// fixture builds a Server + ServerMux + valid API token + memOPML.
func fixture(t *testing.T) (*Server, http.Handler, string, *memOPML, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pwHash, _ := auth.HashPassword("p")
	cfg := auth.Config{Username: "u", PasswordHash: pwHash}
	as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), cfg)
	tok, _ := as.IssueAPIToken("u", "p")
	op := &memOPML{}
	s := New(st, as, op)
	mux := http.NewServeMux()
	handler := s.Routes(mux)
	return s, handler, tok, op, st
}

func do(t *testing.T, handler http.Handler, method, path, tok string, body url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if tok != "" {
		r.Header.Set("Authorization", "GoogleLogin auth="+tok)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestClientLogin(t *testing.T) {
	_, mux, _, _, _ := fixture(t)
	body := url.Values{"Email": {"u"}, "Passwd": {"p"}}
	w := do(t, mux, "POST", "/accounts/ClientLogin", "", body)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Auth=") {
		t.Fatal("missing Auth")
	}
	// lowercase params accepted
	body2 := url.Values{"email": {"u"}, "passwd": {"p"}}
	if w := do(t, mux, "POST", "/accounts/ClientLogin", "", body2); w.Code != 200 {
		t.Fatalf("lower code=%d", w.Code)
	}
	// Bad creds → 401
	bad := url.Values{"Email": {"u"}, "Passwd": {"wrong"}}
	if w := do(t, mux, "POST", "/accounts/ClientLogin", "", bad); w.Code != 401 {
		t.Fatalf("bad code=%d", w.Code)
	}
}

func TestAuthRequired(t *testing.T) {
	_, mux, _, _, _ := fixture(t)
	if w := do(t, mux, "GET", "/reader/api/0/user-info", "", nil); w.Code != 401 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestToken(t *testing.T) {
	_, mux, tok, _, _ := fixture(t)
	w := do(t, mux, "GET", "/reader/api/0/token", tok, nil)
	if w.Code != 200 || w.Body.String() != tok {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestUserInfo(t *testing.T) {
	_, mux, tok, _, _ := fixture(t)
	w := do(t, mux, "GET", "/reader/api/0/user-info", tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"userName":"u"`) {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestSubscriptionListAndTagList(t *testing.T) {
	_, mux, tok, op, _ := fixture(t)
	op.opml.Feeds = []store.Feed{
		{XMLURL: "https://a/feed", Title: "A", Folder: "News", HTMLURL: "https://a"},
		{XMLURL: "https://b/feed", Title: "B"},
	}
	w := do(t, mux, "GET", "/reader/api/0/subscription/list", tok, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"feed/https://a/feed"`) {
		t.Fatalf("body=%s", w.Body.String())
	}
	w2 := do(t, mux, "GET", "/reader/api/0/tag/list", tok, nil)
	if !strings.Contains(w2.Body.String(), "user/-/label/News") {
		t.Fatalf("tag body=%s", w2.Body.String())
	}
}

func TestSubscriptionListLoadErr(t *testing.T) {
	_, mux, tok, op, _ := fixture(t)
	op.loadErr = errBoom
	for _, p := range []string{
		"/reader/api/0/subscription/list",
		"/reader/api/0/tag/list",
		"/reader/api/0/unread-count",
	} {
		w := do(t, mux, "GET", p, tok, nil)
		if w.Code != 500 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
}

func TestSubscriptionEdit(t *testing.T) {
	_, mux, tok, op, _ := fixture(t)
	body := url.Values{"ac": {"subscribe"}, "s": {"feed/https://x/y"}, "t": {"X"}, "a": {"user/-/label/F"}}
	w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, body)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if len(op.opml.Feeds) != 1 || op.opml.Feeds[0].Folder != "F" {
		t.Fatalf("opml=%+v", op.opml.Feeds)
	}
	// subscribe without title falls back to url
	body2 := url.Values{"ac": {"subscribe"}, "s": {"feed/https://y/y"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, body2); w.Code != 200 {
		t.Fatalf("w=%d", w.Code)
	}
	// edit
	edit := url.Values{"ac": {"edit"}, "s": {"feed/https://x/y"}, "t": {"NewTitle"}, "a": {"user/-/label/F2"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, edit); w.Code != 200 {
		t.Fatalf("w=%d", w.Code)
	}
	if op.opml.Feeds[0].Title != "NewTitle" || op.opml.Feeds[0].Folder != "F2" {
		t.Fatalf("not edited: %+v", op.opml.Feeds[0])
	}
	// edit with r removing folder
	edit2 := url.Values{"ac": {"edit"}, "s": {"feed/https://x/y"}, "r": {"user/-/label/F2"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, edit2); w.Code != 200 {
		t.Fatalf("w=%d", w.Code)
	}
	if op.opml.Feeds[0].Folder != "" {
		t.Fatalf("folder not cleared: %+v", op.opml.Feeds[0])
	}
	// edit on missing
	miss := url.Values{"ac": {"edit"}, "s": {"feed/missing"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, miss); w.Code != 400 {
		t.Fatalf("miss code=%d", w.Code)
	}
	// unsubscribe
	un := url.Values{"ac": {"unsubscribe"}, "s": {"feed/https://x/y"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, un); w.Code != 200 {
		t.Fatalf("un=%d", w.Code)
	}
	// missing s
	miss2 := url.Values{"ac": {"subscribe"}, "s": {""}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, miss2); w.Code != 400 {
		t.Fatalf("missing s code=%d", w.Code)
	}
	// bad ac
	bad := url.Values{"ac": {"nope"}, "s": {"feed/x"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, bad); w.Code != 400 {
		t.Fatalf("bad ac=%d", w.Code)
	}
}

func TestSubscriptionEditLoadAndSaveErr(t *testing.T) {
	_, mux, tok, op, _ := fixture(t)
	op.loadErr = errBoom
	body := url.Values{"ac": {"subscribe"}, "s": {"feed/x"}}
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, body); w.Code != 500 {
		t.Fatalf("load err code=%d", w.Code)
	}
	op.loadErr = nil
	op.saveErr = errBoom
	if w := do(t, mux, "POST", "/reader/api/0/subscription/edit", tok, body); w.Code != 500 {
		t.Fatalf("save err code=%d", w.Code)
	}
}

func TestQuickAdd(t *testing.T) {
	_, mux, tok, op, _ := fixture(t)
	body := url.Values{"quickadd": {"https://q/feed"}}
	w := do(t, mux, "POST", "/reader/api/0/subscription/quickadd", tok, body)
	if w.Code != 200 || len(op.opml.Feeds) != 1 {
		t.Fatalf("code=%d feeds=%+v body=%s", w.Code, op.opml.Feeds, w.Body.String())
	}
	// missing
	if w := do(t, mux, "POST", "/reader/api/0/subscription/quickadd", tok, url.Values{}); w.Code != 400 {
		t.Fatalf("missing=%d", w.Code)
	}
	// load err
	op.loadErr = errBoom
	if w := do(t, mux, "POST", "/reader/api/0/subscription/quickadd", tok, body); w.Code != 500 {
		t.Fatalf("load err=%d", w.Code)
	}
	op.loadErr = nil
	op.saveErr = errBoom
	if w := do(t, mux, "POST", "/reader/api/0/subscription/quickadd", tok, body); w.Code != 500 {
		t.Fatalf("save err=%d", w.Code)
	}
}

// seedFeed populates one feed with N entries in the store, registers it
// in the OPML and returns the feed url.
func seedFeed(t *testing.T, op *memOPML, st *store.Store, count int, folder string) string {
	t.Helper()
	u := "https://feed.example/" + folder
	op.opml.Feeds = append(op.opml.Feeds, store.Feed{XMLURL: u, Title: "F", Folder: folder, HTMLURL: "https://feed.example"})
	fh := store.FeedHash(u)
	now := time.Now().UTC()
	es := make([]store.Entry, count)
	for i := 0; i < count; i++ {
		es[i] = store.Entry{
			GUID:      "g" + strconv.Itoa(i),
			Link:      "https://feed.example/" + strconv.Itoa(i),
			Title:     "Item " + strconv.Itoa(i),
			Content:   "content " + strconv.Itoa(i),
			Summary:   "summ " + strconv.Itoa(i),
			Published: now.Add(time.Duration(-i) * time.Minute),
			FetchedAt: now,
		}
	}
	if _, err := st.AppendEntries(fh, es); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestStreamContentsAndPagination(t *testing.T) {
	srv, mux, tok, op, st := fixture(t)
	srv.MaxPage = 20
	url := seedFeed(t, op, st, 10, "F")
	w := do(t, mux, "GET", "/reader/api/0/stream/contents/"+("feed/"+url)+"?n=2", tok, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp streamResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 2 || resp.Continuation == "" {
		t.Fatalf("got items=%d cont=%q", len(resp.Items), resp.Continuation)
	}
	// follow continuation to last page
	w2 := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+url+"?n=15&c="+resp.Continuation, tok, nil)
	var r2 streamResponse
	json.Unmarshal(w2.Body.Bytes(), &r2)
	if r2.Continuation != "" {
		t.Fatalf("expected last page, got cont %q", r2.Continuation)
	}
}

func TestStreamLabelStarredReadAndDefault(t *testing.T) {
	srv, mux, tok, op, st := fixture(t)
	srv.MaxPage = 50
	u := seedFeed(t, op, st, 5, "Cat")
	// Star one, read another.
	es, _ := st.ListEntries(store.FeedHash(u))
	st.SetStarred(es[0].Hash, true)
	st.SetRead(es[1].Hash, true)
	// label/Cat → all
	w := do(t, mux, "GET", "/reader/api/0/stream/contents/user/-/label/Cat", tok, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	// starred → 1
	w = do(t, mux, "GET", "/reader/api/0/stream/contents/user/-/state/com.google/starred", tok, nil)
	var resp streamResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("starred items=%d", len(resp.Items))
	}
	// read → 1
	w = do(t, mux, "GET", "/reader/api/0/stream/contents/user/-/state/com.google/read", tok, nil)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("read items=%d", len(resp.Items))
	}
	// reading-list (default) → 4 unread
	w = do(t, mux, "GET", "/reader/api/0/stream/contents/user/-/state/com.google/reading-list", tok, nil)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 4 {
		t.Fatalf("unread=%d", len(resp.Items))
	}
}

func TestStreamContentsLoadErr(t *testing.T) {
	_, mux, tok, op, _ := fixture(t)
	op.loadErr = errBoom
	for _, p := range []string{
		"/reader/api/0/stream/contents/feed/x",
		"/reader/api/0/stream/items/ids",
		"/reader/api/0/stream/items/contents",
	} {
		w := do(t, mux, "POST", p, tok, url.Values{"i": {"abc"}})
		if w.Code != 500 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
}

func TestStreamContentsBadContinuation(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 2, "F")
	bad := base64.RawURLEncoding.EncodeToString([]byte("not-json"))
	if w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u+"?c="+bad, tok, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u+"?c=$$notb64$$", tok, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	// n overflow → clamped
	if w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u+"?n=10000", tok, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	// n garbage
	if w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u+"?n=oops", tok, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestItemsIDs(t *testing.T) {
	srv, mux, tok, op, st := fixture(t)
	srv.MaxPage = 3
	seedFeed(t, op, st, 5, "F")
	w := do(t, mux, "GET", "/reader/api/0/stream/items/ids", tok, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	// explicit n
	w = do(t, mux, "GET", "/reader/api/0/stream/items/ids?s="+streamReadingList+"&n=2", tok, nil)
	if !strings.Contains(w.Body.String(), `"itemRefs"`) {
		t.Fatalf("body=%s", w.Body.String())
	}
	// bad n + huge n
	do(t, mux, "GET", "/reader/api/0/stream/items/ids?n=oops", tok, nil)
	do(t, mux, "GET", "/reader/api/0/stream/items/ids?n=99999", tok, nil)
}

func TestItemsContents(t *testing.T) {
	srv, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 2, "F")
	es, _ := st.ListEntries(store.FeedHash(u))
	body := url.Values{"i": {itemID(es[0].Hash), itemID(es[1].Hash)}}
	w := do(t, mux, "POST", "/reader/api/0/stream/items/contents", tok, body)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp streamResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 2 {
		t.Fatalf("items=%d", len(resp.Items))
	}
	// empty list → empty items
	if w := do(t, mux, "POST", "/reader/api/0/stream/items/contents", tok, url.Values{}); w.Code != 200 {
		t.Fatalf("empty code=%d", w.Code)
	}
	_ = srv
}

func TestEditTag(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 2, "F")
	es, _ := st.ListEntries(store.FeedHash(u))
	body := url.Values{
		"i": {itemID(es[0].Hash)},
		"a": {stateReadID, stateStarredID},
	}
	w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !st.EntryState(es[0].Hash).Read || !st.EntryState(es[0].Hash).Starred {
		t.Fatal("flags not set")
	}
	// remove
	body2 := url.Values{"i": {itemID(es[0].Hash)}, "r": {stateReadID, stateStarredID}}
	if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body2); w.Code != 200 {
		t.Fatalf("rem code=%d", w.Code)
	}
	// missing i
	if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, url.Values{}); w.Code != 400 {
		t.Fatalf("missing=%d", w.Code)
	}
	// unknown state → no-op, still 200
	body3 := url.Values{"i": {itemID(es[0].Hash)}, "a": {"user/-/label/F"}}
	if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body3); w.Code != 200 {
		t.Fatalf("unknown=%d", w.Code)
	}
}

func TestMarkAllRead(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 3, "F")
	body := url.Values{"s": {"feed/" + u}}
	if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, body); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	es, _ := st.ListEntries(store.FeedHash(u))
	for _, e := range es {
		if !st.EntryState(e.Hash).Read {
			t.Fatalf("entry %s not read", e.Hash)
		}
	}
	// ts upper bound future → marks (already marked, idempotent).
	tsBody := url.Values{
		"s":  {"feed/" + u},
		"ts": {strconv.FormatInt(time.Now().Add(time.Hour).UnixMicro(), 10)},
	}
	if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, tsBody); w.Code != 200 {
		t.Fatalf("ts=%d", w.Code)
	}
	// ts way in the past → skips most
	tsBody2 := url.Values{
		"s":  {"feed/" + u},
		"ts": {strconv.FormatInt(time.Now().Add(-365*24*time.Hour).UnixMicro(), 10)},
	}
	if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, tsBody2); w.Code != 200 {
		t.Fatalf("ts2=%d", w.Code)
	}
	// missing s
	if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, url.Values{}); w.Code != 400 {
		t.Fatalf("missing=%d", w.Code)
	}
	// load err
	op.loadErr = errBoom
	body2 := url.Values{"s": {"feed/" + u}}
	if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, body2); w.Code != 500 {
		t.Fatalf("load=%d", w.Code)
	}
}

func TestUnreadCount(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	seedFeed(t, op, st, 4, "F")
	w := do(t, mux, "GET", "/reader/api/0/unread-count", tok, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"count":4`) {
		t.Fatalf("body=%s", w.Body.String())
	}
}

// Drive errors from ListEntries by corrupting an entries file.
func TestListEntriesErrorPropagates(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 1, "F")
	fh := store.FeedHash(u)
	feedDir := filepath.Join(st.Dir, "entries", fh)
	// Corrupt current.ndjson
	_ = io.Discard
	ndjson := filepath.Join(feedDir, "current.ndjson")
	if err := writeFileAtomically(t, ndjson, "this is not json\n"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/reader/api/0/stream/contents/feed/" + u,
		"/reader/api/0/stream/items/contents",
		"/reader/api/0/unread-count",
	} {
		method := "GET"
		var body url.Values
		if strings.HasSuffix(p, "items/contents") {
			method = "POST"
			body = url.Values{"i": {"x"}}
		}
		w := do(t, mux, method, p, tok, body)
		if w.Code != 500 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
}

func TestItemIDRoundtrip(t *testing.T) {
	h := "abcdef0123456789abcd"
	if got := itemIDToHash(itemID(h)); got != h {
		t.Fatalf("got %q", got)
	}
	// long-form decimal — accepts but returns hex prefix
	dec := strconv.FormatInt(1234567890123, 10)
	if got := itemIDToHash(dec); len(got) != 20 {
		t.Fatalf("dec got %q", got)
	}
	// otherwise: raw passthrough
	if got := itemIDToHash("plainhash"); got != "plainhash" {
		t.Fatalf("got %q", got)
	}
}

func TestReverseFeedURLNotFound(t *testing.T) {
	srv, _, _, op, _ := fixture(t)
	op.opml.Feeds = []store.Feed{{XMLURL: "https://known/feed"}}
	o, _ := op.Load()
	items := srv.toStreamItems([]store.Entry{
		{Hash: "h1", FeedHash: "unknown", Title: "T"},
	}, o)
	if len(items) != 1 {
		t.Fatal(items)
	}
}

func TestReverseFeedURLNil(t *testing.T) {
	if got := reverseFeedURL(nil, "x"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// brokenResponseWriter fails on Write.
type brokenResponseWriter struct{ h http.Header }

func (b *brokenResponseWriter) Header() http.Header { return b.h }
func (b *brokenResponseWriter) Write(p []byte) (int, error) {
	return 0, &fakeErr{}
}
func (b *brokenResponseWriter) WriteHeader(int) {}

type fakeErr struct{}

func (fakeErr) Error() string { return "fake" }

func TestWriteJSONEncodeError(t *testing.T) {
	w := &brokenResponseWriter{h: http.Header{}}
	writeJSON(w, map[string]any{"x": 1})
}

func TestCollectStreamEmpty(t *testing.T) {
	srv, _, _, op, _ := fixture(t)
	es, err := srv.collectStream("feed/none")
	if err != nil || len(es) != 0 {
		t.Fatalf("es=%v err=%v", es, err)
	}
	_ = op
}

func TestStreamLabelFilterSkip(t *testing.T) {
	// Two feeds, one in folder Cat and one in folder Dog. Listing stream
	// label/Cat must skip the Dog feed (covers the `if !filter(f)` body).
	_, mux, tok, op, st := fixture(t)
	seedFeed(t, op, st, 1, "Cat")
	seedFeed(t, op, st, 1, "Dog")
	w := do(t, mux, "GET", "/reader/api/0/stream/contents/user/-/label/Cat", tok, nil)
	var resp streamResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("items=%d", len(resp.Items))
	}
}

func TestUnreadCountSomeRead(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 3, "F")
	es, _ := st.ListEntries(store.FeedHash(u))
	st.SetRead(es[0].Hash, true)
	w := do(t, mux, "GET", "/reader/api/0/unread-count", tok, nil)
	if !strings.Contains(w.Body.String(), `"count":2`) {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestItemsIDsClampN(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	seedFeed(t, op, st, 2, "F")
	// n > entries → clamped down to len.
	w := do(t, mux, "GET", "/reader/api/0/stream/items/ids?n=50", tok, nil)
	if !strings.Contains(w.Body.String(), `"itemRefs"`) {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestSummaryFallbackWhenContentEmpty(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := "https://no-content.example/feed"
	op.opml.Feeds = []store.Feed{{XMLURL: u, Title: "X"}}
	fh := store.FeedHash(u)
	now := time.Now().UTC()
	st.AppendEntries(fh, []store.Entry{
		{GUID: "g", Link: "https://no-content.example/1", Title: "T", Summary: "summary-only", Published: now, FetchedAt: now},
	})
	w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u, tok, nil)
	if !strings.Contains(w.Body.String(), "summary-only") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

// Drive Store errors for label/starred/read/default gather branches by
// corrupting one feed's entries file.
func TestStreamGatherErrors(t *testing.T) {
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 1, "F")
	feedDir := filepath.Join(st.Dir, "entries", store.FeedHash(u))
	if err := os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{
		"user/-/label/F",
		streamStarred,
		streamRead,
		streamReadingList,
	} {
		w := do(t, mux, "GET", "/reader/api/0/stream/contents/"+sid, tok, nil)
		if w.Code != 500 {
			t.Fatalf("%s code=%d body=%s", sid, w.Code, w.Body.String())
		}
		// also mark-all-as-read with the same stream
		body := url.Values{"s": {sid}}
		if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, body); w.Code != 500 {
			t.Fatalf("mar %s code=%d", sid, w.Code)
		}
	}
}

// Force SetRead/SetStarred to fail by chmod 0500 on data dir.
func TestEditTagAndMarkAllSetReadFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 1, "F")
	es, _ := st.ListEntries(store.FeedHash(u))
	// Seed in-memory flags so the "remove" path has something to clear.
	st.SetRead(es[0].Hash, true)
	// Make read.log + starred.log read-only so subsequent appends fail.
	if err := os.Chmod(filepath.Join(st.Dir, "read.log"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(st.Dir, "read.log"), 0o644) })
	if err := os.Chmod(st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(st.Dir, 0o755) })
	// edit-tag: add starred → SetStarred fails (starred.log doesn't exist,
	// O_CREATE in chmod-0500 dir → EACCES).
	body2 := url.Values{"i": {itemID(es[0].Hash)}, "a": {stateStarredID}}
	if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body2); w.Code != 500 {
		t.Fatalf("add star code=%d", w.Code)
	}
	// edit-tag: remove read (state is currently Read=true; SetRead(false)
	// tries to append to read.log which is 0444 → EACCES).
	body3 := url.Values{"i": {itemID(es[0].Hash)}, "r": {stateReadID}}
	if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body3); w.Code != 500 {
		t.Fatalf("rem read code=%d", w.Code)
	}
	// edit-tag: add read on a *different* item.
	body1 := url.Values{"i": {itemID("freshhash000000000000")}, "a": {stateReadID}}
	if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body1); w.Code != 500 {
		t.Fatalf("add read code=%d", w.Code)
	}
	// mark-all-as-read against the (sole) feed: its lone entry is already
	// read in memory, so we use a fresh feed with a fresh entry.
	op.opml.Feeds = append(op.opml.Feeds, store.Feed{XMLURL: "https://other.example/feed", Title: "Other"})
	// Can't AppendEntries here — chmod blocks file creation. Use a hash
	// directly: pretend to mark-all by hitting an empty stream which now
	// fails inside ListEntries (no current.ndjson in a dir we can't
	// create). Easier: just hit the existing feed and exercise the
	// already-read continue path → returns 200.
	_ = u
}

func TestMarkAllAsReadSetReadFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	_, mux, tok, op, st := fixture(t)
	u := seedFeed(t, op, st, 1, "F")
	if err := os.Chmod(st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(st.Dir, 0o755) })
	body := url.Values{"s": {"feed/" + u}}
	if w := do(t, mux, "POST", "/reader/api/0/mark-all-as-read", tok, body); w.Code != 500 {
		t.Fatalf("mark-all code=%d", w.Code)
	}
}

// --- helpers ---

var errBoom = boom("boom")

type boom string

func (b boom) Error() string { return string(b) }

// writeFileAtomically replaces the file directly (test helper).
func writeFileAtomically(t *testing.T, p, content string) error {
	t.Helper()
	return os.WriteFile(p, []byte(content), 0o644)
}
