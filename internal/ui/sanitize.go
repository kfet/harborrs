package ui

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// sanitizeHTML cleans untrusted feed HTML for safe rendering in the web
// UI. Feed content is attacker-controlled (anyone can publish a feed a
// user subscribes to), so rendering it verbatim is a stored-XSS vector.
//
// Strategy — parse into a node tree with golang.org/x/net/html, then
// rebuild it through a strict allow-list and re-serialize. Re-parsing +
// re-serialization is what neutralises mutation-XSS (misnested / exotic
// markup that a naive regex stripper would mangle into something the
// browser re-interprets as script). The rules:
//
//   - Only allow-listed elements survive. Known-dangerous elements
//     (script, style, iframe, object, embed, svg, …) are dropped
//     together with their subtree. Any other unrecognised element is
//     "unwrapped": the tag is discarded but its safe children are kept.
//   - Only allow-listed attributes survive on each element. Every
//     `on*` event handler, `style`, and presentational/scripting hook
//     is stripped.
//   - URL-bearing attributes (href, src, …) are scheme-checked: only
//     http, https, mailto and scheme-relative / relative references
//     are kept. `javascript:`, `data:`, `vbscript:` and friends — even
//     obfuscated with embedded whitespace/control chars or mixed case
//     — are dropped.
//   - Comments are dropped (they can smuggle conditional-comment script
//     in legacy engines and add no display value).
//
// We use golang.org/x/net/html rather than a third-party sanitizer
// (e.g. bluemonday): x/net is already a transitive dependency via
// gofeed, so this adds no new module while keeping the project's
// stdlib-mostly constraint intact.
func sanitizeHTML(s string) string {
	if s == "" {
		return ""
	}
	// Parse as a fragment in a <body> context so bare inline/block
	// markup parses the way a browser would inside the document body.
	// html.ParseFragment with a valid context node does not error on
	// real input (the tokenizer is total); on the off chance it returns
	// nothing we simply emit an empty string — fail closed.
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, _ := html.ParseFragment(strings.NewReader(s), ctx)
	var b strings.Builder
	for _, n := range nodes {
		for _, c := range cleanNode(n) {
			_ = html.Render(&b, c)
		}
	}
	return b.String()
}

// cleanNode returns the sanitized replacement for n: zero nodes (the
// node and its subtree are dropped), exactly one node (an allow-listed
// element or text node), or several nodes (an unwrapped unknown element
// replaced by its cleaned children).
func cleanNode(n *html.Node) []*html.Node {
	switch n.Type {
	case html.TextNode:
		return []*html.Node{{Type: html.TextNode, Data: n.Data}}
	case html.ElementNode:
		// fall through below
	default:
		// Comments, doctypes, etc. — drop entirely.
		return nil
	}

	tag := strings.ToLower(n.Data)
	if droppedSubtree[tag] {
		return nil
	}

	kids := cleanChildren(n)

	allowed, ok := allowedAttrs[tag]
	if !ok {
		// Unknown but not dangerous: unwrap — keep the safe children,
		// discard the tag itself.
		return kids
	}

	el := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: n.DataAtom}
	for _, a := range n.Attr {
		name := strings.ToLower(a.Key)
		// Namespaced attributes (xlink:href, xml:*) are never needed by
		// our allow-listed elements and are a known SVG/XML XSS surface.
		if a.Namespace != "" || strings.ContainsAny(name, ":") {
			continue
		}
		if !allowed[name] {
			continue
		}
		if urlAttrs[name] {
			if !safeURL(a.Val) {
				continue
			}
		}
		el.Attr = append(el.Attr, html.Attribute{Key: name, Val: a.Val})
	}

	if tag == "a" {
		el.Attr = openLinkAttrs(el.Attr)
	}

	for _, c := range kids {
		el.AppendChild(c)
	}
	return []*html.Node{el}
}

// cleanChildren returns the flattened, cleaned children of n as a fresh
// slice of detached nodes (no parent links), ready to be re-attached.
func cleanChildren(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, cleanNode(c)...)
	}
	return out
}

// openLinkAttrs reproduces openLinksInNewTab's behaviour at the node
// level: every <a> opens in a new tab with rel="noopener noreferrer",
// unless the author already specified a target.
func openLinkAttrs(attrs []html.Attribute) []html.Attribute {
	for _, a := range attrs {
		if a.Key == "target" {
			return attrs // author intent wins
		}
	}
	return append(attrs,
		html.Attribute{Key: "target", Val: "_blank"},
		html.Attribute{Key: "rel", Val: "noopener noreferrer"},
	)
}

// safeURL reports whether a URL-valued attribute is safe to keep. It
// permits relative references, scheme-relative ("//host/…") and the
// http/https/mailto schemes; everything else (javascript:, data:,
// vbscript:, file:, …) is rejected. The scheme test is robust against
// the classic obfuscations — leading/embedded whitespace and control
// characters ("java\tscript:") and mixed case — because browsers strip
// those before resolving the scheme, so we must too.
func safeURL(v string) bool {
	// Strip all ASCII whitespace and C0 control chars anywhere up to
	// the first ':' the same way a browser's URL parser tolerates them.
	var stripped strings.Builder
	for _, r := range v {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		stripped.WriteRune(r)
	}
	cleaned := stripped.String()
	colon := strings.IndexByte(cleaned, ':')
	if colon < 0 {
		return true // no scheme: relative reference
	}
	// A '/', '?' or '#' before the colon means the colon is part of a
	// path/query/fragment, not a scheme (e.g. "foo/bar:baz").
	if i := strings.IndexAny(cleaned, "/?#"); i >= 0 && i < colon {
		return true
	}
	scheme := strings.ToLower(cleaned[:colon])
	switch scheme {
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}

// droppedSubtree are elements whose tag AND contents are removed — they
// are either active content (script, scripting hooks) or carry their
// own scripting/embedding surface that an allow-list can't tame.
var droppedSubtree = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true,
	"embed": true, "applet": true, "noscript": true, "link": true,
	"meta": true, "base": true, "form": true, "input": true,
	"button": true, "textarea": true, "select": true, "option": true,
	"svg": true, "math": true, "frame": true, "frameset": true,
	"title": true, "head": true, "template": true, "canvas": true,
	"audio": true, "video": true, "track": true, "portal": true,
}

// urlAttrs are attributes whose value is a URL and must be scheme-checked.
var urlAttrs = map[string]bool{
	"href": true, "src": true, "cite": true, "longdesc": true,
	"poster": true, "srcset": true,
}

// allowedAttrs maps each allow-listed element to its permitted
// attribute set. An element not present here is unwrapped (tag dropped,
// children kept) unless it is in droppedSubtree.
var allowedAttrs = map[string]map[string]bool{
	"a":          {"href": true, "title": true, "name": true, "target": true, "rel": true},
	"abbr":       {"title": true},
	"address":    {},
	"b":          {},
	"blockquote": {"cite": true},
	"br":         {},
	"caption":    {},
	"cite":       {},
	"code":       {},
	"col":        {"span": true},
	"colgroup":   {"span": true},
	"dd":         {},
	"del":        {"cite": true},
	"details":    {},
	"dfn":        {"title": true},
	"div":        {},
	"dl":         {},
	"dt":         {},
	"em":         {},
	"figcaption": {},
	"figure":     {},
	"h1":         {}, "h2": {}, "h3": {}, "h4": {}, "h5": {}, "h6": {},
	"hr":      {},
	"i":       {},
	"img":     {"src": true, "alt": true, "title": true, "width": true, "height": true},
	"ins":     {"cite": true},
	"kbd":     {},
	"li":      {"value": true},
	"mark":    {},
	"ol":      {"start": true, "reversed": true, "type": true},
	"p":       {},
	"pre":     {},
	"q":       {"cite": true},
	"s":       {},
	"samp":    {},
	"small":   {},
	"span":    {},
	"strike":  {},
	"strong":  {},
	"sub":     {},
	"summary": {},
	"sup":     {},
	"table":   {},
	"tbody":   {},
	"td":      {"colspan": true, "rowspan": true},
	"tfoot":   {},
	"th":      {"colspan": true, "rowspan": true, "scope": true},
	"thead":   {},
	"time":    {"datetime": true},
	"tr":      {},
	"u":       {},
	"ul":      {},
	"var":     {},
	"wbr":     {},
}
