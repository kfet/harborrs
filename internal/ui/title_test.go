package ui

import (
	"strings"
	"testing"

	"github.com/kfet/harb/internal/store"
)

func TestHTMLToText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"<p>Hello world</p>", "Hello world"},
		{"<p>one</p><p>two</p>", "one two"},
		{"a<br>b", "a b"},
		{"<p>Zed 1.0 is out! <a href=\"x\">link</a></p>", "Zed 1.0 is out! link"},
		{"<div>nested <span>text</span> here</div>", "nested text here"},
		{"line1\n\n   line2", "line1 line2"},
	}
	for _, c := range cases {
		if got := htmlToText(c.in); got != c.want {
			t.Errorf("htmlToText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncateTitle(t *testing.T) {
	if got := truncateTitle("short", 100); got != "short" {
		t.Errorf("short unchanged: got %q", got)
	}
	long := strings.Repeat("a", 60) + " " + strings.Repeat("b", 60)
	got := truncateTitle(long, 100)
	if []rune(got)[len([]rune(got))-1] != '\u2026' {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	if len([]rune(got)) > 101 {
		t.Errorf("too long: %d runes", len([]rune(got)))
	}
	// word-boundary break: "aaaa... bbbb" should cut at the space.
	if strings.Contains(got, "b") {
		t.Errorf("expected break before second word, got %q", got)
	}
	// multibyte safety: no panic, no broken glyph.
	mb := strings.Repeat("é", 200)
	_ = truncateTitle(mb, 100)
}

func TestEntryTitle(t *testing.T) {
	// Explicit title wins.
	if got := entryTitle(store.Entry{Title: "Real Title", Summary: "<p>body</p>"}); got != "Real Title" {
		t.Errorf("explicit title: got %q", got)
	}
	// Whitespace-only title falls through to body.
	if got := entryTitle(store.Entry{Title: "  ", Summary: "<p>Mastodon post body</p>"}); got != "Mastodon post body" {
		t.Errorf("empty title -> summary snippet: got %q", got)
	}
	// Content preferred over Summary when meaningful.
	if got := entryTitle(store.Entry{Content: "<p>From content</p>", Summary: "<p>From summary</p>"}); got != "From content" {
		t.Errorf("content preferred: got %q", got)
	}
	// Link-only content falls back to Summary.
	got := entryTitle(store.Entry{Content: "<a href=\"x\">link</a>", Summary: "<p>Real excerpt</p>"})
	if got != "Real excerpt" {
		t.Errorf("link-only content -> summary: got %q", got)
	}
	// No usable text anywhere -> (untitled).
	if got := entryTitle(store.Entry{}); got != "(untitled)" {
		t.Errorf("empty entry -> untitled: got %q", got)
	}
}
