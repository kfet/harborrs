package ui

import (
	"strings"
	"testing"

	"github.com/kfet/harb/internal/store"
)

// TestSanitizeHTMLHostile feeds the sanitizer a battery of XSS payloads
// and asserts that the dangerous bits are gone while benign markup and
// the open-in-new-tab rewrite survive.
func TestSanitizeHTMLHostile(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string // substrings that must NOT appear (case-insensitive)
		present []string // substrings that MUST appear
	}{
		{
			name:   "script tag dropped with contents",
			in:     `<p>hi</p><script>alert(1)</script>`,
			absent: []string{"<script", "alert(1)"},
			// "hi" must remain.
			present: []string{"hi"},
		},
		{
			name:   "img onerror handler stripped",
			in:     `<img src="x" onerror="alert(1)">`,
			absent: []string{"onerror", "alert"},
		},
		{
			name:   "javascript href neutralised",
			in:     `<a href="javascript:alert(1)">x</a>`,
			absent: []string{"javascript:"},
			// link text kept, anchor may remain without href.
			present: []string{"x"},
		},
		{
			name:   "obfuscated javascript scheme with control chars",
			in:     "<a href=\"java\tscript:alert(1)\">x</a>",
			absent: []string{"javascript", "alert"},
		},
		{
			name:   "data uri href dropped",
			in:     `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
			absent: []string{"data:text/html"},
		},
		{
			name:   "vbscript href dropped",
			in:     `<a href="vbscript:msgbox(1)">x</a>`,
			absent: []string{"vbscript:"},
		},
		{
			name:    "inline style stripped",
			in:      `<p style="background:url(javascript:alert(1))">x</p>`,
			absent:  []string{"style", "javascript"},
			present: []string{"x"},
		},
		{
			name:    "iframe dropped with contents",
			in:      `<iframe src="https://evil.example"></iframe>after`,
			absent:  []string{"<iframe", "evil.example"},
			present: []string{"after"},
		},
		{
			name:    "svg payload dropped",
			in:      `<svg><script>alert(1)</script></svg>ok`,
			absent:  []string{"<svg", "<script", "alert"},
			present: []string{"ok"},
		},
		{
			name:    "object/embed dropped",
			in:      `<object data="x"></object><embed src="y">tail`,
			absent:  []string{"<object", "<embed"},
			present: []string{"tail"},
		},
		{
			name:    "form input button dropped",
			in:      `<form><input name="a"><button>go</button></form>body`,
			absent:  []string{"<form", "<input", "<button"},
			present: []string{"body"},
		},
		{
			name:    "comment dropped",
			in:      `<!-- [if IE]><script>x</script><![endif] -->visible`,
			absent:  []string{"<!--", "<script"},
			present: []string{"visible"},
		},
		{
			name:   "xlink namespaced attr dropped",
			in:     `<a xlink:href="javascript:alert(1)">x</a>`,
			absent: []string{"xlink", "javascript"},
		},
		{
			name:   "mutation xss via misnested tags",
			in:     `<noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
			absent: []string{"onerror", "<noscript"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeHTML(c.in)
			low := strings.ToLower(got)
			for _, a := range c.absent {
				if strings.Contains(low, strings.ToLower(a)) {
					t.Errorf("sanitized output must not contain %q\ngot: %s", a, got)
				}
			}
			for _, p := range c.present {
				if !strings.Contains(low, strings.ToLower(p)) {
					t.Errorf("sanitized output must contain %q\ngot: %s", p, got)
				}
			}
		})
	}
}

// TestSanitizeHTMLPreservesSafeMarkup checks that benign formatting and
// safe links/images survive intact.
func TestSanitizeHTMLPreservesSafeMarkup(t *testing.T) {
	in := `<p>Hello <strong>world</strong> <a href="https://example.com/post">link</a></p>` +
		`<ul><li>one</li><li>two</li></ul>` +
		`<img src="https://example.com/a.png" alt="pic">` +
		`<blockquote cite="https://example.com">quote</blockquote>`
	got := sanitizeHTML(in)
	for _, want := range []string{
		"<strong>world</strong>",
		`href="https://example.com/post"`,
		"<li>one</li>",
		`src="https://example.com/a.png"`,
		`alt="pic"`,
		"<blockquote",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output\ngot: %s", want, got)
		}
	}
}

// TestSanitizeHTMLOpenLinks verifies the open-in-new-tab rewrite is
// applied to safe links and that an author-specified target wins.
func TestSanitizeHTMLOpenLinks(t *testing.T) {
	got := sanitizeHTML(`<a href="https://example.com">x</a>`)
	if !strings.Contains(got, `target="_blank"`) || !strings.Contains(got, `rel="noopener noreferrer"`) {
		t.Fatalf("expected target/rel injection, got: %s", got)
	}
	got2 := sanitizeHTML(`<a href="https://example.com" target="_self">x</a>`)
	if strings.Contains(got2, "_blank") {
		t.Fatalf("author target should win, got: %s", got2)
	}
	if !strings.Contains(got2, `target="_self"`) {
		t.Fatalf("author target dropped, got: %s", got2)
	}
}

// TestSanitizeHTMLEmpty covers the empty-input fast path.
func TestSanitizeHTMLEmpty(t *testing.T) {
	if got := sanitizeHTML(""); got != "" {
		t.Fatalf("empty input must yield empty output, got %q", got)
	}
}

// TestSanitizeHTMLUnknownTagUnwrapped checks that an unrecognised but
// non-dangerous element is unwrapped: the tag is dropped, its safe text
// children survive.
func TestSanitizeHTMLUnknownTagUnwrapped(t *testing.T) {
	got := sanitizeHTML(`<center><marquee>hi <b>there</b></marquee></center>`)
	if strings.Contains(got, "marquee") || strings.Contains(got, "center") {
		t.Fatalf("unknown tags should be unwrapped, got: %s", got)
	}
	if !strings.Contains(got, "hi") || !strings.Contains(got, "<b>there</b>") {
		t.Fatalf("children of unwrapped tag should survive, got: %s", got)
	}
}

// TestSanitizeHTMLRelativeURL keeps relative and scheme-relative links.
func TestSanitizeHTMLRelativeURL(t *testing.T) {
	for _, in := range []string{
		`<a href="/path/to/post">x</a>`,
		`<a href="post.html">x</a>`,
		`<a href="//cdn.example/p">x</a>`,
		`<a href="foo/bar:baz">x</a>`,
		`<a href="mailto:a@b.com">x</a>`,
	} {
		got := sanitizeHTML(in)
		if !strings.Contains(got, "href=") {
			t.Errorf("relative/safe href should be kept for %q, got: %s", in, got)
		}
	}
}

// TestEntryBodySanitizes wires the sanitizer through entryBody and
// confirms a hostile feed body cannot inject script.
func TestEntryBodySanitizes(t *testing.T) {
	e := store.Entry{Content: `<p>ok</p><script>alert(1)</script>`}
	got := string(entryBody(e))
	if strings.Contains(got, "<script") {
		t.Fatalf("script leaked through entryBody: %s", got)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("benign content dropped: %s", got)
	}
	// Summary fallback path is also sanitized.
	e2 := store.Entry{Summary: `<img src=x onerror=alert(1)>`}
	if strings.Contains(string(entryBody(e2)), "onerror") {
		t.Fatalf("onerror leaked via summary fallback")
	}
}
