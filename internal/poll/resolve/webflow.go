package resolve

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// WebflowGenerator is written into the synthetic channel's <generator>
// element so downstream consumers — specifically the poller's article-text
// enrichment — can recognise a feed harb synthesised from a Webflow page
// without coupling to the resolver internals. Detection is a simple equal
// against parsed.Generator.
const WebflowGenerator = "harb-webflow-to-feed"

func init() {
	Register("webflow-to-feed", newWebflowToFeed)
}

// --- webflow-to-feed -----------------------------------------------------

// webflowToFeed turns a Webflow CMS collection page (a blog index that
// ships no RSS/Atom feed) into a synthetic RSS 2.0 document so gofeed can
// parse it like any other feed. Webflow renders CMS collections into the
// server HTML as a `.w-dyn-list` / `.w-dyn-item` structure, so the items
// are present without executing JavaScript — which is exactly what makes
// them scrapeable here.
//
// This is a Transform-stage resolver. It runs in two modes:
//
//   - as a zero-config builtin (see autoWebflowResolver), applied to every
//     feed like strip-control-chars, so a feedless Webflow blog can be
//     added through the UI with no configuration; and
//   - as a sidecar Spec (Source "user"/"agent") scoped to one feed, which
//     can override any parameter below.
//
// Either way the gate defaults to text/html so it never touches a response
// that is already a real feed, and the looksLikeXML guard in Transform
// makes a second pass (builtin + sidecar both present) a no-op.
//
// Params (all optional — defaults match a stock Webflow blog):
//
//	content_type_contains  gate substring (default "html")
//	item_selector          repeating item node   (default ".w-dyn-item")
//	link_selector          anchor within an item (default "a[href]")
//	title_selector         title within an item  (default "h1,h2,h3,h4")
//	date_selector          date  within an item  (default "time,[datetime]")
//	base_url               origin for relative links (default: the feed URL)
//	title                  channel title (default derived from <title>/host)
//	limit                  max items to emit (default 50, hard cap 200)
//	exclude_link_contains  drop items whose link contains this substring
//	                       (e.g. "/category/" to skip Webflow nav lists);
//	                       comma-separated for several
//	require_webflow        if "false", skip the Webflow-marker check and
//	                       scrape any page matching item_selector
//
// When the page carries no Webflow markers (and require_webflow is not
// disabled) or yields zero items, Transform returns the body unchanged so
// the failure surfaces exactly as it would without the resolver, rather
// than masking a genuinely broken page.
type webflowToFeed struct {
	base
	itemSel, linkSel, titleSel, dateSel string
	baseURL, chanTitle                  string
	excludeLink                         []string
	limit                               int
	requireWebflow                      bool
}

func newWebflowToFeed(params map[string]string) (Resolver, error) {
	g := gateFrom(params)
	if g.ctContains == "" {
		g.ctContains = "html"
	}
	wf := webflowToFeed{
		base:           base{name: "webflow-to-feed", gate: g},
		itemSel:        firstNonEmpty(params["item_selector"], ".w-dyn-item"),
		linkSel:        firstNonEmpty(params["link_selector"], "a[href]"),
		titleSel:       firstNonEmpty(params["title_selector"], "h1,h2,h3,h4"),
		dateSel:        firstNonEmpty(params["date_selector"], "time,[datetime]"),
		baseURL:        strings.TrimSpace(params["base_url"]),
		chanTitle:      strings.TrimSpace(params["title"]),
		excludeLink:    splitNonEmpty(params["exclude_link_contains"]),
		limit:          50,
		requireWebflow: !strings.EqualFold(strings.TrimSpace(params["require_webflow"]), "false"),
	}
	if v := strings.TrimSpace(params["limit"]); v != "" {
		n, err := parsePositiveInt(v)
		if err != nil {
			return nil, fmt.Errorf("webflow-to-feed: bad %q: %w", "limit", err)
		}
		wf.limit = n
	}
	if wf.limit > 200 {
		wf.limit = 200
	}
	return wf, nil
}

// defaultWebflowExclude is the zero-config taxonomy filter for the builtin
// webflow instance. A stock Webflow blog index also renders its
// /category/ and /tag/ nav lists as .w-dyn-item nodes; dropping links that
// contain these substrings keeps the synthesised feed to real posts. A
// sidecar Spec's exclude_link_contains overrides this default.
const defaultWebflowExclude = "/category/,/tag/"

// autoWebflowResolver is the zero-config builtin instance of
// webflow-to-feed, applied to every feed (see builtinResolvers). It cannot
// fail to construct — its only fallible param (limit) is unset — so the
// error is discarded, keeping it off the poll error path exactly like the
// other builtins.
func autoWebflowResolver() Resolver {
	r, _ := newWebflowToFeed(map[string]string{"exclude_link_contains": defaultWebflowExclude})
	return r
}

// looksLikeXML reports whether body already begins with an XML/feed prolog
// after leading whitespace. It is the idempotency guard for the builtin:
// if both the builtin auto-instance and a sidecar instance of
// webflow-to-feed run, the second sees the RSS the first synthesised — its
// content-type gate still reads "text/html" (the original response CT), so
// this byte-level check, not the gate, is what makes the second pass a
// no-op. It also leaves a real XML feed mislabelled as text/html untouched.
func looksLikeXML(body []byte) bool {
	b := bytes.TrimLeft(body, " \t\r\n")
	if len(b) > 16 {
		b = b[:16]
	}
	b = bytes.ToLower(b)
	return bytes.HasPrefix(b, []byte("<?xml")) ||
		bytes.HasPrefix(b, []byte("<rss")) ||
		bytes.HasPrefix(b, []byte("<feed")) ||
		bytes.HasPrefix(b, []byte("<rdf:"))
}

func (w webflowToFeed) Transform(body []byte, m FeedMeta) ([]byte, error) {
	if looksLikeXML(body) {
		return body, nil // already a feed (real, or one we just synthesised)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return body, nil // not parseable as HTML: leave it for the next stage
	}
	if w.requireWebflow && !isWebflow(doc) {
		return body, nil
	}

	root := firstNonEmpty(w.baseURL, m.URL)
	channelTitle := w.chanTitle
	if channelTitle == "" {
		channelTitle = strings.TrimSpace(doc.Find("title").First().Text())
	}
	if channelTitle == "" {
		channelTitle = hostOf(root)
	}

	type item struct{ title, link, date string }
	var items []item
	seen := map[string]bool{}

	doc.Find(w.itemSel).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, ok := s.Find(w.linkSel).First().Attr("href")
		if !ok {
			// the item node may itself be the anchor
			if h, ok2 := s.Attr("href"); ok2 {
				href = h
			} else {
				return true
			}
		}
		link := absolutise(root, strings.TrimSpace(href))
		if link == "" || seen[link] || w.excluded(link) {
			return true
		}
		title := strings.TrimSpace(s.Find(w.titleSel).First().Text())
		if title == "" {
			title = strings.TrimSpace(s.Find(w.linkSel).First().Text())
		}
		if title == "" {
			return true
		}
		date := findDate(s, w.dateSel)
		seen[link] = true
		items = append(items, item{title: title, link: link, date: date})
		return len(items) < w.limit
	})

	if len(items) == 0 {
		return body, nil
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0"><channel>` + "\n")
	b.WriteString("<title>" + xmlEscape(channelTitle) + "</title>\n")
	if root != "" {
		b.WriteString("<link>" + xmlEscape(root) + "</link>\n")
	}
	b.WriteString("<description>" + xmlEscape("Synthesised from Webflow CMS page by harb") + "</description>\n")
	b.WriteString("<generator>" + xmlEscape(WebflowGenerator) + "</generator>\n")
	for _, it := range items {
		b.WriteString("<item>")
		b.WriteString("<title>" + xmlEscape(it.title) + "</title>")
		b.WriteString("<link>" + xmlEscape(it.link) + "</link>")
		b.WriteString("<guid isPermaLink=\"true\">" + xmlEscape(it.link) + "</guid>")
		if pd := pubDate(it.date); pd != "" {
			b.WriteString("<pubDate>" + xmlEscape(pd) + "</pubDate>")
		}
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel></rss>\n")
	return []byte(b.String()), nil
}

// isWebflow reports whether the document carries Webflow CMS markers. It
// keys on the dynamic-collection classes Webflow emits server-side plus
// the html[data-wf-*] / Webflow generator hints.
func isWebflow(doc *goquery.Document) bool {
	if doc.Find(".w-dyn-list, .w-dyn-items, .w-dyn-item").Length() > 0 {
		return true
	}
	if _, ok := doc.Find("html").Attr("data-wf-site"); ok {
		return true
	}
	if _, ok := doc.Find("html").Attr("data-wf-page"); ok {
		return true
	}
	gen, _ := doc.Find(`meta[name="generator"]`).Attr("content")
	return strings.Contains(strings.ToLower(gen), "webflow")
}

func (w webflowToFeed) excluded(link string) bool {
	for _, sub := range w.excludeLink {
		if strings.Contains(link, sub) {
			return true
		}
	}
	return false
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a non-negative integer: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive: %q", s)
	}
	return n, nil
}

func hostOf(raw string) string {
	if raw == "" {
		return "Webflow feed"
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// absolutise resolves href against base. Absolute hrefs pass through;
// relative ones are joined onto base's origin/path.
func absolutise(base, href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	b, err := url.Parse(base)
	if err != nil || b.Host == "" {
		return href
	}
	return b.ResolveReference(u).String()
}

// findDate extracts a publish date for an item. It first tries the date
// selector — a <time datetime> attribute, else the element's text — and if
// that yields nothing pubDate can parse, it scans the item's full text for
// the first date-like substring. Webflow CMS indexes commonly render the
// date as plain text in a <div> (e.g. "May 19, 2026") rather than a
// <time datetime>, which the selector alone misses. Returns a string
// pubDate() can parse, or "".
func findDate(s *goquery.Selection, dateSel string) string {
	raw := ""
	if d := s.Find(dateSel).First(); d.Length() > 0 {
		if dt, ok := d.Attr("datetime"); ok && strings.TrimSpace(dt) != "" {
			raw = strings.TrimSpace(dt)
		} else {
			raw = strings.TrimSpace(d.Text())
		}
	}
	if pubDate(raw) != "" {
		return raw
	}
	return scanDate(s.Text())
}

// dateScanRe matches candidate date substrings: a "Month D, YYYY" label
// (the capitalised word is validated as a real month by pubDate) and an
// ISO date with an optional RFC3339 time/offset.
var dateScanRe = regexp.MustCompile(`[A-Z][a-z]+ \d{1,2}, \d{4}|\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2})?)?`)

// scanDate returns the first date-like substring in text that pubDate can
// actually parse, or "". The pubDate check guards against false positives
// — a "Foo 3, 2024" that matches the shape but names no real month.
func scanDate(text string) string {
	for _, m := range dateScanRe.FindAllString(text, -1) {
		if pubDate(m) != "" {
			return m
		}
	}
	return ""
}

// pubDate normalises a scraped date into an RFC1123Z string gofeed will
// parse. It accepts an RFC3339 datetime attribute or a handful of common
// human formats; an unrecognised string is dropped (returns "").
func pubDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02",
		"January 2, 2006", "Jan 2, 2006", "2 January 2006", "02 Jan 2006",
		"2006/01/02", "01/02/2006",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC().Format(time.RFC1123Z)
		}
	}
	return ""
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
