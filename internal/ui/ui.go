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
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/config"
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

	// StaticVer is appended to bundled-static URLs as a cache-busting
	// query string (?v=...). Set this to the binary's commit / version
	// at construction time so a binary upgrade automatically forces
	// browsers to re-fetch CSS / JS without users having to hard-reload.
	StaticVer string

	// Version is the harborrs build version. Rendered unobtrusively in
	// the base layout footer; empty hides the footer line.
	Version string

	// ConfigPath is the on-disk path to config.json. When set, the
	// UI exposes /ui/settings with a change-password form. When empty,
	// settings is hidden and the route returns 404.
	ConfigPath string

	// Previewer fetches+parses a feed for the add-feed preview page.
	// Production callers point this at a poll.Poller-backed adapter.
	// When nil, /ui/feed/new renders without preview support.
	Previewer FeedPreviewer

	// pages maps page name -> a fully-parsed template tree rooted at
	// `base`. Per-page trees keep title/content blocks isolated.
	pages map[string]*template.Template
}

// New builds a UI server. Theme defaults to "auto".
func New(s *store.Store, a *auth.Store, o OPMLProvider, theme, overrides string) (*Server, error) {
	if theme == "" {
		theme = "auto"
	}
	srv := &Server{Store: s, Auth: a, OPML: o, Theme: theme, Overrides: overrides}
	if err := srv.loadTemplates(); err != nil {
		return nil, err
	}
	return srv, nil
}

// pageNames are the per-page template files we expect to find under
// templates/ (besides base.html, which is the shared layout).
var pageNames = []string{"login", "home", "feed", "entry", "settings", "newfeed"}

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
	mux.HandleFunc("/ui/feed/new", s.requireSession(s.handleFeedNew))
	mux.HandleFunc("/ui/feed/add", s.requireSession(s.handleFeedAdd))
	mux.HandleFunc("/ui/feed/remove", s.requireSession(s.handleFeedRemove))
	mux.HandleFunc("/ui/feed/tag", s.requireSession(s.handleFeedTag))
	mux.HandleFunc("/ui/all", s.requireSession(s.handleAllUnread))
	mux.HandleFunc("/ui/starred", s.requireSession(s.handleStarred))
	mux.HandleFunc("/ui/entry", s.requireSession(s.handleEntry))
	mux.HandleFunc("/ui/entry/read", s.requireSession(s.handleSetRead))
	mux.HandleFunc("/ui/entry/star", s.requireSession(s.handleSetStarred))
	mux.HandleFunc("/ui/mark-all-read", s.requireSession(s.handleMarkAllRead))
	mux.HandleFunc("/ui/settings", s.requireSession(s.handleSettings))
	mux.HandleFunc("/ui/settings/passwd", s.requireSession(s.handlePasswd))
	return mux
}

func (s *Server) requireSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Auth.CheckSession(auth.SessionFromRequest(r)) {
			RelRedirect(w, r, uiBase(r)+"login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// uiBase returns the relative URL prefix that, when resolved against
// the effective request URI, points back at the /ui/ root. We pick
// relative references over absolute paths so the app can be served
// under any external prefix (e.g. tailscale funnel --set-path=/rss)
// without baking a base-path config knob into the server.
//
// Precondition: r.URL.Path is under /ui/. Every call site is a UI
// handler reached via Routes(), and Go's http.ServeMux redirects
// "/ui" → "/ui/" before any handler runs, so we never see a path
// without the trailing slash. Callers outside this package would
// produce nonsense ("../../" for /foo/bar) — don't.
//
// Examples (request → returned prefix → resolves to):
//
//	/ui/             → "./"   → /ui/
//	/ui/login        → "./"   → /ui/   (login is a sibling of /ui/)
//	/ui/feed/new     → "../"  → /ui/
//	/ui/settings/passwd → "../" → /ui/
//
// The prefix always ends in "/" so callers can append a path segment
// directly: uiBase(r) + "login".
func uiBase(r *http.Request) string {
	rest := strings.TrimPrefix(r.URL.Path, "/ui/")
	depth := strings.Count(rest, "/")
	if depth == 0 {
		return "./"
	}
	return strings.Repeat("../", depth)
}

// RelRedirect writes a 3xx response whose Location header is the
// verbatim relative reference loc — leading slash forbidden. We can't
// use net/http.Redirect here because it eagerly resolves any relative
// reference against r.URL into an absolute path, which is exactly the
// rewriting we are trying to avoid. Browsers resolve a relative
// Location against the effective request URI per RFC 7231 §7.1.2, and
// that is what makes the UI work under an arbitrary path prefix.
func RelRedirect(w http.ResponseWriter, r *http.Request, loc string, code int) {
	if strings.HasPrefix(loc, "/") {
		// Defence in depth: a leading slash here would re-introduce
		// the very absolute-path bug this function exists to prevent.
		panic("ui: RelRedirect called with absolute-path Location: " + loc)
	}
	h := w.Header()
	h.Set("Location", loc)
	if _, hasType := h["Content-Type"]; !hasType && r.Method == http.MethodGet {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(code)
	// Match http.Redirect's tiny body for GET so curl -i users see
	// something. The href is HTML-escaped to keep template-injection-
	// flavoured payloads inert; callers only ever pass static strings
	// today, but defence is cheap.
	if r.Method == http.MethodGet {
		fmt.Fprintf(w, "<a href=\"%s\">%s</a>.\n",
			template.HTMLEscapeString(loc), http.StatusText(code))
	}
}

// --- pages ---

type baseData struct {
	Theme         string
	User          string
	ExtraCSS      string
	Error         string
	StaticVer     string // "?v=<commit>" suffix used in base.html
	Version       string // shown in the base.html footer; empty hides it
	Settings      bool   // true when /ui/settings is available
	PasswdChanged bool   // set on login page after a successful change

	// Base is the relative URL prefix from the effective request URI
	// back up to the /ui/ root, always ending in "/". Templates prefix
	// every internal href/action/hx-* with {{.Base}} so the rendered
	// markup contains no leading-slash URLs.
	Base string
}

func (s *Server) base(r *http.Request) baseData {
	d := baseData{Theme: s.Theme, StaticVer: s.StaticVer, Version: s.Version, Settings: s.ConfigPath != "", Base: uiBase(r)}
	if s.Auth.CheckSession(auth.SessionFromRequest(r)) {
		d.User = s.Auth.Cfg.Username
	}
	if s.Overrides != "" {
		if _, err := os.Stat(filepath.Join(s.Overrides, "overrides", "theme.css")); err == nil {
			d.ExtraCSS = d.Base + "static/theme.css"
		}
	}
	return d
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		d := s.base(r)
		d.PasswdChanged = r.URL.Query().Get("passwd") == "1"
		s.render(w, "login", struct{ baseData }{d})
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
		RelRedirect(w, r, "./", http.StatusSeeOther)
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
	RelRedirect(w, r, "login", http.StatusSeeOther)
}

// unreadCookieName persists the "show unread only" choice across feed
// pages, the home list and navigation. The value is "1" (default) or
// "0" (show all). The cookie is set by writeUnreadCookie when the
// user clicks an explicit ?unread=0/1 link; subsequent pages without
// the query param read the cookie via effectiveUnreadOnly.
const unreadCookieName = "h_unread"

// effectiveUnreadOnly returns the user's current "unread only" choice
// for list pages. Precedence: explicit ?unread=… query param first,
// then the persisted cookie, then the default (true — start filtered).
func effectiveUnreadOnly(r *http.Request) bool {
	switch r.URL.Query().Get("unread") {
	case "1":
		return true
	case "0":
		return false
	}
	if c, err := r.Cookie(unreadCookieName); err == nil && c.Value == "0" {
		return false
	}
	return true
}

// writeUnreadCookie persists the user's choice. Called from handlers
// when the request URL carries an explicit ?unread=0/1, so that the
// next plain navigation (e.g. clicking a feed in the sidebar, hitting
// `u` from an entry) keeps the same filter without a query param.
func writeUnreadCookie(w http.ResponseWriter, r *http.Request, secure bool) {
	q := r.URL.Query().Get("unread")
	if q != "0" && q != "1" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     unreadCookieName,
		Value:    q,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// tagSlug derives a stable, DOM-id-friendly slug from a tag name. Used
// for the collapse-target id (#grp-<slug>). Non-alnum runs collapse to
// '-'; the empty tag (untagged sentinel) maps to "untagged".
func tagSlug(name string) string {
	if name == "" {
		return "all"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "tag"
	}
	return s
}

type homeFeed struct {
	Title  string
	URL    string
	Unread int
	Tags   []string
}

// feedGroup is one tag-bucket rendered on the home page. A single feed
// appears in every group whose tag it carries; feeds with no tags land
// in the "Untagged" group. When a tag filter is active we still emit
// the same shape — there's just one group.
type feedGroup struct {
	Name   string // tag name; "" for untagged sentinel display
	Label  string
	Slug   string // stable CSS-id slug for the collapsible target
	Unread int
	Feeds  []homeFeed
}

// tagCount is one row in the home sidebar.
type tagCount struct {
	Name   string // tag name, or "" for "All", or store.ReservedTagUntagged for the no-tags bucket
	Label  string // display label
	Unread int
}

// unreadCounts walks every unread entry once and returns:
//   - per-feed unread counts (keyed by feed XML URL)
//   - special bucket "" → all-unread total
//   - special bucket store.ReservedTagUntagged → untagged-feed unread total
//   - one bucket per tag name
//
// A single tag is counted multiple times for a feed only if that feed's
// Tags list contains duplicates — NormalizeTags prevents that, so this
// is a strict O(unread * tags) pass.
func (s *Server) unreadCounts(op *store.OPML) (perFeed map[string]int, buckets map[string]int, err error) {
	perFeed = map[string]int{}
	buckets = map[string]int{}
	for _, f := range op.Feeds {
		es, lerr := s.Store.ListEntries(store.FeedHash(f.XMLURL))
		if lerr != nil {
			return nil, nil, lerr
		}
		count := 0
		for _, e := range es {
			if !s.Store.EntryState(e.Hash).Read {
				count++
			}
		}
		perFeed[f.XMLURL] = count
		buckets[""] += count
		if len(f.Tags) == 0 {
			buckets[store.ReservedTagUntagged] += count
		}
		for _, t := range f.Tags {
			buckets[t] += count
		}
	}
	return perFeed, buckets, nil
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
	unreadOnly := effectiveUnreadOnly(r)
	writeUnreadCookie(w, r, s.Secure)
	tagFilter := r.URL.Query().Get("tag") // "" → all; store.ReservedTagUntagged → untagged
	perFeed, buckets, err := s.unreadCounts(op)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	feeds := make([]homeFeed, 0, len(op.Feeds))
	withUnread := 0
	scopedTotal := 0
	for _, f := range op.Feeds {
		count := perFeed[f.XMLURL]
		if tagFilter == store.ReservedTagUntagged {
			if len(f.Tags) != 0 {
				continue
			}
		} else if tagFilter != "" {
			if !f.HasTag(tagFilter) {
				continue
			}
		}
		// In scope: contribute to the scope-aware totals.
		scopedTotal += count
		if count > 0 {
			withUnread++
		}
		if unreadOnly && count == 0 {
			continue
		}
		feeds = append(feeds, homeFeed{Title: f.Title, URL: f.XMLURL, Unread: count, Tags: f.Tags})
	}
	tags := op.AllTags()
	pinned := []tagCount{
		{Name: "", Label: "All", Unread: buckets[""]},
		{Name: store.ReservedTagUntagged, Label: "Untagged", Unread: buckets[store.ReservedTagUntagged]},
	}
	sidebar := make([]tagCount, 0, len(tags))
	for _, t := range tags {
		sidebar = append(sidebar, tagCount{Name: t, Label: t, Unread: buckets[t]})
	}
	groups := buildFeedGroups(feeds, tagFilter)
	data := struct {
		baseData
		Feeds      []homeFeed
		Total      int
		WithUnread int
		UnreadOnly bool
		Sidebar    []tagCount // pinned rows
		Tags       []tagCount // real tag rows (empty when no tags exist)
		TagFilter  string
		Groups     []feedGroup
	}{s.base(r), feeds, scopedTotal, withUnread, unreadOnly, pinned, sidebar, tagFilter, groups}
	s.render(w, "home", data)
}

// buildFeedGroups buckets feeds by tag for the home page. Each feed
// shows under every tag it carries; feeds with no tags land in the
// trailing "Untagged" group. When tagFilter is set, every feed in the
// input already matches that filter, so we emit a single group with
// the filter's label.
func buildFeedGroups(feeds []homeFeed, tagFilter string) []feedGroup {
	if len(feeds) == 0 {
		return nil
	}
	if tagFilter != "" {
		label := tagFilter
		if tagFilter == store.ReservedTagUntagged {
			label = "Untagged"
		}
		g := feedGroup{Name: tagFilter, Label: label, Slug: tagSlug(label)}
		for _, f := range feeds {
			g.Feeds = append(g.Feeds, f)
			g.Unread += f.Unread
		}
		return []feedGroup{g}
	}
	// No filter: bucket by tag, with an Untagged trailer.
	byTag := map[string]*feedGroup{}
	var order []string
	for _, f := range feeds {
		if len(f.Tags) == 0 {
			g, ok := byTag[""]
			if !ok {
				g = &feedGroup{Name: store.ReservedTagUntagged, Label: "Untagged", Slug: "untagged"}
				byTag[""] = g
				order = append(order, "")
			}
			g.Feeds = append(g.Feeds, f)
			g.Unread += f.Unread
			continue
		}
		for _, t := range f.Tags {
			g, ok := byTag[t]
			if !ok {
				g = &feedGroup{Name: t, Label: t, Slug: tagSlug(t)}
				byTag[t] = g
				order = append(order, t)
			}
			g.Feeds = append(g.Feeds, f)
			g.Unread += f.Unread
		}
	}
	// Sort: real tags alpha, Untagged always last.
	sort.SliceStable(order, func(i, j int) bool {
		if order[i] == "" {
			return false
		}
		if order[j] == "" {
			return true
		}
		return strings.ToLower(order[i]) < strings.ToLower(order[j])
	})
	out := make([]feedGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byTag[k])
	}
	return out
}

type feedEntry struct {
	Hash         string
	Title        string
	Read         bool
	Starred      bool
	FeedTitle    string // only set on cross-feed views
	Published    time.Time
	PublishedFmt string // pre-formatted for display
}

type entryListData struct {
	baseData
	Heading     string
	Entries     []feedEntry
	ShowMarkAll bool
	Scope       string // "feed" | "all" | "starred"
	ScopeID     string // feed URL when Scope == "feed"
	UnreadOnly  bool   // when true, Entries has been filtered to unread
	UnreadCount int    // unread count for this scope (pre-filter)
	FeedTags    []string
	AllTags     []string // every tag across all feeds, for the add-tag datalist
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
	unreadOnly := effectiveUnreadOnly(r)
	writeUnreadCookie(w, r, s.Secure)
	entries := make([]feedEntry, 0, len(es))
	unreadCount := 0
	for _, e := range es {
		st := s.Store.EntryState(e.Hash)
		if !st.Read {
			unreadCount++
		}
		if unreadOnly && st.Read {
			continue
		}
		entries = append(entries, feedEntry{
			Hash:         e.Hash,
			Title:        e.Title,
			Read:         st.Read,
			Starred:      st.Starred,
			Published:    e.Published,
			PublishedFmt: formatPublished(e.Published, time.Now()),
		})
	}
	s.render(w, "feed", entryListData{
		baseData:    s.base(r),
		Heading:     feed.Title,
		Entries:     entries,
		ShowMarkAll: true,
		Scope:       "feed",
		ScopeID:     urlS,
		UnreadOnly:  unreadOnly,
		UnreadCount: unreadCount,
		FeedTags:    feed.Tags,
		AllTags:     op.AllTags(),
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
			Hash:         x.entry.Hash,
			Title:        x.entry.Title,
			Read:         st.Read,
			Starred:      st.Starred,
			FeedTitle:    x.feedTitle,
			Published:    x.entry.Published,
			PublishedFmt: formatPublished(x.entry.Published, time.Now()),
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
	tags := parseTagInput(r.FormValue("tags"))
	if title == "" {
		title = u
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op.Add(store.Feed{XMLURL: u, Title: title, Tags: tags})
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	RelRedirect(w, r, "../", http.StatusSeeOther)
}

// handleFeedTag adds or removes a single tag on a feed. POST with form
// `url` + (`add` or `remove`). Re-renders the tag-chip fragment so the
// caller (an hx-post button) can swap it in-place.
func (s *Server) handleFeedTag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u := strings.TrimSpace(r.FormValue("url"))
	add := strings.TrimSpace(r.FormValue("add"))
	rem := strings.TrimSpace(r.FormValue("remove"))
	if u == "" || (add == "" && rem == "") {
		http.Error(w, "missing url and add/remove", http.StatusBadRequest)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f := op.Find(u)
	if f == nil {
		http.NotFound(w, r)
		return
	}
	if add != "" {
		for _, t := range parseTagInput(add) {
			f.AddTag(t)
		}
	}
	if rem != "" {
		f.RemoveTag(rem)
	}
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = s.pages["feed"].ExecuteTemplate(w, "tagchips", struct {
		ScopeID  string
		FeedTags []string
		AllTags  []string
	}{u, f.Tags, op.AllTags()})
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
	RelRedirect(w, r, "../", http.StatusSeeOther)
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
		// Land back on the feed and let the user's persisted
		// "unread only" choice (or the default) decide the view.
		RelRedirect(w, r, "./", http.StatusSeeOther)
	case "all":
		for _, f := range op.Feeds {
			if err := mark(f.XMLURL); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		RelRedirect(w, r, "all", http.StatusSeeOther)
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
	w.Header().Set("Cache-Control", "no-store")
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
	row := feedEntry{
		Hash:         hash,
		Title:        e.Title,
		Read:         st.Read,
		Starred:      st.Starred,
		Published:    e.Published,
		PublishedFmt: formatPublished(e.Published, time.Now()),
	}
	_ = s.pages["feed"].ExecuteTemplate(w, "entryrow", row)
}

// handleSettings renders the settings page (GET /ui/settings). Refuses
// with 404 when ConfigPath isn't wired, e.g. test setups without a
// disk-backed config.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.ConfigPath == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d := s.base(r)
	s.render(w, "settings", struct {
		baseData
		Ok bool
	}{d, r.URL.Query().Get("ok") == "1"})
}

// handlePasswd handles POST /ui/settings/passwd. Verifies the current
// password, hashes the new one, atomically updates config.json + the
// in-memory auth.Cfg, then revokes every session (including this one)
// and bounces the browser back to the login page so the user has to
// authenticate with the new password.
func (s *Server) handlePasswd(w http.ResponseWriter, r *http.Request) {
	if s.ConfigPath == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderPasswdErr(w, r, "bad form")
		return
	}
	old := r.FormValue("old")
	newp := r.FormValue("new")
	confirm := r.FormValue("confirm")
	if newp != confirm {
		s.renderPasswdErr(w, r, "new password and confirmation do not match")
		return
	}
	if len(newp) < 8 {
		s.renderPasswdErr(w, r, "new password must be at least 8 characters")
		return
	}
	if err := s.Auth.Verify(s.Auth.Cfg.Username, old); err != nil {
		s.renderPasswdErr(w, r, "current password is incorrect")
		return
	}
	h, err := authHashPasswordHook(newp)
	if err != nil {
		s.renderPasswdErr(w, r, "hash: "+err.Error())
		return
	}
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		s.renderPasswdErr(w, r, "load config: "+err.Error())
		return
	}
	cfg.Auth.PasswordHash = h
	if err := config.Save(s.ConfigPath, cfg); err != nil {
		s.renderPasswdErr(w, r, "save config: "+err.Error())
		return
	}
	s.Auth.SetPasswordHash(h)
	_ = s.Auth.RevokeAllSessions()
	auth.ClearSessionCookie(w)
	RelRedirect(w, r, "../login?passwd=1", http.StatusSeeOther)
}

func (s *Server) renderPasswdErr(w http.ResponseWriter, r *http.Request, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	d := s.base(r)
	d.Error = msg
	s.render(w, "settings", struct {
		baseData
		Ok bool
	}{d, false})
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
	// Aggressive cache when the URL is fingerprinted (?v=...). Without
	// the fingerprint we still allow short caching but require revalidation.
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=60")
	}
	w.Write(data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Authenticated UI pages must never be cached. Without this, hitting
	// the browser's Back button (or pressing `u` in the entry view) often
	// shows a stale snapshot of the list page — the entry you just toggled
	// read/unread/starred still appears in its old state until you F5.
	// Per the HTTP cache spec, no-store also disqualifies the response
	// from bfcache, so all back-navigations re-fetch from the server.
	// keys.js still has a bfcache pageshow.reload listener as belt-and-
	// suspenders in case a downstream cache layer overrides this header.
	w.Header().Set("Cache-Control", "no-store")
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "unknown template: "+name, http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// authHashPasswordHook is the seam tests swap to exercise the hash-error
// branch of handlePasswd. Production callers go through auth.HashPassword.
var authHashPasswordHook = auth.HashPassword

// parseTagInput splits a comma-separated tag form value into a
// normalised tag slice (trimmed, deduped, sorted).
func parseTagInput(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	return store.NormalizeTags(parts)
}

// formatPublished returns a compact relative-or-absolute timestamp used
// on entry list rows. <1h → "Nm", <24h → "Nh", <7d → "Nd", same year →
// "Jan 02", older → "2006-01-02". Zero time renders as "" so old test
// fixtures without a Published field don't show a stray label.
func formatPublished(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case t.Year() == now.Year():
		return t.Format("Jan 02")
	default:
		return t.Format("2006-01-02")
	}
}

// FeedPreview is the lightweight description the add-feed page renders
// after a successful fetch. We deliberately don't reuse store.Entry —
// the preview is purely visual and the user has not yet subscribed.
type FeedPreview struct {
	Title       string
	Description string
	Link        string
	Items       []FeedPreviewItem
}

// FeedPreviewItem is one entry shown in the add-feed preview list.
type FeedPreviewItem struct {
	Title string
}

// FeedPreviewer fetches + parses a feed URL without persisting it.
// Implementations should bound time + size and refuse non-feed content.
type FeedPreviewer interface {
	Preview(url string) (*FeedPreview, error)
}

// handleFeedNew renders the dedicated "add feed" page. GET shows an
// empty form; POST fetches the URL via Previewer and shows a preview
// underneath the form. The user then clicks "subscribe" which POSTs
// to the existing /ui/feed/add handler.
func (s *Server) handleFeedNew(w http.ResponseWriter, r *http.Request) {
	type newFeedData struct {
		baseData
		URL     string
		Tags    string
		Preview *FeedPreview
	}
	d := newFeedData{baseData: s.base(r)}
	switch r.Method {
	case http.MethodGet:
		s.render(w, "newfeed", d)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			d.Error = err.Error()
			w.WriteHeader(http.StatusBadRequest)
			s.render(w, "newfeed", d)
			return
		}
		d.URL = strings.TrimSpace(r.FormValue("url"))
		d.Tags = strings.TrimSpace(r.FormValue("tags"))
		if d.URL == "" {
			d.Error = "feed URL is required"
			w.WriteHeader(http.StatusBadRequest)
			s.render(w, "newfeed", d)
			return
		}
		if s.Previewer == nil {
			d.Error = "preview not configured on this server"
			w.WriteHeader(http.StatusServiceUnavailable)
			s.render(w, "newfeed", d)
			return
		}
		p, err := s.Previewer.Preview(d.URL)
		if err != nil {
			d.Error = "could not fetch feed: " + err.Error()
			w.WriteHeader(http.StatusBadRequest)
			s.render(w, "newfeed", d)
			return
		}
		d.Preview = p
		s.render(w, "newfeed", d)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
