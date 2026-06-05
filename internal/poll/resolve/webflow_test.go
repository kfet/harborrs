package resolve

import (
	"testing"

	"github.com/mmcdole/gofeed"
)

// A compact stand-in for a stock Webflow CMS blog index: an html tag with
// Webflow data attrs, a generator meta, and a .w-dyn-list of .w-dyn-item
// nodes each holding a relative anchor, a heading, and a <time datetime>.
const webflowBlogHTML = `<!DOCTYPE html>
<html data-wf-site="abc" data-wf-page="def">
<head><title>Acme Blog</title><meta name="generator" content="Webflow"></head>
<body>
<div class="w-dyn-list"><div role="list" class="w-dyn-items">
  <div role="listitem" class="w-dyn-item">
    <a href="/blog/hello-world" class="card">
      <h3>Hello World</h3>
      <time datetime="2026-05-01">May 1, 2026</time>
    </a>
  </div>
  <div role="listitem" class="w-dyn-item">
    <a href="/blog/second-post" class="card">
      <h3>Second Post</h3>
      <time datetime="2026-04-15">April 15, 2026</time>
    </a>
  </div>
  <div role="listitem" class="w-dyn-item">
    <a href="https://acme.test/blog/abs-link" class="card"><h3>Absolute Link</h3></a>
  </div>
</div></div>
</body></html>`

func TestWebflowToFeed(t *testing.T) {
	r, err := Build(Spec{Name: "webflow-to-feed"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Transform([]byte(webflowBlogHTML), FeedMeta{URL: "https://acme.test/blog", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}

	// The output must be a feed gofeed can parse.
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatalf("synthetic feed did not parse: %v\n---\n%s", err, out)
	}
	if f.Title != "Acme Blog" {
		t.Errorf("channel title = %q, want Acme Blog", f.Title)
	}
	if len(f.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(f.Items))
	}
	if f.Items[0].Title != "Hello World" {
		t.Errorf("item0 title = %q", f.Items[0].Title)
	}
	// relative link absolutised against the feed URL origin
	if f.Items[0].Link != "https://acme.test/blog/hello-world" {
		t.Errorf("item0 link = %q, want absolutised", f.Items[0].Link)
	}
	// datetime attr parsed into a real time
	if f.Items[0].PublishedParsed == nil {
		t.Errorf("item0 has no parsed pubDate")
	}
	// absolute href passes through unchanged
	if f.Items[2].Link != "https://acme.test/blog/abs-link" {
		t.Errorf("item2 link = %q, want absolute passthrough", f.Items[2].Link)
	}
}

func TestWebflowToFeed_NonWebflowPassthrough(t *testing.T) {
	r, _ := Build(Spec{Name: "webflow-to-feed"})
	// A plain HTML page with no Webflow markers must be returned verbatim.
	in := `<html><head><title>x</title></head><body><a href="/a">a</a></body></html>`
	out, err := r.Transform([]byte(in), FeedMeta{URL: "https://x.test", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Errorf("non-webflow page was modified:\n%s", out)
	}
}

func TestWebflowToFeed_RealFeedUntouchedByGate(t *testing.T) {
	r, _ := Build(Spec{Name: "webflow-to-feed"})
	// Gate defaults to text/html, so an application/xml feed is skipped by
	// the chain. Verify Applies returns false for a real feed response.
	if r.Applies(FeedMeta{ContentType: "application/rss+xml"}) {
		t.Error("webflow-to-feed should not apply to application/rss+xml")
	}
	if !r.Applies(FeedMeta{ContentType: "text/html; charset=utf-8"}) {
		t.Error("webflow-to-feed should apply to text/html")
	}
}

func TestWebflowToFeed_RequireWebflowFalse(t *testing.T) {
	// With require_webflow=false and a custom item selector, scrape a
	// non-Webflow listing.
	r, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{
		"require_webflow": "false",
		"item_selector":   "li.post",
		"title_selector":  "a",
	}})
	if err != nil {
		t.Fatal(err)
	}
	in := `<html><head><title>Plain</title></head><body><ul>
		<li class="post"><a href="/p/1">First</a></li>
		<li class="post"><a href="/p/2">Second</a></li>
	</ul></body></html>`
	out, err := r.Transform([]byte(in), FeedMeta{URL: "https://plain.test/", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Items) != 2 || f.Items[0].Title != "First" {
		t.Fatalf("unexpected items: %+v", f.Items)
	}
}

func TestWebflowToFeed_ExcludeLinkContains(t *testing.T) {
	r, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{
		"exclude_link_contains": "/category/, /tag/",
	}})
	if err != nil {
		t.Fatal(err)
	}
	in := `<html data-wf-site="x"><head><title>B</title></head><body>
		<div class="w-dyn-item"><a href="/blog/category/news"><h3>News</h3></a></div>
		<div class="w-dyn-item"><a href="/blog/tag/foo"><h3>Foo</h3></a></div>
		<div class="w-dyn-item"><a href="/blog/real-post"><h3>Real Post</h3></a></div>
	</body></html>`
	out, err := r.Transform([]byte(in), FeedMeta{URL: "https://b.test/blog", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := gofeed.NewParser().ParseString(string(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Items) != 1 || f.Items[0].Title != "Real Post" {
		t.Fatalf("exclusion failed, items: %+v", f.Items)
	}
}

func TestWebflowToFeed_BadLimit(t *testing.T) {
	if _, err := Build(Spec{Name: "webflow-to-feed", Params: map[string]string{"limit": "-3"}}); err == nil {
		t.Error("expected error for bad limit")
	}
}

func TestPubDate(t *testing.T) {
	cases := map[string]bool{ // input -> should-parse
		"2026-05-01":           true,
		"May 1, 2026":          true,
		"2026-05-01T10:00:00Z": true,
		"":                     false,
		"sometime soon":        false,
	}
	for in, want := range cases {
		got := pubDate(in) != ""
		if got != want {
			t.Errorf("pubDate(%q) parsed=%v, want %v", in, got, want)
		}
	}
}

func TestAbsolutise(t *testing.T) {
	if got := absolutise("https://x.test/blog", "/a/b"); got != "https://x.test/a/b" {
		t.Errorf("relative: got %q", got)
	}
	if got := absolutise("https://x.test/blog", "https://y.test/z"); got != "https://y.test/z" {
		t.Errorf("absolute passthrough: got %q", got)
	}
	if got := absolutise("", ""); got != "" {
		t.Errorf("empty: got %q", got)
	}
}

// sanity: the registry exposes the new primitive in the fixer vocabulary.
func TestWebflowRegistered(t *testing.T) {
	found := false
	for _, n := range Known() {
		if n == "webflow-to-feed" {
			found = true
		}
	}
	if !found {
		t.Errorf("webflow-to-feed not in Known(): %v", Known())
	}
}
