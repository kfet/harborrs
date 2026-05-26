// Package reader implements the Google Reader API subset spoken by
// FreshRSS-compatible clients (Reeder Classic, NetNewsWire, etc).
//
// All `/reader/*` endpoints require an API token issued by
// `/accounts/ClientLogin`. Tokens are obtained via `Authorization:
// GoogleLogin auth=<token>` or `T=<token>` form value.
package reader

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/store"
)

// OPMLProvider lets the Reader server fetch (and atomically replace) the
// subscriptions OPML. The host (cmd/harborrs) supplies this so the Reader
// package stays decoupled from the file layout.
type OPMLProvider interface {
	Load() (*store.OPML, error)
	Save(*store.OPML) error
}

// Server is the Reader API HTTP surface. Construct via New and mount with
// Routes(mux).
type Server struct {
	Store   *store.Store
	Auth    *auth.Store
	OPML    OPMLProvider
	Now     func() time.Time
	MaxPage int

	// Version is the harborrs build version surfaced via /status and
	// the `harborrsVersion` extension field on user-info responses.
	// Empty when unset; main.go wires this to harborrs.Version.
	Version   string
	Commit    string
	BuildDate string

	mu sync.Mutex // guards subscription mutations
}

// New returns a Server with sensible defaults. All three fields are
// required.
func New(s *store.Store, a *auth.Store, opml OPMLProvider) *Server {
	return &Server{Store: s, Auth: a, OPML: opml, Now: time.Now, MaxPage: 100}
}

// Routes registers every Reader endpoint on mux. The returned handler
// wraps mux to bypass `http.ServeMux`'s URL cleaning for the
// stream/contents endpoint (whose suffix is a raw feed URL that may
// legitimately contain `//`).
func (s *Server) Routes(mux *http.ServeMux) http.Handler {
	mux.HandleFunc("/accounts/ClientLogin", s.handleClientLogin)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/reader/api/0/token", s.requireAuth(s.handleToken))
	mux.HandleFunc("/reader/api/0/user-info", s.requireAuth(s.handleUserInfo))
	mux.HandleFunc("/reader/api/0/subscription/list", s.requireAuth(s.handleSubscriptionList))
	mux.HandleFunc("/reader/api/0/subscription/edit", s.requireAuth(s.handleSubscriptionEdit))
	mux.HandleFunc("/reader/api/0/subscription/quickadd", s.requireAuth(s.handleQuickAdd))
	mux.HandleFunc("/reader/api/0/tag/list", s.requireAuth(s.handleTagList))
	mux.HandleFunc("/reader/api/0/rename-tag", s.requireAuth(s.handleRenameTag))
	mux.HandleFunc("/reader/api/0/disable-tag", s.requireAuth(s.handleDisableTag))
	mux.HandleFunc("/reader/api/0/stream/items/ids", s.requireAuth(s.handleItemsIDs))
	mux.HandleFunc("/reader/api/0/stream/items/contents", s.requireAuth(s.handleItemsContents))
	mux.HandleFunc("/reader/api/0/edit-tag", s.requireAuth(s.handleEditTag))
	mux.HandleFunc("/reader/api/0/mark-all-as-read", s.requireAuth(s.handleMarkAllRead))
	mux.HandleFunc("/reader/api/0/unread-count", s.requireAuth(s.handleUnreadCount))

	streamContents := s.requireAuth(s.handleStreamContents)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intercept stream/contents directly with the raw, un-normalised
		// path so feed-URLs with `//` survive.
		if strings.HasPrefix(r.URL.Path, "/reader/api/0/stream/contents/") {
			streamContents(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return gzipMiddleware(h)
}

// requireAuth wraps a handler with ClientLogin token verification.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Most clients use POST with form params; ensure form is parsed.
		_ = r.ParseForm()
		tok := auth.ExtractAPIToken(r)
		if !s.Auth.CheckAPIToken(tok) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// --- handlers ---

func (s *Server) handleClientLogin(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	email := r.FormValue("Email")
	if email == "" {
		email = r.FormValue("email")
	}
	pass := r.FormValue("Passwd")
	if pass == "" {
		pass = r.FormValue("passwd")
	}
	tok, err := s.Auth.IssueAPIToken(email, pass)
	if err != nil {
		http.Error(w, "BadAuthentication", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "SID=%s\nLSID=%s\nAuth=%s\n", tok, tok, tok)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// Echo the same token; some clients PUT this into T= subsequently.
	w.Write([]byte(auth.ExtractAPIToken(r)))
}

func (s *Server) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"userId":          "1",
		"userName":        s.Auth.Cfg.Username,
		"userEmail":       s.Auth.Cfg.Username,
		"userProfileId":   "1",
		"harborrsVersion": s.Version,
	})
}

// handleStatus returns a small unauthenticated JSON document identifying
// the running binary. Useful for monitoring, version-pinning scripts,
// and clients that want to know who they're talking to before going
// through the ClientLogin dance. Vendor-prefixed field names keep the
// payload out of any namespace collision with the Reader API.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"product":   "harborrs",
		"version":   s.Version,
		"commit":    s.Commit,
		"buildDate": s.BuildDate,
	})
}

// subItem is the JSON shape of one entry in subscription/list.
type subItem struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Categories []subCategory `json:"categories"`
	URL        string        `json:"url"`
	HTMLURL    string        `json:"htmlUrl,omitempty"`
	IconURL    string        `json:"iconUrl,omitempty"`
	SortID     string        `json:"sortid,omitempty"`
}
type subCategory struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func feedStreamID(url string) string   { return "feed/" + url }
func labelStreamID(name string) string { return "user/-/label/" + name }

const (
	streamReadingList = "user/-/state/com.google/reading-list"
	streamStarred     = "user/-/state/com.google/starred"
	streamRead        = "user/-/state/com.google/read"
	stateReadID       = "user/-/state/com.google/read"
	stateStarredID    = "user/-/state/com.google/starred"
)

func (s *Server) handleSubscriptionList(w http.ResponseWriter, r *http.Request) {
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := struct {
		Subscriptions []subItem `json:"subscriptions"`
	}{}
	for _, f := range op.Feeds {
		item := subItem{
			ID:      feedStreamID(f.XMLURL),
			Title:   f.Title,
			URL:     f.XMLURL,
			HTMLURL: f.HTMLURL,
		}
		for _, t := range f.Tags {
			item.Categories = append(item.Categories, subCategory{ID: labelStreamID(t), Label: t})
		}
		out.Subscriptions = append(out.Subscriptions, item)
	}
	writeJSON(w, out)
}

func (s *Server) handleSubscriptionEdit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	streamID := r.FormValue("s")
	url := strings.TrimPrefix(streamID, "feed/")
	if url == "" {
		http.Error(w, "missing s", http.StatusBadRequest)
		return
	}
	ac := r.FormValue("ac")
	stripLabels := func(vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			v = strings.TrimPrefix(v, "user/-/label/")
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	switch ac {
	case "subscribe":
		title := r.FormValue("t")
		if title == "" {
			title = url
		}
		tags := stripLabels(r.Form["a"])
		op.Add(store.Feed{XMLURL: url, Title: title, Tags: tags})
	case "unsubscribe":
		op.Remove(url)
	case "edit":
		f := op.Find(url)
		if f == nil {
			http.Error(w, "not found", http.StatusBadRequest)
			return
		}
		if t := r.FormValue("t"); t != "" {
			f.Title = t
		}
		for _, t := range stripLabels(r.Form["a"]) {
			f.AddTag(t)
		}
		for _, t := range stripLabels(r.Form["r"]) {
			f.RemoveTag(t)
		}
	default:
		http.Error(w, "bad ac", http.StatusBadRequest)
		return
	}
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeText(w, "OK")
}

func (s *Server) handleQuickAdd(w http.ResponseWriter, r *http.Request) {
	url := r.FormValue("quickadd")
	if url == "" {
		http.Error(w, "missing quickadd", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op.Add(store.Feed{XMLURL: url, Title: url})
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"query":      url,
		"numResults": 1,
		"streamId":   feedStreamID(url),
	})
}

func (s *Server) handleTagList(w http.ResponseWriter, r *http.Request) {
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type tag struct {
		ID   string `json:"id"`
		Type string `json:"type,omitempty"`
	}
	out := struct {
		Tags []tag `json:"tags"`
	}{
		Tags: []tag{
			{ID: stateStarredID},
			{ID: stateReadID},
		},
	}
	for _, n := range op.AllTags() {
		out.Tags = append(out.Tags, tag{ID: labelStreamID(n), Type: "folder"})
	}
	writeJSON(w, out)
}

// handleRenameTag implements `/reader/api/0/rename-tag`: rewrites every
// feed's Tags so that `s=user/-/label/<old>` becomes
// `dest=user/-/label/<new>`. Missing or malformed params → 400; an
// unknown source tag is a no-op + 200 (idempotent rename semantics).
func (s *Server) handleRenameTag(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldName := strings.TrimPrefix(r.FormValue("s"), "user/-/label/")
	newName := strings.TrimPrefix(r.FormValue("dest"), "user/-/label/")
	if oldName == "" || newName == "" {
		http.Error(w, "missing s/dest", http.StatusBadRequest)
		return
	}
	if store.IsReservedTag(newName) {
		http.Error(w, "reserved tag name", http.StatusBadRequest)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op.RenameTag(oldName, newName)
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeText(w, "OK")
}

// handleDisableTag implements `/reader/api/0/disable-tag`: drops the
// given tag from every feed. The feeds themselves remain subscribed.
func (s *Server) handleDisableTag(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.TrimPrefix(r.FormValue("s"), "user/-/label/")
	if name == "" {
		http.Error(w, "missing s", http.StatusBadRequest)
		return
	}
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op.DisableTag(name)
	if err := s.OPML.Save(op); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeText(w, "OK")
}

// streamItem is one item in a stream/contents response.
type streamItem struct {
	ID            string        `json:"id"`
	LongID        string        `json:"longId"`
	Categories    []string      `json:"categories"`
	Title         string        `json:"title"`
	Published     int64         `json:"published"`
	Updated       int64         `json:"updated"`
	CrawlTimeMsec string        `json:"crawlTimeMsec"`
	TimestampUsec string        `json:"timestampUsec"`
	Author        string        `json:"author,omitempty"`
	Alternate     []streamLink  `json:"alternate"`
	Summary       streamContent `json:"summary"`
	Origin        streamOrigin  `json:"origin"`
}
type streamLink struct {
	HREF string `json:"href"`
	Type string `json:"type,omitempty"`
}
type streamContent struct {
	Direction string `json:"direction,omitempty"`
	Content   string `json:"content"`
}
type streamOrigin struct {
	StreamID string `json:"streamId"`
	Title    string `json:"title,omitempty"`
	HTMLURL  string `json:"htmlUrl,omitempty"`
}
type streamResponse struct {
	ID           string       `json:"id"`
	Updated      int64        `json:"updated"`
	Title        string       `json:"title,omitempty"`
	Items        []streamItem `json:"items"`
	Continuation string       `json:"continuation,omitempty"`
}

func itemID(hash string) string { return "tag:google.com,2005:reader/item/" + readerItemHex(hash) }

func itemLongID(hash string) string {
	h := readerItemHex(hash)
	n, err := strconv.ParseUint(h, 16, 64)
	if err != nil {
		return "0"
	}
	// Google Reader item ids are unsigned 64-bit decimals on the wire.
	// Reeder (and likely other strict clients) silently drop items whose
	// `id` / `longId` parse as negative signed decimals, which is why a
	// roughly-half-of-items display gap appears: feeds whose sha1-derived
	// 16-hex hashes start with 8..f map to negative int64 and get culled.
	return strconv.FormatUint(n, 10)
}

func readerItemHex(hash string) string { return store.CanonicalEntryHash(hash) }

func itemIDToHash(id string) string {
	if strings.HasPrefix(id, "tag:google.com,2005:reader/item/") {
		id = strings.TrimPrefix(id, "tag:google.com,2005:reader/item/")
	}
	// Decimal forms: unsigned (current; emitted by itemLongID) or signed
	// (legacy / older Reader clients). Try unsigned first, then signed.
	if n, err := strconv.ParseUint(id, 10, 64); err == nil {
		return fmt.Sprintf("%016x", n)
	}
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		return fmt.Sprintf("%016x", uint64(n))
	}
	return store.CanonicalEntryHash(id)
}

func (s *Server) handleStreamContents(w http.ResponseWriter, r *http.Request) {
	streamID := strings.TrimPrefix(r.URL.Path, "/reader/api/0/stream/contents/")
	items, err := s.collectStream(streamID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items = s.applyRequestFilters(items, r, streamID)
	s.sortForRequest(items, r)
	s.writeStreamPage(w, streamID, items, r)
}

func (s *Server) handleItemsContents(w http.ResponseWriter, r *http.Request) {
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wantOrder := []string{}
	want := map[string]bool{}
	for _, id := range r.Form["i"] {
		h := itemIDToHash(id)
		if h == "" || want[h] {
			continue
		}
		want[h] = true
		wantOrder = append(wantOrder, h)
	}
	if len(wantOrder) == 0 {
		writeJSON(w, streamResponse{
			ID:      streamReadingList,
			Updated: s.Now().Unix(),
			Items:   []streamItem{},
		})
		return
	}
	found := map[string]store.Entry{}
	for h := range want {
		if e, ok := s.Store.EntryByHash(h); ok {
			found[h] = e
		}
	}
	entries := make([]store.Entry, 0, len(wantOrder))
	for _, h := range wantOrder {
		if e, ok := found[h]; ok {
			entries = append(entries, e)
		}
	}
	writeJSON(w, streamResponse{
		ID:      streamReadingList,
		Updated: s.Now().Unix(),
		Items:   s.toStreamItems(entries, op),
	})
}

// collectStream gathers entries for a stream id. Only the common stream
// kinds are supported; unknown ids resolve to "all entries".
func (s *Server) collectStream(streamID string) ([]store.Entry, error) {
	op, err := s.OPML.Load()
	if err != nil {
		return nil, err
	}
	var entries []store.Entry
	gather := func(filter func(store.Feed) bool) {
		for _, f := range op.Feeds {
			if !filter(f) {
				continue
			}
			fh := store.FeedHash(f.XMLURL)
			entries = append(entries, s.Store.IndexedEntries(fh)...)
		}
	}
	switch {
	case strings.HasPrefix(streamID, "feed/"):
		url := strings.TrimPrefix(streamID, "feed/")
		gather(func(f store.Feed) bool { return f.XMLURL == url })
	case strings.HasPrefix(streamID, "user/-/label/"):
		tag := strings.TrimPrefix(streamID, "user/-/label/")
		gather(func(f store.Feed) bool { return f.HasTag(tag) })
	case streamID == streamStarred:
		gather(func(store.Feed) bool { return true })
		entries = filterEntries(entries, func(e store.Entry) bool {
			return s.Store.EntryState(e.Hash).Starred
		})
	case streamID == streamRead:
		gather(func(store.Feed) bool { return true })
		entries = filterEntries(entries, func(e store.Entry) bool {
			return s.Store.EntryState(e.Hash).Read
		})
	default:
		// reading-list and everything else: all entries. Unread-only
		// views are expressed as reading-list/feed streams plus
		// xt=user/-/state/com.google/read. Reeder relies on this Google
		// Reader convention when seeding a fresh account.
		gather(func(store.Feed) bool { return true })
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Published.After(entries[j].Published)
	})
	return entries, nil
}

func filterEntries(es []store.Entry, ok func(store.Entry) bool) []store.Entry {
	out := es[:0]
	for _, e := range es {
		if ok(e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *Server) applyRequestFilters(es []store.Entry, r *http.Request, streamID string) []store.Entry {
	includes := r.Form["it"]
	excludes := r.Form["xt"]
	var after, before time.Time
	if v := r.FormValue("ot"); v != "" {
		after = parseReaderUnixTime(v)
	}
	if v := r.FormValue("nt"); v != "" {
		before = parseReaderUnixTime(v)
	}
	if len(includes) == 0 && len(excludes) == 0 && after.IsZero() && before.IsZero() {
		return es
	}
	// For state streams (read / starred) the GReader contract for ot/nt
	// is "items whose READ STATE changed in the window", not "items
	// fetched in the window". Reeder relies on this to incrementally
	// sync read flags — comparing against entry fetch/publish time
	// re-streams every recently-polled item on every sync and clobbers
	// the client's own unread display. For content streams the
	// publish/fetch time semantics are correct and unchanged.
	timeOf := entrySyncTime
	if streamID == streamRead || streamID == streamStarred {
		timeOf = func(e store.Entry) time.Time {
			return s.Store.EntryState(e.Hash).UpdatedAt
		}
	}
	return filterEntries(es, func(e store.Entry) bool {
		for _, inc := range includes {
			if !s.entryHasState(e, inc) {
				return false
			}
		}
		for _, exc := range excludes {
			if s.entryHasState(e, exc) {
				return false
			}
		}
		t := timeOf(e)
		if !after.IsZero() && !t.After(after) {
			return false
		}
		if !before.IsZero() && !t.Before(before) {
			return false
		}
		return true
	})
}

func entrySyncTime(e store.Entry) time.Time {
	if e.FetchedAt.After(e.Published) {
		return e.FetchedAt
	}
	return e.Published
}

func entryDisplayTime(e store.Entry) time.Time {
	if !e.Published.IsZero() {
		return e.Published
	}
	return e.FetchedAt
}

func parseReaderUnixTime(v string) time.Time {
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil || i <= 0 {
		return time.Time{}
	}
	// Google Reader docs specify seconds for ot/nt, but some clients use
	// millisecond or microsecond epoch values on adjacent endpoints. Accept
	// all three so a timestamp unit mismatch cannot make Reeder's read-state
	// delta query return an empty or wildly over-broad page.
	switch {
	case i >= 1_000_000_000_000_000:
		return time.UnixMicro(i).UTC()
	case i >= 1_000_000_000_000:
		return time.UnixMilli(i).UTC()
	default:
		return time.Unix(i, 0).UTC()
	}
}

func (s *Server) entryHasState(e store.Entry, state string) bool {
	switch state {
	case streamReadingList:
		return true
	case stateReadID:
		return s.Store.EntryState(e.Hash).Read
	case stateStarredID:
		return s.Store.EntryState(e.Hash).Starred
	default:
		return false
	}
}

func (s *Server) sortForRequest(es []store.Entry, r *http.Request) {
	if r.FormValue("r") != "o" {
		return
	}
	sort.Slice(es, func(i, j int) bool {
		return es[i].Published.Before(es[j].Published)
	})
}

func directStreamsByFeed(op *store.OPML) map[string][]string {
	out := map[string][]string{}
	for _, f := range op.Feeds {
		ids := []string{feedStreamID(f.XMLURL)}
		for _, tag := range f.Tags {
			ids = append(ids, labelStreamID(tag))
		}
		out[store.FeedHash(f.XMLURL)] = ids
	}
	return out
}

// writeStreamPage paginates entries and writes a streamResponse.
func (s *Server) writeStreamPage(w http.ResponseWriter, streamID string, entries []store.Entry, r *http.Request) {
	op, _ := s.OPML.Load()
	n := s.MaxPage
	if v := r.FormValue("n"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 && i < s.MaxPage {
			n = i
		}
	}
	offset := 0
	if c := r.FormValue("c"); c != "" {
		if dec, err := base64.RawURLEncoding.DecodeString(c); err == nil {
			var st struct {
				Offset int `json:"o"`
			}
			if json.Unmarshal(dec, &st) == nil && st.Offset > 0 {
				offset = st.Offset
			}
		}
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	hi := offset + n
	cont := ""
	if hi < len(entries) {
		raw, _ := json.Marshal(struct {
			Offset int `json:"o"`
		}{Offset: hi})
		cont = base64.RawURLEncoding.EncodeToString(raw)
	} else if hi > len(entries) {
		hi = len(entries)
	}
	page := entries[offset:hi]
	resp := streamResponse{
		ID:           streamID,
		Updated:      s.Now().Unix(),
		Items:        s.toStreamItems(page, op),
		Continuation: cont,
	}
	writeJSON(w, resp)
}

func (s *Server) toStreamItems(es []store.Entry, op *store.OPML) []streamItem {
	out := make([]streamItem, 0, len(es))
	feedTitle := map[string]string{}
	feedHTML := map[string]string{}
	feedURL := map[string]string{}
	feedTags := map[string][]string{}
	if op != nil {
		for _, f := range op.Feeds {
			fh := store.FeedHash(f.XMLURL)
			feedTitle[fh] = f.Title
			feedHTML[fh] = f.HTMLURL
			feedURL[fh] = f.XMLURL
			feedTags[fh] = append([]string{}, f.Tags...)
		}
	}
	for _, e := range es {
		st := s.Store.EntryState(e.Hash)
		cats := []string{streamReadingList}
		for _, tag := range feedTags[e.FeedHash] {
			cats = append(cats, labelStreamID(tag))
		}
		if st.Read {
			cats = append(cats, stateReadID)
		}
		if st.Starred {
			cats = append(cats, stateStarredID)
		}
		displayTime := entryDisplayTime(e)
		ts := displayTime.Unix()
		body := e.Content
		if body == "" {
			body = e.Summary
		}
		out = append(out, streamItem{
			ID:            itemID(e.Hash),
			LongID:        itemLongID(e.Hash),
			Categories:    cats,
			Title:         e.Title,
			Published:     ts,
			Updated:       ts,
			CrawlTimeMsec: strconv.FormatInt(e.FetchedAt.UnixMilli(), 10),
			TimestampUsec: strconv.FormatInt(displayTime.UnixMicro(), 10),
			Author:        e.Author,
			Alternate:     []streamLink{{HREF: e.Link, Type: "text/html"}},
			Summary:       streamContent{Content: body},
			Origin: streamOrigin{
				StreamID: feedStreamID(feedURL[e.FeedHash]),
				Title:    feedTitle[e.FeedHash],
				HTMLURL:  feedHTML[e.FeedHash],
			},
		})
	}
	return out
}

// handleItemsIDs returns just the item ids for a stream (used to seed
// "what's new" queries). Supports the `c=` continuation token in the same
// shape as writeStreamPage, so clients with more than MaxPage unread items
// can walk the full set.
func (s *Server) handleItemsIDs(w http.ResponseWriter, r *http.Request) {
	streamID := r.FormValue("s")
	if streamID == "" {
		streamID = streamReadingList
	}
	entries, err := s.collectStream(streamID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries = s.applyRequestFilters(entries, r, streamID)
	s.sortForRequest(entries, r)
	directStreams := map[string][]string{}
	if op, err := s.OPML.Load(); err == nil {
		directStreams = directStreamsByFeed(op)
	}
	n := s.MaxPage
	if v := r.FormValue("n"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			if i > s.MaxPage {
				i = s.MaxPage
			}
			n = i
		}
	}
	offset := 0
	if c := r.FormValue("c"); c != "" {
		if dec, err := base64.RawURLEncoding.DecodeString(c); err == nil {
			var st struct {
				Offset int `json:"o"`
			}
			if json.Unmarshal(dec, &st) == nil && st.Offset > 0 {
				offset = st.Offset
			}
		}
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	hi := offset + n
	cont := ""
	if hi < len(entries) {
		raw, _ := json.Marshal(struct {
			Offset int `json:"o"`
		}{Offset: hi})
		cont = base64.RawURLEncoding.EncodeToString(raw)
	} else if hi > len(entries) {
		hi = len(entries)
	}
	type ref struct {
		ID            string   `json:"id"`
		LongID        string   `json:"longId"`
		DirectStreams []string `json:"directStreamIds,omitempty"`
		TimestampUsec string   `json:"timestampUsec"`
	}
	out := struct {
		ItemRefs     []ref  `json:"itemRefs"`
		Continuation string `json:"continuation,omitempty"`
	}{ItemRefs: []ref{}, Continuation: cont}
	for _, e := range entries[offset:hi] {
		longID := itemLongID(e.Hash)
		out.ItemRefs = append(out.ItemRefs, ref{
			// The Google Reader stream/items/ids contract uses the signed
			// decimal item id here. Reeder matches these refs against the
			// later stream/items/contents payload via longId / parsed item
			// id; returning tag-form ids here makes the client lose unread
			// membership and show freshly fetched entries as already read.
			ID:            longID,
			LongID:        longID,
			DirectStreams: directStreams[e.FeedHash],
			TimestampUsec: strconv.FormatInt(entryDisplayTime(e).UnixMicro(), 10),
		})
	}
	writeJSON(w, out)
}

// handleEditTag toggles read / starred on items.
func (s *Server) handleEditTag(w http.ResponseWriter, r *http.Request) {
	ids := r.Form["i"]
	if len(ids) == 0 {
		http.Error(w, "missing i", http.StatusBadRequest)
		return
	}
	add := r.Form["a"]
	rem := r.Form["r"]
	apply := func(state string, on bool) error {
		for _, id := range ids {
			h := itemIDToHash(id)
			switch state {
			case stateReadID:
				if err := s.Store.SetRead(h, on); err != nil {
					return err
				}
			case stateStarredID:
				if err := s.Store.SetStarred(h, on); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, a := range add {
		if err := apply(a, true); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, rr := range rem {
		if err := apply(rr, false); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeText(w, "OK")
}

// handleMarkAllRead marks every entry older-than-ts in a stream as read.
func (s *Server) handleMarkAllRead(w http.ResponseWriter, r *http.Request) {
	streamID := r.FormValue("s")
	if streamID == "" {
		http.Error(w, "missing s", http.StatusBadRequest)
		return
	}
	var cutoff time.Time
	if ts := r.FormValue("ts"); ts != "" {
		if usec, err := strconv.ParseInt(ts, 10, 64); err == nil {
			cutoff = time.UnixMicro(usec).UTC()
		}
	}
	entries, err := s.collectStream(streamID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		if !cutoff.IsZero() && e.Published.After(cutoff) {
			continue
		}
		if err := s.Store.SetRead(e.Hash, true); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeText(w, "OK")
}

// handleUnreadCount returns per-feed unread counts + an overall reading-
// list total.
func (s *Server) handleUnreadCount(w http.ResponseWriter, r *http.Request) {
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type uc struct {
		ID                      string `json:"id"`
		Count                   int    `json:"count"`
		NewestItemTimestampUsec string `json:"newestItemTimestampUsec"`
	}
	out := struct {
		Max          int  `json:"max"`
		UnreadCounts []uc `json:"unreadcounts"`
	}{Max: 1000}
	total := 0
	var globalNewest int64
	for _, f := range op.Feeds {
		fh := store.FeedHash(f.XMLURL)
		es := s.Store.IndexedEntries(fh)
		count := 0
		var newest int64
		for _, e := range es {
			if s.Store.EntryState(e.Hash).Read {
				continue
			}
			count++
			if ts := e.FetchedAt.UnixMicro(); ts > newest {
				newest = ts
			}
		}
		if newest > globalNewest {
			globalNewest = newest
		}
		total += count
		out.UnreadCounts = append(out.UnreadCounts, uc{
			ID:                      feedStreamID(f.XMLURL),
			Count:                   count,
			NewestItemTimestampUsec: strconv.FormatInt(newest, 10),
		})
	}
	out.UnreadCounts = append(out.UnreadCounts, uc{
		ID:                      streamReadingList,
		Count:                   total,
		NewestItemTimestampUsec: strconv.FormatInt(globalNewest, 10),
	})
	writeJSON(w, out)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		// Tail-of-response error — log silently; nothing left to do.
		_ = err
	}
}

func writeText(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(body))
}

// Ensure error import isn't pruned by goimports during refactors.
var _ = errors.New
