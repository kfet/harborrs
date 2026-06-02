package accesslog

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// echoHandler is the standard downstream used by these tests: returns
// 200 + a tiny body so we can assert byte counts in the log line.
func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

// secretMaterial enumerates every literal secret we plant in test
// requests. After exercising a request, the test verifies none of
// these strings appear in the captured log output. Centralised so a
// future contributor can extend the redaction guarantee by adding a
// new literal here.
var secretMaterial = []string{
	"PROD_API_TOKEN_DEADBEEFCAFE",
	"PROD_SESSION_COOKIE_NEVER_LOG",
	"PROD_PASSWORD_PLEASEHIDE",
	"PROD_WRITE_TOKEN_T_VALUE",
	"PROD_EMAIL_NEVER_LOG",
	"PRIVATE_FEED_TOKEN_NEVER_LOG",
}

func assertNoSecrets(t *testing.T, s string) {
	t.Helper()
	for _, lit := range secretMaterial {
		if strings.Contains(s, lit) {
			t.Fatalf("redaction failure: log contains secret %q\nfull log: %s", lit, s)
		}
	}
}

// TestDisabledEmitsNothing: when enabled=false (or dst=nil) the
// returned handler must run the downstream chain but write nothing
// to the configured writer. This is the off-by-default contract.
func TestDisabledEmitsNothing(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		nilDst  bool
	}{
		{"explicit-disabled", false, false},
		{"defensive-nil-writer", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var dst io.Writer = &buf
			if tc.nilDst {
				dst = nil
			}
			downstreamHit := false
			base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downstreamHit = true
				_, _ = w.Write([]byte("ok"))
			})
			h := New(base, tc.enabled, dst)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
			if !downstreamHit {
				t.Fatal("downstream handler was not invoked")
			}
			if rr.Code != 200 || rr.Body.String() != "ok" {
				t.Fatalf("downstream broken: code=%d body=%q", rr.Code, rr.Body.String())
			}
			if buf.Len() != 0 {
				t.Fatalf("disabled wrote to buffer: %q", buf.String())
			}
		})
	}
}

// TestEnabledFromEnv: env-driven enablement is the production wiring.
// Off by default; on iff the value is exactly "1". Anything else is
// off — including unset, empty, "true", "yes", "01".
func TestEnabledFromEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false}, // covers both empty-set and unset (os.Getenv returns "")
		{"0", false},
		{"true", false},
		{"yes", false},
		{"on", false},
		{"01", false},
		{"1", true},
	}
	for _, tc := range cases {
		t.Run("HARB_ACCESS_LOG="+tc.val, func(t *testing.T) {
			t.Setenv("HARB_ACCESS_LOG", tc.val)
			if got := EnabledFromEnv(); got != tc.want {
				t.Fatalf("EnabledFromEnv()=%v want %v", got, tc.want)
			}
		})
	}
}

// TestLogsBasicFields: one line per request with the documented
// fields. A deterministic clock pins dur_ms so the assertion is
// reproducible.
func TestLogsBasicFields(t *testing.T) {
	var buf bytes.Buffer
	h := newDeterministic(echoHandler(), &buf)

	r := httptest.NewRequest("GET", "/ui/", nil)
	r.RemoteAddr = "192.0.2.7:65432"
	r.Header.Set("User-Agent", "Reeder/5.4")
	h.ServeHTTP(httptest.NewRecorder(), r)

	line := buf.String()
	for _, want := range []string{
		"access ",
		"method=GET",
		`path="/ui/"`,
		"status=200",
		"bytes=2",
		"dur_ms=13",
		`ua="Reeder/5.4"`,
		`remote="192.0.2.7:65432"`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q\nfull line: %s", want, line)
		}
	}
	// No accidental query=... when the request carried no params.
	if strings.Contains(line, "query=") {
		t.Fatalf("unexpected query= in log: %s", line)
	}
	// One line, ends in newline (log.Logger guarantee).
	if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		t.Fatalf("expected exactly one trailing newline; got %q", line)
	}
}

// newDeterministic returns New(h, true, dst) but pins the clock so
// the first now() returns t0 and the second returns t0+13ms.
func newDeterministic(h http.Handler, dst io.Writer) http.Handler {
	m := New(h, true, dst).(*middleware)
	calls := 0
	m.now = func() time.Time {
		calls++
		return time.Unix(1_700_000_000, 0).Add(time.Duration(calls-1) * 13 * time.Millisecond)
	}
	return m
}

// TestQueryRedaction exhaustively exercises the four classes of query
// keys: safe (value verbatim), count-only, presence-only, and the
// catch-all redacted bucket. Every sensitive synonym (T, Auth, SID,
// Email, Passwd, password, token, lsid) falls through the catch-all.
func TestQueryRedaction(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		mustHave []string
		mustMiss []string
	}{
		{
			name: "safe-values-verbatim",
			path: "/reader/api/0/stream/items/ids" +
				"?s=user/-/state/com.google/reading-list&n=100&r=o" +
				"&xt=user/-/state/com.google/read" +
				"&it=user/-/state/com.google/starred&output=json" +
				"&ac=subscribe&ts=1700000000000000",
			mustHave: []string{
				"n=100", "r=o",
				"s=user/-/state/com.google/reading-list",
				"xt=user/-/state/com.google/read",
				"it=user/-/state/com.google/starred",
				"output=json", "ac=subscribe", "ts=1700000000000000",
			},
		},
		{
			name: "count-only-i-a-t",
			path: "/reader/api/0/edit-tag" +
				"?i=tag:google.com,2005:reader/item/AAA" +
				"&i=tag:google.com,2005:reader/item/BBB" +
				"&a=user/-/state/com.google/read&t=NewTitle",
			mustHave: []string{"i=<count:2>", "a=<count:1>", "t=<count:1>"},
			mustMiss: []string{"tag:google.com", "NewTitle"},
		},
		{
			name:     "safe-stream-id-strips-private-feed-query",
			path:     "/reader/api/0/stream/items/ids?s=" + url.QueryEscape("feed/https://priv.example/rss?token=PRIVATE_FEED_TOKEN_NEVER_LOG&x=y"),
			mustHave: []string{"s=feed/https://priv.example/rss?<redacted>"},
			mustMiss: []string{"PRIVATE_FEED_TOKEN_NEVER_LOG", "token=", "x=y"},
		},
		{
			name:     "presence-only-c",
			path:     "/reader/api/0/stream/items/ids?s=feed/x&c=OPAQUEBASE64TOKENVALUE",
			mustHave: []string{"c=<present>"},
			mustMiss: []string{"OPAQUEBASE64TOKENVALUE"},
		},
		{
			name:     "T-write-token-redacted",
			path:     "/reader/api/0/edit-tag?T=PROD_API_TOKEN_DEADBEEFCAFE",
			mustHave: []string{"T=<redacted>"},
			mustMiss: []string{"PROD_API_TOKEN_DEADBEEFCAFE"},
		},
		{
			name: "auth-sid-email-passwd-password-all-redacted",
			path: "/accounts/ClientLogin" +
				"?Auth=PROD_API_TOKEN_DEADBEEFCAFE" +
				"&SID=PROD_API_TOKEN_DEADBEEFCAFE" +
				"&Email=PROD_EMAIL_NEVER_LOG" +
				"&Passwd=PROD_PASSWORD_PLEASEHIDE" +
				"&password=PROD_PASSWORD_PLEASEHIDE" +
				"&token=PROD_API_TOKEN_DEADBEEFCAFE" +
				"&lsid=PROD_API_TOKEN_DEADBEEFCAFE",
			mustHave: []string{
				"Auth=<redacted>", "SID=<redacted>", "Email=<redacted>",
				"Passwd=<redacted>", "password=<redacted>",
				"token=<redacted>", "lsid=<redacted>",
			},
			mustMiss: []string{
				"PROD_API_TOKEN_DEADBEEFCAFE",
				"PROD_EMAIL_NEVER_LOG",
				"PROD_PASSWORD_PLEASEHIDE",
			},
		},
		{
			name:     "unknown-key-default-redacted",
			path:     "/whatever?weird_key=anything-could-be-here",
			mustHave: []string{"weird_key=<redacted>"},
			mustMiss: []string{"anything-could-be-here"},
		},
		{
			name:     "no-query-no-query-field",
			path:     "/ui/",
			mustHave: []string{`path="/ui/"`},
			mustMiss: []string{"query="},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := New(echoHandler(), true, &buf)
			r := httptest.NewRequest("GET", tc.path, nil)
			h.ServeHTTP(httptest.NewRecorder(), r)
			line := buf.String()
			for _, want := range tc.mustHave {
				if !strings.Contains(line, want) {
					t.Errorf("missing %q\nfull line: %s", want, line)
				}
			}
			for _, bad := range tc.mustMiss {
				if strings.Contains(line, bad) {
					t.Errorf("unexpected %q in log\nfull line: %s", bad, line)
				}
			}
		})
	}
}

// TestQueryKeysSortedStable: query keys must be emitted in
// alphabetical order so log grep / shell pipelines see a stable shape
// across requests.
func TestQueryKeysSortedStable(t *testing.T) {
	var buf bytes.Buffer
	h := New(echoHandler(), true, &buf)
	// Insertion order intentionally messy.
	r := httptest.NewRequest("GET", "/x?s=A&n=1&xt=Y&it=Z&output=json&c=tok&r=o", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	line := buf.String()
	start := strings.Index(line, `query="`)
	if start < 0 {
		t.Fatalf("missing query=: %s", line)
	}
	rest := line[start+len(`query="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("unterminated query=: %s", line)
	}
	want := `c=<present> it=Z n=1 output=json r=o s=A xt=Y`
	if got := rest[:end]; got != want {
		t.Fatalf("query field=%q\nwant %q", got, want)
	}
}

// TestAuthorizationCookieAndBodyNeverLogged: a request that carries
// every kind of secret material we know about (Authorization header,
// Cookie header, form body with Email/Passwd, query with T=token)
// must produce a log line that contains none of those secret literals.
// The middleware must not even drain the request body — downstream
// must still be able to read it.
func TestAuthorizationCookieAndBodyNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	bodyRead := 0
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyRead = len(b)
		w.WriteHeader(204)
	}), true, &buf)

	body := strings.NewReader("Email=admin&Passwd=PROD_PASSWORD_PLEASEHIDE&T=PROD_WRITE_TOKEN_T_VALUE")
	r := httptest.NewRequest("POST", "/accounts/ClientLogin?T=PROD_API_TOKEN_DEADBEEFCAFE", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "GoogleLogin auth=PROD_API_TOKEN_DEADBEEFCAFE")
	r.Header.Set("Cookie", "harb_session=PROD_SESSION_COOKIE_NEVER_LOG")
	r.Header.Set("User-Agent", "Reeder/5.4")
	r.RemoteAddr = "192.0.2.1:1234"
	h.ServeHTTP(httptest.NewRecorder(), r)

	if bodyRead == 0 {
		t.Fatal("downstream got an empty body — middleware consumed it")
	}
	line := buf.String()
	assertNoSecrets(t, line)
	for _, want := range []string{
		"method=POST",
		`path="/accounts/ClientLogin"`,
		"status=204",
		`ua="Reeder/5.4"`,
		`remote="192.0.2.1:1234"`,
		"T=<redacted>",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in line: %s", want, line)
		}
	}
}

// TestResponseBodyNeverLogged: a downstream that writes a body
// containing secret material must not see it echoed in the log.
func TestResponseBodyNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pretend this is a ClientLogin success body: contains the
		// token verbatim, exactly what real responses look like.
		_, _ = io.WriteString(w, "SID=PROD_API_TOKEN_DEADBEEFCAFE\n")
		_, _ = io.WriteString(w, "LSID=PROD_API_TOKEN_DEADBEEFCAFE\n")
		_, _ = io.WriteString(w, "Auth=PROD_API_TOKEN_DEADBEEFCAFE\n")
	}), true, &buf)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/accounts/ClientLogin", nil))
	assertNoSecrets(t, buf.String())
	// Sanity: the test fixture itself does emit the secret in the
	// response — we just want the *log* to be clean.
	if !strings.Contains(rr.Body.String(), "PROD_API_TOKEN_DEADBEEFCAFE") {
		t.Fatal("test wiring broken: secret missing from response body fixture")
	}
}

// TestImplicit200: handler writes a body without WriteHeader →
// captureWriter reports status=200, bytes=4.
func TestImplicit200(t *testing.T) {
	var buf bytes.Buffer
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body"))
	}), true, &buf)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	line := buf.String()
	if !strings.Contains(line, "status=200") || !strings.Contains(line, "bytes=4") {
		t.Fatalf("expected status=200 bytes=4 in %s", line)
	}
}

// TestWriteHeaderOnlyNoBody: WriteHeader without Write → reported
// status and bytes=0.
func TestWriteHeaderOnlyNoBody(t *testing.T) {
	var buf bytes.Buffer
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
	}), true, &buf)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	line := buf.String()
	if !strings.Contains(line, "status=418") || !strings.Contains(line, "bytes=0") {
		t.Fatalf("expected status=418 bytes=0 in %s", line)
	}
}

// TestSuperfluousWriteHeaderKeepsFirst: a buggy handler that calls
// WriteHeader(500) then WriteHeader(200) must log status=500 (what
// the client actually saw — net/http drops the second).
func TestSuperfluousWriteHeaderKeepsFirst(t *testing.T) {
	var buf bytes.Buffer
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.WriteHeader(200) // superfluous; dropped.
	}), true, &buf)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(buf.String(), "status=500") {
		t.Fatalf("expected status=500 in %s", buf.String())
	}
}

// TestWriteThenWriteHeaderKeepsImplicit200: Write before WriteHeader
// → implicit 200. The later WriteHeader(500) is superfluous and
// dropped by net/http, so captureWriter must report 200 to match
// what the client saw.
func TestWriteThenWriteHeaderKeepsImplicit200(t *testing.T) {
	var buf bytes.Buffer
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hi"))
		w.WriteHeader(500) // superfluous; dropped.
	}), true, &buf)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(buf.String(), "status=200") {
		t.Fatalf("expected status=200 (implicit) in %s", buf.String())
	}
}

// TestQueryFieldEscaping: a query value containing characters that
// would otherwise break the field shape (literal double quote, space)
// must survive into the log via strconv.Quote escaping. Stream ids
// in the wild can contain unusual characters when feed URLs are odd.
func TestQueryFieldEscaping(t *testing.T) {
	var buf bytes.Buffer
	h := New(echoHandler(), true, &buf)
	u := &url.URL{Path: "/x", RawQuery: url.Values{"s": {`weird"value`}}.Encode()}
	r := httptest.NewRequest("GET", u.String(), nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	line := buf.String()
	if !strings.Contains(line, `weird\"value`) {
		t.Fatalf("escaped value missing: %s", line)
	}
}

// TestPathLogInjectionRefused pins the fix for the path-injection
// vector found during review. A client that sends GET /foo%0AFAKE
// would otherwise produce two log lines, the second of which looks
// like a forged "access" entry. strconv.Quote on r.URL.Path defangs
// it by emitting "/foo\nFAKE" as a single escaped string.
//
// Without the fix this test fails with a 2-line log output.
func TestPathLogInjectionRefused(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string // substring that must appear (escaped form)
	}{
		// %0A (LF): the classic log-forgery vector.
		{"percent-encoded-LF", "/foo%0AINJECTED",
			`path="/foo\nINJECTED"`},
		// %0D (CR): same shape; some log readers treat CR as a
		// line break.
		{"percent-encoded-CR", "/foo%0DINJECTED",
			`path="/foo\rINJECTED"`},
		// Embedded double quote: would otherwise close the quoted
		// field early.
		{"embedded-doublequote", `/foo%22INJECTED`,
			`path="/foo\"INJECTED"`},
		// NUL byte: undesirable in any text log.
		{"embedded-NUL", "/foo%00bar",
			`path="/foo\x00bar"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := New(echoHandler(), true, &buf)
			r := httptest.NewRequest("GET", tc.path, nil)
			h.ServeHTTP(httptest.NewRecorder(), r)
			out := buf.String()
			if got := strings.Count(out, "\n"); got != 1 {
				t.Fatalf("log injection: %d lines, want 1\nfull output: %q", got, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("expected escaped path %q in log\nfull output: %q", tc.want, out)
			}
		})
	}
}

// TestRemoteAddrInjectionRefused: in normal operation r.RemoteAddr is
// IP:port set by the HTTP server from the TCP connection, which a
// client cannot influence. But if harb ever grows reverse-proxy
// support that overrides RemoteAddr from X-Forwarded-For, an attacker
// could insert a newline. Defense in depth: quote the field.
func TestRemoteAddrInjectionRefused(t *testing.T) {
	var buf bytes.Buffer
	h := New(echoHandler(), true, &buf)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:80\nINJECTED secret=PROD"
	h.ServeHTTP(httptest.NewRecorder(), r)
	out := buf.String()
	if got := strings.Count(out, "\n"); got != 1 {
		t.Fatalf("remote-addr injection: %d lines, want 1\nfull output: %q", got, out)
	}
	if !strings.Contains(out, `remote="192.0.2.1:80\nINJECTED secret=PROD"`) {
		t.Fatalf("expected escaped RemoteAddr in log; full output: %q", out)
	}
}

// TestHandlerPanicEmitsNoLineButPropagates: if the downstream handler
// panics, the log line is never emitted (deferred Printf would need
// an explicit recover() — we don't add one because we don't want to
// swallow panics). Document this as the intentional contract so a
// future contributor doesn't try to "fix" it by adding a recover
// that might inadvertently surface secret material from the panic
// message into the log.
func TestHandlerPanicEmitsNoLineButPropagates(t *testing.T) {
	var buf bytes.Buffer
	h := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom-with-secret=PROD_PANIC")
	}), true, &buf)
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic to propagate, did not")
		}
		if buf.Len() != 0 {
			t.Fatalf("expected empty log before panic, got %q", buf.String())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/?T=PROD_TOKEN", nil))
}

func TestPathStripsEmbeddedPrivateFeedQuery(t *testing.T) {
	var buf bytes.Buffer
	h := New(echoHandler(), true, &buf)
	r := httptest.NewRequest("GET", "/reader/api/0/stream/contents/feed/https://priv.example/rss%3Ftoken=PRIVATE_FEED_TOKEN_NEVER_LOG%26x=y", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	line := buf.String()
	if !strings.Contains(line, `path="/reader/api/0/stream/contents/feed/https://priv.example/rss?<redacted>"`) {
		t.Fatalf("embedded feed query not redacted in path: %s", line)
	}
	for _, bad := range []string{"PRIVATE_FEED_TOKEN_NEVER_LOG", "token=", "x=y"} {
		if strings.Contains(line, bad) {
			t.Fatalf("unexpected %q in log: %s", bad, line)
		}
	}
}
