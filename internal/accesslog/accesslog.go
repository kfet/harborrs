// Package accesslog provides an HTTP middleware that emits one
// structured access-log line per request, redacting authentication
// material by construction.
//
// Disabled by default. cmd/harborrs enables it for a process via the
// environment variable HARBORRS_ACCESS_LOG=1; when disabled, New
// returns the underlying handler unchanged and adds zero per-request
// overhead. Output goes to whatever io.Writer the caller passes
// (typically os.Stderr, which systemd journal captures with its own
// timestamps).
//
// Redaction contract — the middleware is designed so secrets cannot
// leak into logs:
//
//   - Authorization and Cookie headers are never read by the
//     middleware, so they cannot appear in the log.
//   - Request and response bodies are never read (the middleware
//     wraps http.ResponseWriter only to count bytes and remember the
//     status code; it never inspects payloads).
//   - The URL query is summarised through an allow-list:
//   - safe keys ("s","n","r","xt","it","output","ac","ts") emit
//     useful values after embedded URL query strings are stripped
//     (stream ids may contain feed URLs with private ?token= params);
//   - count-only keys ("i","a","t") emit just <count:N>;
//   - presence-only keys ("c") emit just <present>;
//   - every other key — including "T", "Auth", "SID", "Email",
//     "Passwd", "password", "token", "lsid" — emits <redacted>.
//     The presence of an unknown key is still acknowledged so a
//     leaked-key audit can spot novel parameter names, but the
//     value never appears.
//   - The path, user-agent, and remote address fields are all
//     emitted through strconv.Quote so a client-controlled byte
//     (e.g. a percent-encoded newline in the request path) cannot
//     forge a second "access" line. harborrs route paths do not embed
//     credentials, but feed URLs embedded in stream/contents paths may
//     carry private query parameters, so embedded ?... suffixes are
//     stripped before quoting. Log-injection by an attacker who can
//     choose the path would undermine the integrity of the journal even
//     without secret leakage, so all three fields are quoted.
//
// One line per request:
//
//	access method=GET path="/reader/api/0/stream/items/ids" \
//	  status=200 bytes=4321 dur_ms=4 ua="Reeder/5.4" \
//	  remote="127.0.0.1:54321" \
//	  query="n=100 r=o s=user/-/state/com.google/reading-list xt=user/-/state/com.google/read c=<present>"
//
// Keys inside `query=` are sorted alphabetically for stable output.
package accesslog

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EnabledFromEnv returns true iff HARBORRS_ACCESS_LOG=1 in the
// process environment. Any other value (including empty) means
// disabled — matching the documented contract that access logging is
// off by default and must be opted into.
func EnabledFromEnv() bool {
	return os.Getenv("HARBORRS_ACCESS_LOG") == "1"
}

// New wraps h with access-log middleware. When enabled is false the
// underlying handler is returned as-is, so the disabled path has zero
// per-request overhead and no risk of accidental output.
//
// dst must be non-nil when enabled is true. A nil dst is treated as
// disabled — a defensive default that prevents a nil-deref panic on
// the first request if the caller forgets to wire a writer.
func New(h http.Handler, enabled bool, dst io.Writer) http.Handler {
	if !enabled || dst == nil {
		return h
	}
	return &middleware{
		next:   h,
		logger: log.New(dst, "", 0),
		now:    time.Now,
	}
}

type middleware struct {
	next   http.Handler
	logger *log.Logger
	now    func() time.Time
}

func (m *middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := m.now()
	cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
	m.next.ServeHTTP(cw, r)
	dur := m.now().Sub(start)
	// path / ua / remote are quoted because any of them can in
	// principle contain bytes (newlines, double quotes, NUL) that
	// would otherwise let a client forge a fake "access" line.
	// r.URL.Path in particular is reachable from the wire via
	// percent-encoded control chars (%0A, %0D) which Go's URL parser
	// happily decodes; without quoting, "GET /foo%0AINJECTED" splits
	// the log into two lines.
	m.logger.Printf("access method=%s path=%s status=%d bytes=%d dur_ms=%d ua=%s remote=%s%s",
		r.Method,
		strconv.Quote(stripEmbeddedQuery(r.URL.Path)),
		cw.status,
		cw.bytes,
		dur.Milliseconds(),
		strconv.Quote(r.UserAgent()),
		strconv.Quote(r.RemoteAddr),
		queryField(r.URL),
	)
}

// captureWriter wraps an http.ResponseWriter to remember the status
// code (defaults to 200, matching net/http's implicit-WriteHeader
// behaviour) and to count the bytes written. Bodies are never
// inspected.
type captureWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (c *captureWriter) WriteHeader(code int) {
	if c.wrote {
		// net/http itself logs a "superfluous WriteHeader" warning and
		// keeps the first status. Mirror that here so the log line
		// records what the client actually saw.
		return
	}
	c.wrote = true
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	// Implicit 200: Write before WriteHeader. Don't change c.status —
	// it already defaults to 200 — but mark wrote so a later
	// WriteHeader (which net/http would reject as superfluous) cannot
	// overwrite the captured status.
	c.wrote = true
	n, err := c.ResponseWriter.Write(p)
	c.bytes += int64(n)
	return n, err
}

// Query-parameter classification. Keys are matched case-sensitively
// because the Reader API uses specific cases (Email/Passwd vs.
// lowercase output/etc.) and we want a redact-by-default posture for
// any unrecognised key including unexpected case variations.

// safeQueryKeys: parameter values are useful for debugging
// Reader-API traffic. Values are logged after stripEmbeddedQuery so
// feed stream ids like feed/https://example/rss?token=secret do not
// leak private feed URL query parameters.
//
//   - s   — stream id (feed/<url>, user/-/state/com.google/...).
//   - n   — page-size hint.
//   - r   — sort order (o = oldest first).
//   - xt  — exclude state (filter).
//   - it  — include state (filter).
//   - ac  — subscription/edit action verb.
//   - ts  — mark-all-as-read cutoff timestamp.
//   - output — response format (json).
var safeQueryKeys = map[string]bool{
	"s": true, "n": true, "r": true, "xt": true, "it": true,
	"ac": true, "ts": true, "output": true,
}

// countQueryKeys: parameter may repeat with non-secret but
// potentially long / privacy-adjacent values (item ids, tag names);
// log just the count to avoid pumping per-request data into logs.
var countQueryKeys = map[string]bool{
	"i": true, "a": true, "t": true,
}

// presenceQueryKeys: parameter value is an opaque server-issued
// token whose value should not appear in logs even though it isn't a
// credential; log only presence.
var presenceQueryKeys = map[string]bool{
	"c": true,
}

// queryField returns " query=\"...\"" (with a leading space)
// summarising u's query parameters, or the empty string if there is
// no query. Keys are sorted for stable output.
func queryField(u *url.URL) string {
	q := u.Query()
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vals := q[k]
		switch {
		case safeQueryKeys[k]:
			parts = append(parts, k+"="+strings.Join(stripEmbeddedQueries(vals), ","))
		case countQueryKeys[k]:
			parts = append(parts, k+"=<count:"+strconv.Itoa(len(vals))+">")
		case presenceQueryKeys[k]:
			parts = append(parts, k+"=<present>")
		default:
			parts = append(parts, k+"=<redacted>")
		}
	}
	return " query=" + strconv.Quote(strings.Join(parts, " "))
}

// stripEmbeddedQueries applies stripEmbeddedQuery to every value in vals.
func stripEmbeddedQueries(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = stripEmbeddedQuery(v)
	}
	return out
}

// stripEmbeddedQuery removes the value-bearing part of an embedded URL query.
// Reader stream ids may include feed URLs, and private feeds commonly put
// credentials in those URLs (for example ?token=...). The outer request query
// is still summarised key-by-key by queryField; this helper protects URL-like
// values carried inside otherwise-safe fields or paths.
func stripEmbeddedQuery(v string) string {
	before, _, ok := strings.Cut(v, "?")
	if !ok {
		return v
	}
	return before + "?<redacted>"
}
