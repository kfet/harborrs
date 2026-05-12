package ui

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/reader"
	"github.com/kfet/harborrs/internal/store"
)

type memOPML struct {
	op      store.OPML
	loadErr error
	saveErr error
}

func (m *memOPML) Load() (*store.OPML, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	cp := m.op
	cp.Feeds = append([]store.Feed{}, m.op.Feeds...)
	return &cp, nil
}
func (m *memOPML) Save(o *store.OPML) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.op = *o
	return nil
}

// Compile-time check our memOPML satisfies the reader's interface too —
// keeps the two packages aligned.
var _ reader.OPMLProvider = (*memOPML)(nil)

func fixture(t *testing.T) (*Server, *http.ServeMux, *store.Store, *memOPML, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pwHash, _ := auth.HashPassword("p")
	as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), auth.Config{Username: "u", PasswordHash: pwHash})
	op := &memOPML{}
	overrideDir := filepath.Join(dir, "cfg")
	srv, err := New(st, as, op, "dark", overrideDir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Routes(mux)
	tok, _ := as.IssueSession("u", "p")
	return srv, mux, st, op, tok, dir
}

func req(method, path, tok string, form url.Values) *http.Request {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if tok != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	}
	return r
}

func do(mux *http.ServeMux, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestLoginGetAndPost(t *testing.T) {
	_, mux, _, _, _, _ := fixture(t)
	w := do(mux, req("GET", "/ui/login", "", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "sign in") {
		t.Fatalf("login GET: %d %s", w.Code, w.Body.String())
	}
	form := url.Values{"username": {"u"}, "password": {"p"}}
	w2 := do(mux, req("POST", "/ui/login", "", form))
	if w2.Code != 303 {
		t.Fatalf("login POST: %d", w2.Code)
	}
	// bad creds
	w3 := do(mux, req("POST", "/ui/login", "", url.Values{"username": {"u"}, "password": {"x"}}))
	if w3.Code != 401 {
		t.Fatalf("bad code: %d", w3.Code)
	}
	// bad method
	w4 := do(mux, req("DELETE", "/ui/login", "", nil))
	if w4.Code != 405 {
		t.Fatalf("delete code: %d", w4.Code)
	}
}

func TestLogout(t *testing.T) {
	_, mux, _, _, tok, _ := fixture(t)
	w := do(mux, req("POST", "/ui/logout", tok, nil))
	if w.Code != 303 {
		t.Fatalf("code=%d", w.Code)
	}
	// Without a session cookie too.
	w2 := do(mux, req("POST", "/ui/logout", "", nil))
	if w2.Code != 303 {
		t.Fatalf("nocookie code=%d", w2.Code)
	}
}

func TestRequireSessionRedirects(t *testing.T) {
	_, mux, _, _, _, _ := fixture(t)
	w := do(mux, req("GET", "/ui/", "", nil))
	if w.Code != 303 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestHome(t *testing.T) {
	_, mux, _, op, tok, _ := fixture(t)
	op.op.Feeds = []store.Feed{{XMLURL: "https://x/feed", Title: "X"}}
	w := do(mux, req("GET", "/ui/", tok, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "X") {
		t.Fatalf("home: %d %s", w.Code, w.Body.String())
	}
	// Non-/ui/ path under prefix → 404.
	w2 := do(mux, req("GET", "/ui/nope", tok, nil))
	if w2.Code != 404 {
		t.Fatalf("nope code=%d", w2.Code)
	}
	// Load err
	op.loadErr = errBoom
	w3 := do(mux, req("GET", "/ui/", tok, nil))
	if w3.Code != 500 {
		t.Fatalf("home load err: %d", w3.Code)
	}
}

func TestHomeListErr(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	op.op.Feeds = []store.Feed{{XMLURL: "https://x/feed", Title: "X"}}
	// Corrupt entries file → ListEntries fails.
	fh := store.FeedHash("https://x/feed")
	feedDir := filepath.Join(st.Dir, "entries", fh)
	os.MkdirAll(feedDir, 0o755)
	os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("not json\n"), 0o644)
	w := do(mux, req("GET", "/ui/", tok, nil))
	if w.Code != 500 {
		t.Fatalf("code=%d", w.Code)
	}
}

func seed(t *testing.T, st *store.Store, op *memOPML, count int) string {
	t.Helper()
	u := "https://demo.example/feed"
	op.op.Feeds = []store.Feed{{XMLURL: u, Title: "Demo", HTMLURL: "https://demo.example"}}
	now := time.Now().UTC()
	var es []store.Entry
	for i := 0; i < count; i++ {
		es = append(es, store.Entry{
			GUID:      "g" + strings.Repeat("x", i+1),
			Link:      "https://demo.example/" + strings.Repeat("p", i+1),
			Title:     "T" + strings.Repeat("!", i+1),
			Content:   "<p>body</p>",
			Summary:   "summary",
			Published: now,
			FetchedAt: now,
		})
	}
	st.AppendEntries(store.FeedHash(u), es)
	return u
}

func TestFeedAndEntry(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 2)
	w := do(mux, req("GET", "/ui/feed?id="+u, tok, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Demo") {
		t.Fatalf("feed code=%d body=%s", w.Code, w.Body.String())
	}
	// missing id
	if w := do(mux, req("GET", "/ui/feed", tok, nil)); w.Code != 400 {
		t.Fatalf("miss code=%d", w.Code)
	}
	// not found
	if w := do(mux, req("GET", "/ui/feed?id=nope", tok, nil)); w.Code != 404 {
		t.Fatalf("nope code=%d", w.Code)
	}
	// entry
	es, _ := st.ListEntries(store.FeedHash(u))
	w2 := do(mux, req("GET", "/ui/entry?id="+es[0].Hash, tok, nil))
	if w2.Code != 200 || !strings.Contains(w2.Body.String(), "body") {
		t.Fatalf("entry code=%d body=%s", w2.Code, w2.Body.String())
	}
	// entry missing
	if w := do(mux, req("GET", "/ui/entry", tok, nil)); w.Code != 400 {
		t.Fatalf("entry miss=%d", w.Code)
	}
	if w := do(mux, req("GET", "/ui/entry?id=nosuch", tok, nil)); w.Code != 404 {
		t.Fatalf("entry nosuch=%d", w.Code)
	}
}

func TestFeedAndEntryErrors(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	// Corrupt entries file → ListEntries fails inside feed/entry/setread.
	feedDir := filepath.Join(st.Dir, "entries", store.FeedHash(u))
	if err := os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/ui/feed?id=" + u,
		"/ui/entry?id=x",
	} {
		w := do(mux, req("GET", p, tok, nil))
		if w.Code != 500 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
	// Load err for feed + entry.
	op.loadErr = errBoom
	for _, p := range []string{"/ui/feed?id=" + u, "/ui/entry?id=x"} {
		w := do(mux, req("GET", p, tok, nil))
		if w.Code != 500 {
			t.Fatalf("%s load err: %d", p, w.Code)
		}
	}
}

func TestSetReadAndStarred(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	es, _ := st.ListEntries(store.FeedHash(u))
	h := es[0].Hash
	// read
	w := do(mux, req("POST", "/ui/entry/read?id="+h+"&state=1", tok, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "mark unread") {
		t.Fatalf("read code=%d body=%s", w.Code, w.Body.String())
	}
	if !st.EntryState(h).Read {
		t.Fatal("not set")
	}
	// starred
	w = do(mux, req("POST", "/ui/entry/star?id="+h+"&state=1", tok, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "unstar") {
		t.Fatalf("star code=%d body=%s", w.Code, w.Body.String())
	}
	// missing id
	if w := do(mux, req("POST", "/ui/entry/read", tok, nil)); w.Code != 400 {
		t.Fatalf("miss=%d", w.Code)
	}
	// load err
	op.loadErr = errBoom
	if w := do(mux, req("POST", "/ui/entry/read?id="+h, tok, nil)); w.Code != 500 {
		t.Fatalf("loaderr=%d", w.Code)
	}
}

func TestSetReadNotFound(t *testing.T) {
	_, mux, _, op, tok, _ := fixture(t)
	op.op.Feeds = []store.Feed{{XMLURL: "https://nofeed/feed"}}
	if w := do(mux, req("POST", "/ui/entry/read?id=nosuch&state=1", tok, nil)); w.Code != 404 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestSetFlagStoreError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	es, _ := st.ListEntries(store.FeedHash(u))
	h := es[0].Hash
	os.Chmod(st.Dir, 0o500)
	t.Cleanup(func() { os.Chmod(st.Dir, 0o755) })
	if w := do(mux, req("POST", "/ui/entry/read?id="+h+"&state=1", tok, nil)); w.Code != 500 {
		t.Fatalf("read code=%d", w.Code)
	}
	if w := do(mux, req("POST", "/ui/entry/star?id="+h+"&state=1", tok, nil)); w.Code != 500 {
		t.Fatalf("star code=%d", w.Code)
	}
}

func TestSetFlagListEntriesError(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	es, _ := st.ListEntries(store.FeedHash(u))
	h := es[0].Hash
	// Mark the entry, then corrupt the entries file. SetRead will succeed,
	// but the subsequent ListEntries (to find the row) will fail.
	feedDir := filepath.Join(st.Dir, "entries", store.FeedHash(u))
	if err := os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := do(mux, req("POST", "/ui/entry/read?id="+h+"&state=1", tok, nil)); w.Code != 500 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestStatic(t *testing.T) {
	_, mux, _, _, _, _ := fixture(t)
	w := do(mux, req("GET", "/ui/static/style.css", "", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("style: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	// missing
	if w := do(mux, req("GET", "/ui/static/nope.css", "", nil)); w.Code != 404 {
		t.Fatalf("nope=%d", w.Code)
	}
}

func TestThemeOverride(t *testing.T) {
	dir := t.TempDir()
	overrides := filepath.Join(dir, "cfg", "overrides")
	os.MkdirAll(overrides, 0o755)
	os.WriteFile(filepath.Join(overrides, "theme.css"), []byte("body { color: red }"), 0o644)
	// Template override too — replace home.html.
	tplOverrides := filepath.Join(overrides, "templates")
	os.MkdirAll(tplOverrides, 0o755)
	os.WriteFile(filepath.Join(tplOverrides, "home.html"), []byte(
		`{{define "home"}}OVERRIDDEN{{end}}`,
	), 0o644)

	st, _ := store.Open(t.TempDir())
	pwHash, _ := auth.HashPassword("p")
	as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), auth.Config{Username: "u", PasswordHash: pwHash})
	srv, err := New(st, as, &memOPML{}, "", filepath.Join(dir, "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Routes(mux)
	tok, _ := as.IssueSession("u", "p")
	// home renders override
	w := do(mux, req("GET", "/ui/", tok, nil))
	if !strings.Contains(w.Body.String(), "OVERRIDDEN") {
		t.Fatalf("override not applied: %s", w.Body.String())
	}
	// theme.css served from override
	w2 := do(mux, req("GET", "/ui/static/theme.css", "", nil))
	if w2.Code != 200 || !strings.Contains(w2.Body.String(), "red") {
		t.Fatalf("theme code=%d body=%s", w2.Code, w2.Body.String())
	}
}

func TestNewBadOverrides(t *testing.T) {
	dir := t.TempDir()
	// Make overrides/templates/foo.html invalid.
	bad := filepath.Join(dir, "cfg", "overrides", "templates")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "x.html"), []byte("{{define"), 0o644)
	st, _ := store.Open(t.TempDir())
	as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), auth.Config{})
	if _, err := New(st, as, &memOPML{}, "", filepath.Join(dir, "cfg")); err == nil {
		t.Fatal("expected parse err")
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	srv, _, _, _, _, _ := fixture(t)
	w := httptest.NewRecorder()
	srv.render(w, "no-such", nil)
	if w.Code != 500 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestLoadTemplatesGlobError(t *testing.T) {
	// filepath.Glob errors on a malformed pattern. We can drive this by
	// supplying an overrides root containing a `[` literal — but Glob
	// treats the whole path uniformly. Simulate by providing a path
	// that contains an unmatched bracket.
	dir := t.TempDir()
	bad := dir + string(filepath.Separator) + "["
	srv := &Server{Overrides: bad}
	if err := srv.loadTemplates(); err == nil {
		t.Fatal("expected glob err")
	}
}

// errBoom for shared test errors.
type berr string

func (b berr) Error() string { return string(b) }

var errBoom = berr("boom")

func TestAllUnread(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 3)
	es, _ := st.ListEntries(store.FeedHash(u))
	st.SetRead(es[0].Hash, true) // 2 unread left
	w := do(mux, req("GET", "/ui/all", tok, nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	// 2 unread <li> rows
	if strings.Count(body, `<li id="entry-`) != 2 {
		t.Fatalf("expected 2 entries, body=%s", body)
	}
	// Mark-all button present
	if !strings.Contains(body, "mark all read") {
		t.Fatal("missing mark-all button")
	}
}

func TestStarredView(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 3)
	es, _ := st.ListEntries(store.FeedHash(u))
	st.SetStarred(es[0].Hash, true)
	st.SetStarred(es[1].Hash, true)
	w := do(mux, req("GET", "/ui/starred", tok, nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if strings.Count(w.Body.String(), `<li id="entry-`) != 2 {
		t.Fatalf("expected 2 starred, body=%s", w.Body.String())
	}
}

func TestCrossFeedLoadErr(t *testing.T) {
	_, mux, _, op, tok, _ := fixture(t)
	op.loadErr = errBoom
	for _, p := range []string{"/ui/all", "/ui/starred"} {
		w := do(mux, req("GET", p, tok, nil))
		if w.Code != 500 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
}

func TestCrossFeedListErr(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	// Corrupt entries so ListEntries fails inside the cross-feed loop.
	feedDir := filepath.Join(st.Dir, "entries", store.FeedHash(u))
	os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("garbage\n"), 0o644)
	for _, p := range []string{"/ui/all", "/ui/starred"} {
		w := do(mux, req("GET", p, tok, nil))
		if w.Code != 500 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
}

func TestMarkAllReadFeed(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 3)
	w := do(mux, req("POST", "/ui/mark-all-read?scope=feed&id="+u, tok, nil))
	if w.Code != 303 {
		t.Fatalf("code=%d", w.Code)
	}
	for _, e := range mustList(t, st, u) {
		if !st.EntryState(e.Hash).Read {
			t.Fatalf("not read: %s", e.Hash)
		}
	}
}

func TestMarkAllReadAll(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 2)
	w := do(mux, req("POST", "/ui/mark-all-read?scope=all", tok, nil))
	if w.Code != 303 {
		t.Fatalf("code=%d", w.Code)
	}
	for _, e := range mustList(t, st, u) {
		if !st.EntryState(e.Hash).Read {
			t.Fatalf("not read: %s", e.Hash)
		}
	}
	// idempotent: second call leaves entries read (and exercises the
	// already-read continue branch).
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=all", tok, nil)); w.Code != 303 {
		t.Fatalf("second code=%d", w.Code)
	}
}

func TestMarkAllReadBadInputs(t *testing.T) {
	_, mux, _, _, tok, _ := fixture(t)
	if w := do(mux, req("GET", "/ui/mark-all-read?scope=feed&id=x", tok, nil)); w.Code != 405 {
		t.Fatalf("method=%d", w.Code)
	}
	if w := do(mux, req("POST", "/ui/mark-all-read", tok, nil)); w.Code != 400 {
		t.Fatalf("scope=%d", w.Code)
	}
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=feed", tok, nil)); w.Code != 400 {
		t.Fatalf("missing id=%d", w.Code)
	}
}

func TestMarkAllReadErrors(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	// Load err
	op.loadErr = errBoom
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=all", tok, nil)); w.Code != 500 {
		t.Fatalf("load=%d", w.Code)
	}
	op.loadErr = nil
	// ListEntries err (feed scope)
	feedDir := filepath.Join(st.Dir, "entries", store.FeedHash(u))
	os.WriteFile(filepath.Join(feedDir, "current.ndjson"), []byte("garbage\n"), 0o644)
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=feed&id="+u, tok, nil)); w.Code != 500 {
		t.Fatalf("feed list err=%d", w.Code)
	}
	// ListEntries err (all scope)
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=all", tok, nil)); w.Code != 500 {
		t.Fatalf("all list err=%d", w.Code)
	}
}

func TestMarkAllReadSetReadErrFeed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 1)
	if err := os.Chmod(st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(st.Dir, 0o755) })
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=feed&id="+u, tok, nil)); w.Code != 500 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMarkAllReadSetReadErrAll(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	_, mux, st, op, tok, _ := fixture(t)
	seed(t, st, op, 1)
	if err := os.Chmod(st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(st.Dir, 0o755) })
	if w := do(mux, req("POST", "/ui/mark-all-read?scope=all", tok, nil)); w.Code != 500 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func mustList(t *testing.T, st *store.Store, u string) []store.Entry {
	t.Helper()
	es, err := st.ListEntries(store.FeedHash(u))
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestStaticJSContentType(t *testing.T) {
	_, mux, _, _, _, _ := fixture(t)
	w := do(mux, req("GET", "/ui/static/htmx.min.js", "", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("js: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestHomeWithEntries(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := seed(t, st, op, 3)
	es, _ := st.ListEntries(store.FeedHash(u))
	st.SetRead(es[0].Hash, true) // 1 read, 2 unread
	w := do(mux, req("GET", "/ui/", tok, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "2 unread") {
		t.Fatalf("home: %d %s", w.Code, w.Body.String())
	}
}

func TestEntrySummaryFallback(t *testing.T) {
	_, mux, st, op, tok, _ := fixture(t)
	u := "https://nocontent.example/feed"
	op.op.Feeds = []store.Feed{{XMLURL: u, Title: "X"}}
	now := time.Now().UTC()
	st.AppendEntries(store.FeedHash(u), []store.Entry{
		{GUID: "g", Link: "https://x", Title: "T", Summary: "summary-only", Published: now, FetchedAt: now},
	})
	es, _ := st.ListEntries(store.FeedHash(u))
	w := do(mux, req("GET", "/ui/entry?id="+es[0].Hash, tok, nil))
	if !strings.Contains(w.Body.String(), "summary-only") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestLoadTemplatesParseFSError(t *testing.T) {
	orig := pageExtra
	defer func() { pageExtra = orig }()
	pageExtra = func(string) []string { return []string{"templates/no-such.html"} }
	dir := t.TempDir()
	st, _ := store.Open(t.TempDir())
	as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), auth.Config{})
	if _, err := New(st, as, &memOPML{}, "", ""); err == nil {
		t.Fatal("expected parse err")
	}
}

func TestRenderExecuteError(t *testing.T) {
	srv, _, _, _, _, _ := fixture(t)
	// Inject a broken template into pages so ExecuteTemplate returns an
	// error. Reusing the page name lets render() reach it.
	bad, _ := template.New("broken").Parse(`{{define "broken"}}{{.MissingField.SubField}}{{end}}`)
	srv.pages["broken"] = bad
	w := httptest.NewRecorder()
	srv.render(w, "broken", struct{}{})
	if w.Code != 500 {
		t.Fatalf("code=%d", w.Code)
	}
}
