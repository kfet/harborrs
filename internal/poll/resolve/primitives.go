package resolve

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// gate is an optional Applies filter shared by transform primitives. An
// empty gate applies to every feed. ctContains, when set, restricts the
// resolver to responses whose Content-Type contains the substring (case-
// insensitive) — useful to scope a transform to "text/html" mislabelled
// feeds without touching clean XML.
type gate struct {
	ctContains string
}

func gateFrom(params map[string]string) gate {
	return gate{ctContains: strings.ToLower(strings.TrimSpace(params["content_type_contains"]))}
}

func (g gate) applies(m FeedMeta) bool {
	if g.ctContains == "" {
		return true
	}
	return strings.Contains(strings.ToLower(m.ContentType), g.ctContains)
}

// base provides no-op hook implementations so each primitive only writes
// the one hook it cares about.
type base struct {
	name string
	gate gate
}

func (b base) Name() string                                      { return b.name }
func (b base) Applies(m FeedMeta) bool                           { return b.gate.applies(m) }
func (b base) ShapeRequest(*http.Request) error                  { return nil }
func (b base) Transform(body []byte, _ FeedMeta) ([]byte, error) { return body, nil }

func init() {
	Register("strip-control-chars", newStripControlChars)
	Register("set-header", newSetHeader)
	Register("recode-charset", newRecodeCharset)
	Register("regex-replace", newRegexReplace)
}

// --- strip-control-chars -------------------------------------------------

// stripControlChars removes byte values illegal in XML 1.0 — the C0
// control characters other than tab (0x09), LF (0x0A) and CR (0x0D). This
// is the former poll.sanitizeXML: Go's encoding/xml aborts on the first
// such byte, so one stray U+0008 would otherwise drop every item in a
// feed. It works at the byte level — in every ASCII-superset encoding
// these bytes never appear inside a multi-byte sequence — and leaves
// UTF-16 documents (identified by BOM) untouched, where low bytes are
// legitimate payload.
type stripControlChars struct{ base }

func newStripControlChars(params map[string]string) (Resolver, error) {
	return stripControlChars{base{name: "strip-control-chars", gate: gateFrom(params)}}, nil
}

func (s stripControlChars) Transform(body []byte, _ FeedMeta) ([]byte, error) {
	if len(body) >= 2 && ((body[0] == 0xFE && body[1] == 0xFF) || (body[0] == 0xFF && body[1] == 0xFE)) {
		return body, nil
	}
	bad := -1
	for i := 0; i < len(body); i++ {
		if illegalXMLByte(body[i]) {
			bad = i
			break
		}
	}
	if bad < 0 {
		return body, nil
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:bad]...)
	for i := bad; i < len(body); i++ {
		if !illegalXMLByte(body[i]) {
			out = append(out, body[i])
		}
	}
	return out, nil
}

func illegalXMLByte(c byte) bool {
	return c < 0x20 && c != 0x09 && c != 0x0A && c != 0x0D
}

// --- set-header ----------------------------------------------------------

// setHeader sets a request header before the fetch. params: "key",
// "value". A feed behind a CDN that tarpits a disclosure-URL User-Agent,
// or one that needs a cookie or referer, is fixed by a set-header Spec
// rather than a branch in Poll. An empty value deletes the header.
type setHeader struct {
	base
	key, value string
}

func newSetHeader(params map[string]string) (Resolver, error) {
	k := strings.TrimSpace(params["key"])
	if k == "" {
		return nil, fmt.Errorf("set-header: missing \"key\" param")
	}
	return setHeader{base: base{name: "set-header"}, key: k, value: params["value"]}, nil
}

func (h setHeader) ShapeRequest(req *http.Request) error {
	if h.value == "" {
		req.Header.Del(h.key)
		return nil
	}
	req.Header.Set(h.key, h.value)
	return nil
}

// --- recode-charset ------------------------------------------------------

// recodeCharset transcodes a legacy single-byte encoding to UTF-8 so the
// XML parser stops choking on high bytes that the feed mislabels (or fails
// to label) as UTF-8. params: "from" — one of "latin1"/"iso-8859-1" or
// "windows-1252"/"cp1252". Stdlib-only: latin1 is a direct rune map;
// windows-1252 differs only in 0x80–0x9F, handled by a small table.
type recodeCharset struct {
	base
	table *[256]rune // nil for latin1 (identity rune map)
}

func newRecodeCharset(params map[string]string) (Resolver, error) {
	from := strings.ToLower(strings.TrimSpace(params["from"]))
	g := gateFrom(params)
	switch from {
	case "latin1", "iso-8859-1", "iso8859-1", "8859-1":
		return recodeCharset{base: base{name: "recode-charset", gate: g}}, nil
	case "windows-1252", "cp1252", "1252":
		return recodeCharset{base: base{name: "recode-charset", gate: g}, table: &cp1252}, nil
	default:
		return nil, fmt.Errorf("recode-charset: unsupported \"from\" %q (want latin1 or windows-1252)", from)
	}
}

func (c recodeCharset) Transform(body []byte, _ FeedMeta) ([]byte, error) {
	var sb strings.Builder
	sb.Grow(len(body) + len(body)/8)
	for _, b := range body {
		r := rune(b)
		if c.table != nil {
			r = c.table[b]
		}
		sb.WriteRune(r)
	}
	return []byte(sb.String()), nil
}

// cp1252 maps Windows-1252 bytes to runes. Identical to Latin-1 except in
// 0x80–0x9F, where CP1252 places printable characters that ISO-8859-1
// leaves as C1 controls.
var cp1252 = func() [256]rune {
	var t [256]rune
	for i := 0; i < 256; i++ {
		t[i] = rune(i)
	}
	hi := map[byte]rune{
		0x80: '\u20AC', 0x82: '\u201A', 0x83: '\u0192', 0x84: '\u201E',
		0x85: '\u2026', 0x86: '\u2020', 0x87: '\u2021', 0x88: '\u02C6',
		0x89: '\u2030', 0x8A: '\u0160', 0x8B: '\u2039', 0x8C: '\u0152',
		0x8E: '\u017D', 0x91: '\u2018', 0x92: '\u2019', 0x93: '\u201C',
		0x94: '\u201D', 0x95: '\u2022', 0x96: '\u2013', 0x97: '\u2014',
		0x98: '\u02DC', 0x99: '\u2122', 0x9A: '\u0161', 0x9B: '\u203A',
		0x9C: '\u0153', 0x9E: '\u017E', 0x9F: '\u0178',
	}
	for b, r := range hi {
		t[b] = r
	}
	return t
}()

// --- regex-replace -------------------------------------------------------

// regexReplace applies a Go regexp substitution to the body. params:
// "pattern" (RE2), "replace" (may use $1 / ${name}). The escape hatch for
// breakage no other primitive covers — a stray doctype, a busted
// namespace decl — while still being a declarative, revertable Spec rather
// than code.
type regexReplace struct {
	base
	re      *regexp.Regexp
	replace string
}

func newRegexReplace(params map[string]string) (Resolver, error) {
	pat := params["pattern"]
	if strings.TrimSpace(pat) == "" {
		return nil, fmt.Errorf("regex-replace: missing \"pattern\" param")
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("regex-replace: bad pattern: %w", err)
	}
	return regexReplace{base: base{name: "regex-replace", gate: gateFrom(params)}, re: re, replace: params["replace"]}, nil
}

func (rr regexReplace) Transform(body []byte, _ FeedMeta) ([]byte, error) {
	return rr.re.ReplaceAll(body, []byte(rr.replace)), nil
}
