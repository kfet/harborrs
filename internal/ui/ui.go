// Package ui implements the embedded htmx web UI.
//
// Templates are embedded via `//go:embed templates/*.html`. On startup,
// any matching files in `<configDir>/overrides/templates/*.html` are
// parsed *after* the embedded set, so user files shadow embedded ones
// by name. Likewise, `<configDir>/overrides/theme.css` (if present) is
// served at `/ui/static/theme.css` and loaded after the bundled style.
package ui

import (
	"embed"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/store"
)

//go:embed templates/*.html
var embeddedTemplates embed.FS

//go:embed static/*
var embeddedStatic embed.FS

// OPMLProvider is the same shape used by internal/reader.
type OPMLProvider interface {
	Load() (*store.OPML, error)
	Save(*store.OPML) error
}

// Server is the htmx UI HTTP surface. Construct via New and mount with
// Routes(mux).
type Server struct {
	Store     *store.Store
	Auth      *auth.Store
	OPML      OPMLProvider
	Theme     string
	Overrides string // base config dir; "overrides/" is expected underneath
	Secure    bool   // set Secure flag on session cookies (https deployments)

	// pages maps page name -> a fully-parsed template tree rooted at
	// `base`. Per-page trees keep title/content blocks isolated.
	pages map[string]*template.Template
}

// New builds a UI server. Theme defaults to "light".
func New(s *store.Store, a *auth.Store, o OPMLProvider, theme, overrides string) (*Server, error) {
	if theme == "" {
		theme = "light"
	}
	srv := &Server{Store: s, Auth: a, OPML: o, Theme: theme, Overrides: overrides}
	if err := srv.loadTemplates(); err != nil {
		return nil, err
	}
	return srv, nil
}

// pageNames are the per-page template files we expect to find under
// templates/ (besides base.html, which is the shared layout).
var pageNames = []string{"login", "home", "feed", "entry"}

// pageExtra files (beyond base.html) parsed into each page. Override in
// tests to trigger ParseFS error paths.
var pageExtra = func(name string) []string {
	return []string{"templates/base.html", "templates/" + name + ".html"}
}

func (s *Server) loadTemplates() error {
	// Gather override files (if any).
	var overrideFiles []string
	if s.Overrides != "" {
		dir := filepath.Join(s.Overrides, "overrides", "templates")
		matches, err := filepath.Glob(filepath.Join(dir, "*.html"))
		if err != nil {
			return err
		}
		overrideFiles = matches
	}
	pages := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t, err := template.New(name).ParseFS(embeddedTemplates, pageExtra(name)...)
		if err != nil {
			return err
		}
		// Apply override files in stable order. ParseFiles re-parses
		// definitions on the same set, so user files shadow embedded
		// ones by name.
		if len(overrideFiles) > 0 {
			if _, err := t.ParseFiles(overrideFiles...); err != nil {
				return err
			}
		}
		pages[name] = t
	}
	// entryrow is a fragment used by toggle handlers; it lives in feed.html.
	s.pages = pages
	return nil
}

// Routes registers UI endpoints on mux and returns the same mux for
// chaining.
func (s *Server) Routes(mux *http.ServeMux) *http.ServeMux {
	mux.HandleFunc("/ui/login", s.handleLogin)
	mux.HandleFunc("/ui/logout", s.handleLogout)
	mux.HandleFunc("/ui/static/", s.handleStatic)
	mux.HandleFunc("/ui/", s.requireSession(s.handleHome))
	mux.HandleFunc("/ui/feed", s.requireSession(s.handleFeed))
	mux.HandleFunc("/ui/feed/add", s.requireSession(s.handleFeedAdd))
	mux.HandleFunc("/ui/feed/remove", s.requireSession(s.handleFeedRemove))
	mux.HandleFunc("/ui/all", s.requireSession(s.handleAllUnread))
	mux.HandleFunc("/ui/starred", s.requireSession(s.handleStarred))
	mux.HandleFunc("/ui/entry", s.requireSession(s.handleEntry))
	mux.HandleFunc("/ui/entry/read", s.requireSession(s.handleSetRead))
	mux.HandleFunc("/ui/entry/star", s.requireSession(s.handleSetStarred))
	mux.HandleFunc("/ui/mark-all-read", s.requireSession(s.handleMarkAllRead))
	return mux
}

func (s *Server) requireSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Auth.CheckSession(auth.SessionFromRequest(r)) {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// --- pages ---

type baseData struct {
	Theme    string
	User     string
	ExtraCSS string
	Error    string
}

func (s *Server) base(r *http.Request) baseData {
	d := baseData{Theme: s.Theme}
	if s.Auth.CheckSession(auth.SessionFromRequest(r)) {
		d.User = s.Auth.Cfg.Username
	}
	if s.Overrides != "" {
		if _, err := os.Stat(filepath.Join(s.Overrides, "overrides", "theme.css")); err == nil {
			d.ExtraCSS = "/ui/static/theme.css"
		}
	}
	return d
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.render(w, "login", struct {
			baseData
		}{s.base(r)})
	case http.MethodPost:
		_ = r.ParseForm()
		u, p := r.FormValue("username"), r.FormValue("password")
		tok, err := s.Auth.IssueSession(u, p)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			d := s.base(r)
			d.Error = "invalid credentials"
			s.render(w, "login", struct{ baseData }{d})
			return
		}
		auth.SetSessionCookie(w, tok, s.Secure)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := auth.SessionFromRequest(r)
	if tok != "" {
		_ = s.Auth.RevokeSession(tok)
	}
	auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

type homeFeed struct {
	Title  string
	URL    string
	Unread int
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	// Only the exact /ui/ path is the home; reject everything else under
	// /ui/ that isn't handled explicitly.
	if r.URL.Path != "/ui/" {
		http.NotFound(w, r)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	feeds := make([]homeFeed, 0, len(op.Feeds))
	total := 0
	for _, f := range op.Feeds {
		fh := store.FeedHash(f.XMLURL)
		es, err := s.Store.ListEntries(fh)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		count := 0
		for _, e := range es {
			if !s.Store.EntryState(e.Hash).Read {
				count++
			}
		}
		total += count
		feeds = append(feeds, homeFeed{Title: f.Title, URL: f.XMLURL, Unread: count})
	}
	data := struct {
		baseData
		Feeds []homeFeed
		Total int
	}{s.base(r), feeds, total}
	s.render(w, "home", data)
}

type feedEntry struct {
	Hash      string
	Title     string
	Read      bool
	Starred   bool
	FeedTitle string // only set on cross-feed views
}

type entryListData struct {
	baseData
	Heading     string
	Entries     []feedEntry
	ShowMarkAll bool
	Scope       string // "feed" | "all" | "starred"
	ScopeID     string // feed URL when Scope == "feed"
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	urlS := r.URL.Query().Get("id")
	if urlS == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	feed := op.Find(urlS)
	if feed == nil {
		http.NotFound(w, r)
		return
	}
	es, err := s.Store.ListEntries(store.FeedHash(urlS))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries := make([]feedEntry, 0, len(es))
	for _, e := range es {
		st := s.Store.EntryState(e.Hash)
		entries = append(entries, feedEntry{Hash: e.Hash, Title: e.Title, Read: st.Read, Starred: st.Starred})
	}
	s.render(w, "feed", entryListData{
		baseData:    s.base(r),
		Heading:     feed.Title,
		Entries:     entries,
		ShowMarkAll: true,
		Scope:       "feed",
		ScopeID:     urlS,
	})
}

// handleAllUnread renders every unread entry across every feed, newest
// first, with a small feed-title tag on each row.
func (s *Server) handleAllUnread(w http.ResponseWriter, r *http.Request) {
	s.crossFeed(w, r, "unread", "all", func(st store.EntryState) bool { return !st.Read })
}

// handleStarred renders every starred entry across every feed.
func (s *Server) handleStarred(w http.ResponseWriter, r *http.Request) {
	s.crossFeed(w, r, "starred", "starred", func(st store.EntryState) bool { return st.Starred })
}

type entryWithFeed struct {
	entry     store.Entry
	feedTitle string
}

func (s *Server) crossFeed(w http.ResponseWriter, r *http.Request, heading, scope string, keep func(store.EntryState) bool) {
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var all []entryWithFeed
	for _, f := range op.Feeds {
		es, err := s.Store.ListEntries(store.FeedHash(f.XMLURL))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, e := range es {
			if keep(s.Store.EntryState(e.Hash)) {
				all = append(all, entryWithFeed{entry: e, feedTitle: f.Title})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].entry.Published.After(all[j].entry.Published)
	})
	entries := make([]feedEntry, 0, len(all))
	for _, x := range all {
		st := s.Store.EntryState(x.entry.Hash)
		entries = append(entries, feedEntry{
			Hash:      x.entry.Hash,
			Title:     x.entry.Title,
			Read:      st.Read,
			Starred:   st.Starred,
			FeedTitle: x.feedTitle,
		})
	}
	s.render(w, "feed", entryListData{
		baseData:    s.base(r),
		Heading:     heading,
		Entries:     entries,
		ShowMarkAll: scope == "all",
		Scope:       scope,
	})
}

// handleFeedAdd subscribes to a new feed.
func (s *Server) handleFeedAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u := strings.TrimSpace(r.FormValue("url"))
	if u == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	folder := strings.TrimSpace(r.FormValue("folder"))
	if title == "" {
		title = u
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op.Add(store.Feed{XMLURL: u, Title: title, Folder: folder})
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// handleFeedRemove unsubscribes.
func (s *Server) handleFeedRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u := strings.TrimSpace(r.FormValue("url"))
	if u == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op.Remove(u)
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// handleMarkAllRead marks every entry in a given scope as read. Scopes:
// "feed" (requires id=feed-url), "all" (every unread entry across all
// feeds).
func (s *Server) handleMarkAllRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scope := r.URL.Query().Get("scope")
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mark := func(feedURL string) error {
		es, err := s.Store.ListEntries(store.FeedHash(feedURL))
		if err != nil {
			return err
		}
		for _, e := range es {
			if s.Store.EntryState(e.Hash).Read {
				continue
			}
			if err := s.Store.SetRead(e.Hash, true); err != nil {
				return err
			}
		}
		return nil
	}
	switch scope {
	case "feed":
		u := r.URL.Query().Get("id")
		if u == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if err := mark(u); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/ui/feed?id="+u, http.StatusSeeOther)
	case "all":
		for _, f := range op.Feeds {
			if err := mark(f.XMLURL); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		http.Redirect(w, r, "/ui/all", http.StatusSeeOther)
	default:
		http.Error(w, "bad scope", http.StatusBadRequest)
	}
}

// findEntry walks the OPML for the entry with the given hash. Returns
// the entry, its owning feed, ok=true if found, and any I/O error from
// ListEntries. The "owning feed" is the Feed in the current OPML; if the
// entry's feed was removed from the OPML between fetch and now, this
// returns ok=false (the entry is invisible to the UI by design).
func (s *Server) findEntry(op *store.OPML, hash string) (store.Entry, store.Feed, bool, error) {
	for _, f := range op.Feeds {
		es, err := s.Store.ListEntries(store.FeedHash(f.XMLURL))
		if err != nil {
			return store.Entry{}, store.Feed{}, false, err
		}
		for _, e := range es {
			if e.Hash == hash {
				return e, f, true, nil
			}
		}
	}
	return store.Entry{}, store.Feed{}, false, nil
}

func (s *Server) handleEntry(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("id")
	if hash == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	e, f, ok, err := s.findEntry(op, hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	body := e.Content
	if body == "" {
		body = e.Summary
	}
	data := struct {
		baseData
		Entry     store.Entry
		Body      template.HTML
		State     store.EntryState
		FeedURL   string
		FeedTitle string
	}{s.base(r), e, template.HTML(body), s.Store.EntryState(e.Hash), f.XMLURL, f.Title}
	s.render(w, "entry", data)
}

func (s *Server) handleSetRead(w http.ResponseWriter, r *http.Request) {
	s.toggleFlag(w, r, true)
}
func (s *Server) handleSetStarred(w http.ResponseWriter, r *http.Request) {
	s.toggleFlag(w, r, false)
}

func (s *Server) toggleFlag(w http.ResponseWriter, r *http.Request, isRead bool) {
	hash := r.URL.Query().Get("id")
	state := r.URL.Query().Get("state")
	if hash == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	on := state == "1"
	var err error
	if isRead {
		err = s.Store.SetRead(hash, on)
	} else {
		err = s.Store.SetStarred(hash, on)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Find the entry to re-render its row (or full detail).
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	e, f, ok, err := s.findEntry(op, hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	st := s.Store.EntryState(hash)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("view") == "detail" {
		body := e.Content
		if body == "" {
			body = e.Summary
		}
		data := struct {
			Entry     store.Entry
			Body      template.HTML
			State     store.EntryState
			FeedURL   string
			FeedTitle string
		}{e, template.HTML(body), st, f.XMLURL, f.Title}
		_ = s.pages["entry"].ExecuteTemplate(w, "entry-detail", data)
		return
	}
	row := feedEntry{Hash: hash, Title: e.Title, Read: st.Read, Starred: st.Starred}
	_ = s.pages["feed"].ExecuteTemplate(w, "entryrow", row)
}

// handleStatic serves bundled CSS / overrides theme.css.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ui/static/")
	if name == "theme.css" && s.Overrides != "" {
		p := filepath.Join(s.Overrides, "overrides", "theme.css")
		http.ServeFile(w, r, p)
		return
	}
	data, err := embeddedStatic.ReadFile("static/" + name)
	if err != nil {
		// embed.FS.ReadFile only returns NotExist for missing files.
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript")
	}
	w.Write(data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "unknown template: "+name, http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
