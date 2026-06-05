package resolve

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

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
// This is a Transform-stage resolver. It is meant to be scoped to a
// single feed via a sidecar Spec (Source "user"/"agent") whose target
// URL is the collection page itself; the gate defaults to text/html so it
// never touches a response that is already a real feed.
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

func (w webflowToFeed) Transform(body []byte, m FeedMeta) ([]byte, error) {
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
		date := ""
		if d := s.Find(w.dateSel).First(); d.Length() > 0 {
			if dt, ok := d.Attr("datetime"); ok && strings.TrimSpace(dt) != "" {
				date = strings.TrimSpace(dt)
			} else {
				date = strings.TrimSpace(d.Text())
			}
		}
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
