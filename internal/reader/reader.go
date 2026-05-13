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
	mux.HandleFunc("/reader/api/0/token", s.requireAuth(s.handleToken))
	mux.HandleFunc("/reader/api/0/user-info", s.requireAuth(s.handleUserInfo))
	mux.HandleFunc("/reader/api/0/subscription/list", s.requireAuth(s.handleSubscriptionList))
	mux.HandleFunc("/reader/api/0/subscription/edit", s.requireAuth(s.handleSubscriptionEdit))
	mux.HandleFunc("/reader/api/0/subscription/quickadd", s.requireAuth(s.handleQuickAdd))
	mux.HandleFunc("/reader/api/0/tag/list", s.requireAuth(s.handleTagList))
	mux.HandleFunc("/reader/api/0/stream/items/ids", s.requireAuth(s.handleItemsIDs))
	mux.HandleFunc("/reader/api/0/stream/items/contents", s.requireAuth(s.handleItemsContents))
	mux.HandleFunc("/reader/api/0/edit-tag", s.requireAuth(s.handleEditTag))
	mux.HandleFunc("/reader/api/0/mark-all-as-read", s.requireAuth(s.handleMarkAllRead))
	mux.HandleFunc("/reader/api/0/unread-count", s.requireAuth(s.handleUnreadCount))

	streamContents := s.requireAuth(s.handleStreamContents)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intercept stream/contents directly with the raw, un-normalised
		// path so feed-URLs with `//` survive.
		if strings.HasPrefix(r.URL.Path, "/reader/api/0/stream/contents/") {
			streamContents(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
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
		"userId":        "1",
		"userName":      s.Auth.Cfg.Username,
		"userEmail":     s.Auth.Cfg.Username,
		"userProfileId": "1",
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
		if f.Folder != "" {
			item.Categories = []subCategory{{ID: labelStreamID(f.Folder), Label: f.Folder}}
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
	switch ac {
	case "subscribe":
		title := r.FormValue("t")
		if title == "" {
			title = url
		}
		folder := strings.TrimPrefix(r.FormValue("a"), "user/-/label/")
		op.Add(store.Feed{XMLURL: url, Title: title, Folder: folder})
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
		if a := r.FormValue("a"); a != "" {
			f.Folder = strings.TrimPrefix(a, "user/-/label/")
		}
		if rem := r.FormValue("r"); rem != "" && f.Folder == strings.TrimPrefix(rem, "user/-/label/") {
			f.Folder = ""
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
	folders := map[string]bool{}
	for _, f := range op.Feeds {
		if f.Folder != "" {
			folders[f.Folder] = true
		}
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
	names := make([]string, 0, len(folders))
	for n := range folders {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out.Tags = append(out.Tags, tag{ID: labelStreamID(n), Type: "folder"})
	}
	writeJSON(w, out)
}

// streamItem is one item in a stream/contents response.
type streamItem struct {
	ID            string        `json:"id"`
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

func itemID(hash string) string { return "tag:google.com,2005:reader/item/" + hash }
func itemIDToHash(id string) string {
	if strings.HasPrefix(id, "tag:google.com,2005:reader/item/") {
		return strings.TrimPrefix(id, "tag:google.com,2005:reader/item/")
	}
	// long-form decimal int64? Best-effort: pad hex to 20 chars and take the
	// trailing 20 (matches FreshRSS's truncation convention).
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		h := fmt.Sprintf("%020x", uint64(n))
		return h[len(h)-20:]
	}
	return id
}

func (s *Server) handleStreamContents(w http.ResponseWriter, r *http.Request) {
	streamID := strings.TrimPrefix(r.URL.Path, "/reader/api/0/stream/contents/")
	items, err := s.collectStream(streamID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeStreamPage(w, streamID, items, r)
}

func (s *Server) handleItemsContents(w http.ResponseWriter, r *http.Request) {
	op, err := s.OPML.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	want := map[string]bool{}
	for _, id := range r.Form["i"] {
		want[itemIDToHash(id)] = true
	}
	if len(want) == 0 {
		writeJSON(w, streamResponse{ID: "items", Items: []streamItem{}})
		return
	}
	var entries []store.Entry
	for _, f := range op.Feeds {
		fh := store.FeedHash(f.XMLURL)
		es, err := s.Store.ListEntries(fh)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, e := range es {
			if want[e.Hash] {
				entries = append(entries, e)
			}
		}
	}
	writeJSON(w, streamResponse{
		ID:    "items",
		Items: s.toStreamItems(entries, op),
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
	gather := func(filter func(store.Feed) bool) error {
		for _, f := range op.Feeds {
			if !filter(f) {
				continue
			}
			fh := store.FeedHash(f.XMLURL)
			es, err := s.Store.ListEntries(fh)
			if err != nil {
				return err
			}
			entries = append(entries, es...)
		}
		return nil
	}
	switch {
	case strings.HasPrefix(streamID, "feed/"):
		url := strings.TrimPrefix(streamID, "feed/")
		if err := gather(func(f store.Feed) bool { return f.XMLURL == url }); err != nil {
			return nil, err
		}
	case strings.HasPrefix(streamID, "user/-/label/"):
		folder := strings.TrimPrefix(streamID, "user/-/label/")
		if err := gather(func(f store.Feed) bool { return f.Folder == folder }); err != nil {
			return nil, err
		}
	case streamID == streamStarred:
		if err := gather(func(store.Feed) bool { return true }); err != nil {
			return nil, err
		}
		entries = filterEntries(entries, func(e store.Entry) bool {
			return s.Store.EntryState(e.Hash).Starred
		})
	case streamID == streamRead:
		if err := gather(func(store.Feed) bool { return true }); err != nil {
			return nil, err
		}
		entries = filterEntries(entries, func(e store.Entry) bool {
			return s.Store.EntryState(e.Hash).Read
		})
	default:
		// reading-list and everything else: all unread entries.
		if err := gather(func(store.Feed) bool { return true }); err != nil {
			return nil, err
		}
		entries = filterEntries(entries, func(e store.Entry) bool {
			return !s.Store.EntryState(e.Hash).Read
		})
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
	if op != nil {
		for _, f := range op.Feeds {
			fh := store.FeedHash(f.XMLURL)
			feedTitle[fh] = f.Title
			feedHTML[fh] = f.HTMLURL
			feedURL[fh] = f.XMLURL
		}
	}
	for _, e := range es {
		st := s.Store.EntryState(e.Hash)
		cats := []string{}
		if st.Read {
			cats = append(cats, stateReadID)
		}
		if st.Starred {
			cats = append(cats, stateStarredID)
		}
		ts := e.Published.Unix()
		body := e.Content
		if body == "" {
			body = e.Summary
		}
		out = append(out, streamItem{
			ID:            itemID(e.Hash),
			Categories:    cats,
			Title:         e.Title,
			Published:     ts,
			Updated:       ts,
			CrawlTimeMsec: strconv.FormatInt(e.FetchedAt.UnixMilli(), 10),
			TimestampUsec: strconv.FormatInt(e.FetchedAt.UnixMicro(), 10),
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
// "what's new" queries).
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
	n := s.MaxPage
	if v := r.FormValue("n"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 && i < s.MaxPage {
			n = i
		}
	}
	if n > len(entries) {
		n = len(entries)
	}
	type ref struct {
		ID            string   `json:"id"`
		DirectStreams []string `json:"directStreamIds,omitempty"`
		TimestampUsec string   `json:"timestampUsec"`
	}
	out := struct {
		ItemRefs []ref `json:"itemRefs"`
	}{}
	for _, e := range entries[:n] {
		out.ItemRefs = append(out.ItemRefs, ref{
			ID:            itemID(e.Hash),
			TimestampUsec: strconv.FormatInt(e.FetchedAt.UnixMicro(), 10),
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
		NewestItemTimestampUsec string `json:"newestItemTimestampUsec,omitempty"`
	}
	out := struct {
		Max          int  `json:"max"`
		UnreadCounts []uc `json:"unreadcounts"`
	}{Max: 1000}
	total := 0
	for _, f := range op.Feeds {
		fh := store.FeedHash(f.XMLURL)
		es, err := s.Store.ListEntries(fh)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		total += count
		entry := uc{ID: feedStreamID(f.XMLURL), Count: count}
		if newest > 0 {
			entry.NewestItemTimestampUsec = strconv.FormatInt(newest, 10)
		}
		out.UnreadCounts = append(out.UnreadCounts, entry)
	}
	out.UnreadCounts = append(out.UnreadCounts, uc{
		ID:    streamReadingList,
		Count: total,
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
