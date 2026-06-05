package resolve

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

// limit is parsed, capped at 200, and rejected when non-positive.
func TestWebflowToFeed_LimitParseAndCap(t *testing.T) {
	r, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{"limit": "500"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(webflowToFeed).limit; got != 200 {
		t.Errorf("limit cap = %d, want 200", got)
	}
	r2, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{"limit": "7"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.(webflowToFeed).limit; got != 7 {
		t.Errorf("limit = %d, want 7", got)
	}
	if _, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{"limit": "0"}}); err == nil {
		t.Error("expected error for limit=0")
	}
}

// With no <title> and no title param, the channel title falls back to the
// host of the feed URL.
func TestWebflowToFeed_TitleHostFallback(t *testing.T) {
	r, _ := Build(Spec{Name: "webflow-to-feed"})
	in := `<html data-wf-site="x"><body>
		<div class="w-dyn-item"><a href="/p"><h3>Post</h3></a></div>
	</body></html>`
	out, err := r.Transform([]byte(in), FeedMeta{URL: "https://host.test/blog", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "host.test" {
		t.Errorf("channel title = %q, want host.test", f.Title)
	}
}

// Item-loop edge cases: the item node may itself be the anchor; an item
// with no link is skipped; title falls back to the link text; an item
// with no title at all is skipped; a <time> without datetime uses text.
func TestWebflowToFeed_ItemEdgeCases(t *testing.T) {
	r, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{
		"require_webflow": "false",
		"item_selector":   ".item",
		"title_selector":  "span",
	}})
	if err != nil {
		t.Fatal(err)
	}
	in := `<html><head><title>Edge</title></head><body>
		<a class="item" href="/anchor-item"><span>Anchor Item</span></a>
		<div class="item"><span>no link here</span></div>
		<div class="item"><a href="/link-text-title">Link Text Title</a></div>
		<div class="item"><a href="/empty-title"></a></div>
		<div class="item"><a href="/with-date"><h3>Dated</h3></a><time>May 1, 2026</time></div>
	</body></html>`
	out, err := r.Transform([]byte(in), FeedMeta{URL: "https://e.test/", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	// anchor-item (link text used as title via title_selector miss? no:
	// title_selector default h1..h4 misses, falls to link text), link-text-title,
	// with-date => 3 items. no-link and empty-title are skipped.
	if len(f.Items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(f.Items), f.Items)
	}
	if f.Items[0].Title != "Anchor Item" || f.Items[0].Link != "https://e.test/anchor-item" {
		t.Errorf("anchor item wrong: %+v", f.Items[0])
	}
	if f.Items[1].Title != "Link Text Title" {
		t.Errorf("link-text title wrong: %+v", f.Items[1])
	}
	if f.Items[2].Title != "Dated" || f.Items[2].PublishedParsed == nil {
		t.Errorf("dated item wrong (date from <time> text): %+v", f.Items[2])
	}
}

// A Webflow page that yields zero usable items (all excluded) returns the
// body unchanged, so genuine breakage still surfaces downstream.
func TestWebflowToFeed_ZeroItemsPassthrough(t *testing.T) {
	r, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{
		"exclude_link_contains": "/category/",
	}})
	if err != nil {
		t.Fatal(err)
	}
	in := `<html data-wf-site="x"><head><title>B</title></head><body>
		<div class="w-dyn-item"><a href="/category/news"><h3>News</h3></a></div>
	</body></html>`
	out, err := r.Transform([]byte(in), FeedMeta{URL: "https://b.test/blog", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Errorf("zero-item page was modified:\n%s", out)
	}
}

func TestIsWebflow(t *testing.T) {
	mk := func(html string) *goquery.Document {
		d, _ := goquery.NewDocumentFromReader(strings.NewReader(html))
		return d
	}
	cases := map[string]bool{
		`<div class="w-dyn-item"></div>`:                                      true,
		`<html data-wf-site="x"></html>`:                                      true,
		`<html data-wf-page="y"></html>`:                                      true,
		`<html><head><meta name="generator" content="Webflow"></head></html>`: true,
		`<html><head><title>plain</title></head></html>`:                      false,
	}
	for html, want := range cases {
		if got := isWebflow(mk(html)); got != want {
			t.Errorf("isWebflow(%q) = %v, want %v", html, got, want)
		}
	}
}

func TestHostOf(t *testing.T) {
	if got := hostOf(""); got != "Webflow feed" {
		t.Errorf("hostOf(empty) = %q", got)
	}
	if got := hostOf("https://x.test/a"); got != "x.test" {
		t.Errorf("hostOf(url) = %q", got)
	}
	if got := hostOf("plainstring"); got != "plainstring" {
		t.Errorf("hostOf(nohost) = %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", ""); got != "" {
		t.Errorf("firstNonEmpty(all empty) = %q", got)
	}
	if got := firstNonEmpty("", "x"); got != "x" {
		t.Errorf("firstNonEmpty = %q", got)
	}
}

func TestAbsolutiseEdges(t *testing.T) {
	if got := absolutise("https://x.test", "%zz"); got != "" {
		t.Errorf("bad href: got %q, want empty", got)
	}
	if got := absolutise("", "/rel"); got != "/rel" {
		t.Errorf("empty base: got %q, want /rel", got)
	}
	if got := absolutise("nohost", "/rel"); got != "/rel" {
		t.Errorf("hostless base: got %q, want /rel", got)
	}
}

// --- builtin auto-apply --------------------------------------------------

// A Webflow page with no sidecar is turned into a feed by the builtin
// chain; the default /category/,/tag/ exclusion drops taxonomy items.
func TestBuiltinWebflowAutoApplies(t *testing.T) {
	chain, err := Load(t.TempDir(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	in := `<html data-wf-site="x"><head><title>Auto Blog</title></head><body>
		<div class="w-dyn-item"><a href="/blog/real-one"><h3>Real One</h3></a></div>
		<div class="w-dyn-item"><a href="/blog/category/news"><h3>Cat</h3></a></div>
		<div class="w-dyn-item"><a href="/blog/tag/foo"><h3>Tagged</h3></a></div>
		<div class="w-dyn-item"><a href="/blog/real-two"><h3>Real Two</h3></a></div>
	</body></html>`
	out, err := chain.Transform([]byte(in), FeedMeta{URL: "https://auto.test/blog", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatalf("synthetic feed did not parse: %v\n%s", err, out)
	}
	if f.Title != "Auto Blog" {
		t.Errorf("title = %q", f.Title)
	}
	if len(f.Items) != 2 || f.Items[0].Title != "Real One" || f.Items[1].Title != "Real Two" {
		t.Fatalf("default exclusion failed, items: %+v", f.Items)
	}
}

// A plain non-Webflow HTML page passes through the builtin chain
// byte-identical.
func TestBuiltinWebflowNonWebflowPassthrough(t *testing.T) {
	chain, err := Load(t.TempDir(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	in := `<html><head><title>plain</title></head><body><p>hi</p><a href="/x">x</a></body></html>`
	out, err := chain.Transform([]byte(in), FeedMeta{URL: "https://plain.test/", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Errorf("non-webflow HTML was modified:\n%s", out)
	}
}

// The builtin gate skips a real XML feed (application/rss+xml): it is not
// applied at all, so the body is untouched.
func TestBuiltinWebflowSkipsXMLFeed(t *testing.T) {
	chain, err := Load(t.TempDir(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>Real</title>` +
		`<item><title>i</title><link>https://r.test/i</link></item></channel></rss>`
	out, err := chain.Transform([]byte(feed), FeedMeta{URL: "https://r.test/feed", ContentType: "application/rss+xml"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != feed {
		t.Errorf("xml feed was modified:\n%s", out)
	}
}

// Double-apply safety: builtin + a sidecar webflow instance both run. The
// builtin synthesises RSS; the sidecar pass sees XML and is a no-op, so
// the result is a single clean feed (not double-transformed garbage).
func TestBuiltinWebflowDoubleApplyIdempotent(t *testing.T) {
	dir := t.TempDir()
	fh := "doubled"
	writeSidecar(t, dir, fh, []Spec{
		{Name: "webflow-to-feed", Source: "user"},
	})
	chain, err := Load(dir, fh)
	if err != nil {
		t.Fatal(err)
	}
	// builtins (strip, webflow) + sidecar webflow => 3 resolvers.
	if got := chain.Names(); len(got) != 3 || got[1] != "webflow-to-feed" || got[2] != "webflow-to-feed" {
		t.Fatalf("chain=%v", got)
	}
	in := `<html data-wf-site="x"><head><title>Dbl</title></head><body>
		<div class="w-dyn-item"><a href="/p/1"><h3>One</h3></a></div>
		<div class="w-dyn-item"><a href="/p/2"><h3>Two</h3></a></div>
	</body></html>`
	out, err := chain.Transform([]byte(in), FeedMeta{URL: "https://dbl.test/blog", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatalf("double-apply produced unparseable output: %v\n%s", err, out)
	}
	if len(f.Items) != 2 || f.Title != "Dbl" {
		t.Fatalf("double-apply not idempotent, feed: title=%q items=%+v", f.Title, f.Items)
	}
}

func TestLooksLikeXML(t *testing.T) {
	yes := []string{
		`<?xml version="1.0"?><rss/>`,
		"   \n\t<rss version=\"2.0\">",
		"<feed xmlns=\"http://www.w3.org/2005/Atom\">",
		"<RDF:RDF>",
		`<?XML?>`,
	}
	for _, s := range yes {
		if !looksLikeXML([]byte(s)) {
			t.Errorf("looksLikeXML(%q) = false, want true", s)
		}
	}
	no := []string{
		`<!DOCTYPE html><html>`,
		"<html><body>x</body></html>",
		"  plain text",
		"",
	}
	for _, s := range no {
		if looksLikeXML([]byte(s)) {
			t.Errorf("looksLikeXML(%q) = true, want false", s)
		}
	}
}
