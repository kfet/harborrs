// Package e2e is an end-to-end smoke test for harborrs.
//
// It builds the harborrs binary, starts it against a temp data dir, spins
// up an httptest server returning a canned RSS feed, hits the Reader API
// (ClientLogin → subscription/list → stream/contents → edit-tag → unread-
// count) and the web UI, and asserts the expected end state.
//
// Run via `make e2e` from the repo root. Excluded from `make all`'s
// coverage gate via `.covignore`.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const sampleRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>E2E Feed</title>
    <description>e2e</description>
    <item>
      <title>First post</title>
      <link>https://example.com/first</link>
      <guid>first</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate>
      <description>First entry body</description>
    </item>
    <item>
      <title>Second post</title>
      <link>https://example.com/second</link>
      <guid>second</guid>
      <pubDate>Tue, 03 Jan 2006 15:04:05 GMT</pubDate>
      <description>Second entry body</description>
    </item>
  </channel>
</rss>`

// waitForListen polls until a TCP listen succeeds at addr or ctx is done.
func waitForListen(ctx context.Context, addr string) error {
	for {
		d := net.Dialer{Timeout: 200 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func mustGet(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestE2E(t *testing.T) {
	if os.Getenv("E2E") != "1" {
		t.Skip("set E2E=1 to run end-to-end smoke")
	}

	// 1. Build the harborrs binary.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "harborrs")
	build := exec.Command("go", "build", "-o", bin, "./cmd/harborrs")
	build.Dir = repoRoot(t)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	// 2. Canned RSS server.
	rssSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		io.WriteString(w, sampleRSS)
	}))
	defer rssSrv.Close()

	// 3. Write OPML pointing at it, plus a config with a password hash.
	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opmlPath := filepath.Join(tmp, "subs.opml")
	opml := fmt.Sprintf(`<opml version="2.0"><body>
<outline type="rss" text="E2E" title="E2E" xmlUrl="%s" category="News"/>
</body></opml>`, rssSrv.URL)
	if err := os.WriteFile(opmlPath, []byte(opml), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3a. Bootstrap config via the init subcommand.
	addr := freePort(t)
	initCmd := exec.Command(bin, "init",
		"-data", dataDir,
		"-username", "u",
		"-password", "secret",
		"-listen", addr,
		"-force",
	)
	initCmd.Stdout, initCmd.Stderr = os.Stdout, os.Stderr
	if err := initCmd.Run(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// 4. Import OPML.
	imp := exec.Command(bin, "import", "-data", dataDir, opmlPath)
	imp.Stdout, imp.Stderr = os.Stdout, os.Stderr
	if err := imp.Run(); err != nil {
		t.Fatalf("import: %v", err)
	}

	// 5. poll-once to fetch the feed.
	poll := exec.Command(bin, "poll-once", "-data", dataDir)
	poll.Stdout, poll.Stderr = os.Stdout, os.Stderr
	if err := poll.Run(); err != nil {
		t.Fatalf("poll-once: %v", err)
	}

	// 6. Start server. Run with HARBORRS_ACCESS_LOG=1 so the
	// access-log middleware (off by default) is exercised end-to-end
	// against the real binary; stderr is tee'd into a buffer so we
	// can assert at the end that secrets did not leak into the log.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := exec.CommandContext(ctx, bin, "serve", "-data", dataDir)
	srv.Env = append(os.Environ(), "HARBORRS_ACCESS_LOG=1")
	srv.Stdout = os.Stdout
	var stderrLog bytes.Buffer
	srv.Stderr = io.MultiWriter(os.Stderr, &stderrLog)
	if err := srv.Start(); err != nil {
		t.Fatalf("serve start: %v", err)
	}
	defer func() {
		srv.Process.Signal(os.Interrupt)
		srv.Wait()
	}()

	if err := waitForListen(ctx, addr); err != nil {
		t.Fatalf("listen wait: %v", err)
	}
	base := "http://" + addr
	client := &http.Client{Timeout: 10 * time.Second}

	// 7. ClientLogin.
	form := url.Values{"Email": {"u"}, "Passwd": {"secret"}}
	resp, err := client.PostForm(base+"/accounts/ClientLogin", form)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ClientLogin: %d %s", resp.StatusCode, body)
	}
	var tok string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "Auth=") {
			tok = strings.TrimPrefix(line, "Auth=")
		}
	}
	if tok == "" {
		t.Fatalf("no Auth in: %s", body)
	}
	authHdr := func(r *http.Request) { r.Header.Set("Authorization", "GoogleLogin auth="+tok) }

	// subscription/list
	req, _ := http.NewRequest("GET", base+"/reader/api/0/subscription/list", nil)
	authHdr(req)
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), rssSrv.URL) {
		t.Fatalf("sub/list: %d %s", resp.StatusCode, body)
	}

	// stream/contents
	req, _ = http.NewRequest("GET", base+"/reader/api/0/stream/contents/feed/"+rssSrv.URL, nil)
	authHdr(req)
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d %s", resp.StatusCode, body)
	}
	var sresp struct {
		Items []struct {
			ID         string   `json:"id"`
			LongID     string   `json:"longId"`
			Categories []string `json:"categories"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &sresp); err != nil || len(sresp.Items) != 2 {
		t.Fatalf("stream items: %v %s", err, body)
	}
	// Reeder/FreshRSS clients use item categories to associate stream
	// items with the reading-list and feed labels. Without these tags
	// unread items can be silently filtered out of the unread view.
	for _, it := range sresp.Items {
		hexID := strings.TrimPrefix(it.ID, "tag:google.com,2005:reader/item/")
		if len(hexID) != 16 {
			t.Fatalf("item id %q has hex length %d, want 16", it.ID, len(hexID))
		}
		if it.LongID == "" {
			t.Fatalf("item %q missing longId", it.ID)
		}
		if _, err := strconv.ParseInt(it.LongID, 10, 64); err != nil {
			t.Fatalf("item %q longId %q is not int64 decimal: %v", it.ID, it.LongID, err)
		}
		cats := strings.Join(it.Categories, ",")
		for _, want := range []string{
			"user/-/state/com.google/reading-list",
			"user/-/label/News",
		} {
			if !strings.Contains(cats, want) {
				t.Fatalf("item %q categories %v missing %q", it.ID, it.Categories, want)
			}
		}
		for _, cat := range it.Categories {
			if cat == "user/-/state/com.google/read" {
				t.Fatalf("unread item %q should not carry read state: %v", it.ID, it.Categories)
			}
		}
	}

	// stream/items/ids with xt=read on the reading-list: the typical
	// Reeder sync call. Refs must carry directStreamIds so the client
	// can map ids back to feeds, and the xt filter must actually
	// exclude read items (none are read yet, so we expect both back).
	idsURL := base + "/reader/api/0/stream/items/ids" +
		"?s=" + url.QueryEscape("user/-/state/com.google/reading-list") +
		"&xt=" + url.QueryEscape("user/-/state/com.google/read") +
		"&n=50"
	req, _ = http.NewRequest("GET", idsURL, nil)
	authHdr(req)
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("items/ids: %d %s", resp.StatusCode, body)
	}
	var idsResp struct {
		ItemRefs []struct {
			ID              string   `json:"id"`
			LongID          string   `json:"longId"`
			DirectStreamIDs []string `json:"directStreamIds"`
		} `json:"itemRefs"`
	}
	if err := json.Unmarshal(body, &idsResp); err != nil {
		t.Fatalf("items/ids unmarshal: %v %s", err, body)
	}
	if len(idsResp.ItemRefs) != 2 {
		t.Fatalf("items/ids: want 2 refs, got %d (body=%s)", len(idsResp.ItemRefs), body)
	}
	wantFeedStream := "feed/" + rssSrv.URL
	for _, ref := range idsResp.ItemRefs {
		if _, err := strconv.ParseInt(ref.ID, 10, 64); err != nil {
			t.Fatalf("ref id %q is not int64 decimal: %v", ref.ID, err)
		}
		if ref.LongID == "" {
			t.Fatalf("ref %q missing longId", ref.ID)
		}
		if _, err := strconv.ParseInt(ref.LongID, 10, 64); err != nil {
			t.Fatalf("ref %q longId %q is not int64 decimal: %v", ref.ID, ref.LongID, err)
		}
		if ref.ID != ref.LongID {
			t.Fatalf("ref id %q does not match longId %q", ref.ID, ref.LongID)
		}
		streams := strings.Join(ref.DirectStreamIDs, ",")
		for _, want := range []string{wantFeedStream, "user/-/label/News"} {
			if !strings.Contains(streams, want) {
				t.Fatalf("ref %q directStreamIds %v missing %q", ref.ID, ref.DirectStreamIDs, want)
			}
		}
	}

	// stream/items/contents must preserve the order of requested `i=`
	// values — Reeder uses the response order directly.
	wantOrder := []string{sresp.Items[1].ID, sresp.Items[0].ID}
	contentsForm := url.Values{"i": wantOrder}
	req, _ = http.NewRequest("POST", base+"/reader/api/0/stream/items/contents",
		strings.NewReader(contentsForm.Encode()))
	authHdr(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("items/contents: %d %s", resp.StatusCode, body)
	}
	var contentsResp struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &contentsResp); err != nil {
		t.Fatalf("items/contents unmarshal: %v %s", err, body)
	}
	if len(contentsResp.Items) != len(wantOrder) {
		t.Fatalf("items/contents: want %d items, got %d (body=%s)", len(wantOrder), len(contentsResp.Items), body)
	}
	for i, it := range contentsResp.Items {
		if it.ID != wantOrder[i] {
			t.Fatalf("items/contents[%d]=%q, want %q", i, it.ID, wantOrder[i])
		}
	}

	// edit-tag: mark both as read
	// Capture wall-clock right before the mark so we can assert the
	// state-stream delta-sync contract below: `s=read&ot=<before>`
	// must include both newly-read entries (their state UpdatedAt is
	// after `before`), and `s=read&ot=<after>` must exclude them
	// (no state has mutated since). This is the v0.4.10 regression
	// where ot/nt on state streams compared against entry fetch time
	// instead of EntryState.UpdatedAt — Reeder would re-stream every
	// recently-polled item every sync and clobber its own unread UI.
	//
	// Sleep so there is a wall-clock gap between the original
	// poll-once (FetchedAt) and tBeforeMark/UpdatedAt that the bug
	// vs fix produce strictly different answers against. Without the
	// gap, second-granularity ot/nt windows can overlap and let a
	// broken filter silently pass.
	time.Sleep(2 * time.Second)
	tBeforeMark := time.Now().Unix()
	form = url.Values{
		"i": []string{sresp.Items[0].ID, sresp.Items[1].ID},
		"a": []string{"user/-/state/com.google/read"},
	}
	req, _ = http.NewRequest("POST", base+"/reader/api/0/edit-tag", strings.NewReader(form.Encode()))
	authHdr(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("edit-tag: %d %s", resp.StatusCode, body)
	}

	// unread-count
	req, _ = http.NewRequest("GET", base+"/reader/api/0/unread-count", nil)
	authHdr(req)
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"count":0`) {
		t.Fatalf("unread-count expected 0: %s", body)
	}
	// Reeder iOS (and other FreshRSS-compatible clients) require the
	// newestItemTimestampUsec field on every unreadcounts row; without it
	// the client silently shows zero unread. Assert the field is present.
	if !strings.Contains(string(body), `"newestItemTimestampUsec"`) {
		t.Fatalf("unread-count missing newestItemTimestampUsec: %s", body)
	}
	var ucResp struct {
		UnreadCounts []struct {
			ID                      string `json:"id"`
			Count                   int    `json:"count"`
			NewestItemTimestampUsec string `json:"newestItemTimestampUsec"`
		} `json:"unreadcounts"`
	}
	if err := json.Unmarshal(body, &ucResp); err != nil {
		t.Fatalf("unread-count unmarshal: %v %s", err, body)
	}
	if len(ucResp.UnreadCounts) == 0 {
		t.Fatalf("unread-count empty: %s", body)
	}
	for _, row := range ucResp.UnreadCounts {
		if row.NewestItemTimestampUsec == "" {
			t.Fatalf("row %q has empty newestItemTimestampUsec: %s", row.ID, body)
		}
	}

	// stream/items/ids with xt=read after mark-all-read must return zero
	// refs — the Reeder "what's new since last sync" path.
	req, _ = http.NewRequest("GET", idsURL, nil)
	authHdr(req)
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("items/ids post-read: %d %s", resp.StatusCode, body)
	}
	var postRead struct {
		ItemRefs []struct{ ID string } `json:"itemRefs"`
	}
	if err := json.Unmarshal(body, &postRead); err != nil {
		t.Fatalf("items/ids post-read unmarshal: %v %s", err, body)
	}
	if len(postRead.ItemRefs) != 0 {
		t.Fatalf("items/ids post-read: want 0, got %d (body=%s)", len(postRead.ItemRefs), body)
	}

	// Read-state delta-sync contract (the v0.4.10 regression):
	//
	//   s=read&ot=<before-mark> -> include the entries whose state
	//                              mutated after `before` (i.e. both
	//                              entries we just marked).
	//   s=read&ot=<after-mark>  -> exclude all (no state mutated since).
	//
	// Pre-fix the implementation compared `ot` against entry
	// fetch/publish time, so the second query incorrectly returned
	// every recently-polled item and Reeder re-marked them all as
	// read on every sync. This e2e pin is the regression's last
	// line of defence: it must catch the bug even if the contract
	// suite in internal/reedercompat is bypassed.
	deltaURL := func(ot int64) string {
		return base + "/reader/api/0/stream/items/ids" +
			"?s=" + url.QueryEscape("user/-/state/com.google/read") +
			"&ot=" + strconv.FormatInt(ot, 10) + "&n=50"
	}
	doDelta := func(ot int64) []string {
		req, _ := http.NewRequest("GET", deltaURL(ot), nil)
		authHdr(req)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("delta ot=%d: %v", ot, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("delta ot=%d: %d %s", ot, resp.StatusCode, body)
		}
		var dr struct {
			ItemRefs []struct {
				ID string `json:"id"`
			} `json:"itemRefs"`
		}
		if err := json.Unmarshal(body, &dr); err != nil {
			t.Fatalf("delta unmarshal: %v %s", err, body)
		}
		ids := make([]string, len(dr.ItemRefs))
		for i, r := range dr.ItemRefs {
			ids[i] = r.ID
		}
		return ids
	}
	if got := doDelta(tBeforeMark - 1); len(got) != 2 {
		t.Fatalf("delta s=read&ot=<before mark>: want 2 refs, got %d (%v)", len(got), got)
	}
	farFuture := time.Now().Add(24 * time.Hour).Unix()
	if got := doDelta(farFuture); len(got) != 0 {
		t.Fatalf("delta s=read&ot=<far future>: want 0 refs, got %d (%v) — read-state delta is comparing against entry fetch time, not state UpdatedAt", len(got), got)
	}

	// 8. Web UI: login + home.
	jar, _ := cookiejar.New(nil)
	uic := &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = uic.PostForm(base+"/ui/login", url.Values{"username": {"u"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("ui login: %d", resp.StatusCode)
	}
	// The home view defaults to "show unread only"; before the poll
	// loop has fetched any entries, that would hide our seeded feed.
	// Use ?unread=0 to assert against the unfiltered list.
	resp = mustGet(t, uic, base+"/ui/?unread=0")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "E2E") {
		t.Fatalf("ui home: %d %s", resp.StatusCode, body)
	}

	// Feed page → entries visible.
	resp = mustGet(t, uic, base+"/ui/feed?id="+url.QueryEscape(rssSrv.URL))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "First post") {
		t.Fatalf("ui feed: %d %s", resp.StatusCode, body)
	}

	// Bundled stylesheet must actually have rules — a previous stub
	// regression shipped a 25-line CSS that left every page besides
	// /ui/login looking unstyled in the browser. This assertion
	// catches that without us needing to eyeball it.
	resp = mustGet(t, uic, base+"/ui/static/style.css")
	cssBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("css fetch: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("css content-type: %q", ct)
	}
	if len(cssBody) < 4000 {
		t.Fatalf("style.css suspiciously small (%d bytes) — looks like the stub regression",
			len(cssBody))
	}
	for _, sel := range []string{"ul.feeds", "ul.entries", "header .nav", ".add-feed"} {
		if !strings.Contains(string(cssBody), sel) {
			t.Fatalf("style.css missing rule for %q — UI will render unstyled", sel)
		}
	}

	// 9. Keyboard shortcuts: assert the JS/template contracts the
	//    handler in internal/ui/static/keys.js depends on. We can't
	//    run keydown events from net/http, but the two known
	//    regressions ("`u` from entry view doesn't go back" and
	//    "entry doesn't auto-mark as read") are both URL/selector
	//    contract failures, not logic bugs — so we verify the
	//    contracts directly.
	checkKbdContracts(t, uic, base, rssSrv.URL)

	// 10. Access-log redaction end-to-end. The server was started with
	//     HARBORRS_ACCESS_LOG=1, so stderr should now carry one access
	//     line per request. Shut down the server explicitly so the
	//     stderr pipe is fully drained before we assert.
	srv.Process.Signal(os.Interrupt)
	srv.Wait()
	logged := stderrLog.String()
	if !strings.Contains(logged, "harborrs access log enabled") {
		t.Fatalf("expected access-log enabled banner in stderr, got: %s", logged)
	}
	if !strings.Contains(logged, "access method=") {
		t.Fatalf("expected at least one access-log line in stderr, got: %s", logged)
	}
	// One of the Reader-API hits we made — pick a unique query
	// string we know was sent — must appear as a sanitised access
	// line. This is the contract Reeder debugging actually relies on.
	if !strings.Contains(logged, "xt=user/-/state/com.google/read") {
		t.Fatalf("expected sanitised xt= in access log, got: %s", logged)
	}
	// Redaction: every secret the test sent (the issued API token,
	// the password literal, the session cookie) must never appear
	// in the captured stderr. If any do, the redaction contract
	// failed in the running binary, not just the unit tests.
	mustMiss := []string{tok, "secret"}
	// Pull the session cookie value out of the jar — it's an opaque
	// per-run string, so we can only check by literal substring.
	if u, _ := url.Parse(base); u != nil {
		for _, c := range jar.Cookies(u) {
			if c.Name == "harborrs_session" && c.Value != "" {
				mustMiss = append(mustMiss, c.Value)
			}
		}
	}
	for _, bad := range mustMiss {
		if bad == "" {
			continue
		}
		if strings.Contains(logged, bad) {
			t.Fatalf("REDACTION FAILURE: stderr contains secret literal %q\n--- full stderr ---\n%s", bad, logged)
		}
	}
}

// checkKbdContracts verifies the assumptions internal/ui/static/keys.js
// makes about the rendered HTML and about its own URL construction.
// Anything failing here means a keyboard shortcut is silently broken
// for users.
func checkKbdContracts(t *testing.T, uic *http.Client, base, feedURL string) {
	t.Helper()

	// --- keys.js itself: no absolute /ui/... URLs ---------------------
	// keys.js must use the data-ui-base attribute so it works under a
	// path-prefix mount (Tailscale Funnel --set-path=/rss). Any
	// absolute "/ui/..." literal would 404 once mounted under a prefix.
	resp := mustGet(t, uic, base+"/ui/static/keys.js")
	jsBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("keys.js fetch: %d", resp.StatusCode)
	}
	js := string(jsBody)
	for _, bad := range []string{`"/ui/`, `'/ui/`, `"/ui"`, `'/ui'`} {
		if strings.Contains(js, bad) {
			t.Fatalf("keys.js contains absolute UI literal %q — breaks under path-prefix deploys", bad)
		}
	}
	if !strings.Contains(js, "data-ui-base") && !strings.Contains(js, "dataset.uiBase") {
		t.Fatalf("keys.js doesn't reference data-ui-base — it must read the server-emitted base for prefix-aware URLs")
	}

	// --- discover an entry hash from the feed page --------------------
	// The hash is the only id the UI uses for entries. Pull it out of
	// the rendered feed page by looking for the entry permalink.
	resp = mustGet(t, uic, base+"/ui/feed?id="+url.QueryEscape(feedURL))
	feedBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	hash := firstEntryHash(string(feedBody))
	if hash == "" {
		t.Fatalf("could not find entry hash on /ui/feed page: %s", feedBody)
	}

	// --- entry view: HTML structure keys.js depends on ----------------
	resp = mustGet(t, uic, base+"/ui/entry?id="+url.QueryEscape(hash))
	entryBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("entry view: %d %s", resp.StatusCode, entryBody)
	}
	entry := string(entryBody)

	// (a) data-ui-base must be emitted so keys.js can read it.
	if !strings.Contains(entry, `data-ui-base="`) {
		t.Fatalf("entry HTML missing data-ui-base attribute (template regression): %s", entry)
	}
	// (b) The auto-mark-read code in keys.js extracts the hash from
	//     the article's id="entry-detail-<hash>". If we ever rename
	//     this, auto-mark stops working silently.
	wantID := `id="entry-detail-` + hash + `"`
	if !strings.Contains(entry, wantID) {
		t.Fatalf("entry HTML missing %q (auto-mark-read won't find the hash): %s", wantID, entry)
	}
	if !strings.Contains(entry, `class="entry-full`) {
		t.Fatalf(`entry HTML missing class="entry-full" (keys.js gates entry-view bindings on it)`)
	}
	// (c) The entry-view `u` handler looks for a .meta link whose href
	//     contains "feed?id=". If the template stops rendering the
	//     parent-feed link in the meta line, `u` breaks.
	metaStart := strings.Index(entry, `class="meta"`)
	if metaStart < 0 {
		t.Fatalf(`entry HTML missing .meta block`)
	}
	metaEnd := strings.Index(entry[metaStart:], "</p>")
	if metaEnd < 0 {
		t.Fatalf("entry HTML .meta block not closed")
	}
	metaHTML := entry[metaStart : metaStart+metaEnd]
	if !strings.Contains(metaHTML, `feed?id=`) {
		t.Fatalf(".meta block has no feed?id= link — `u` from entry view will not navigate back: %s", metaHTML)
	}

	// --- auto-mark-read endpoint behaviour ----------------------------
	// Simulate what the dwell-timer in keys.js does: POST to
	// entry/read?id=<hash>&state=1, then re-fetch the entry and
	// assert it now has the `read` class. This catches the case
	// where the endpoint regresses or where keys.js's URL is
	// pointed at a path that doesn't exist.
	//
	// The Reader-API edit-tag step above already marked everything
	// read, so we first POST state=0 to flip the entry back to
	// unread — otherwise the state=1 check would pass vacuously.
	postState := func(state string) {
		t.Helper()
		req, _ := http.NewRequest("POST",
			base+"/ui/entry/read?id="+url.QueryEscape(hash)+"&state="+state, nil)
		resp, err := uic.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("entry/read POST state=%s: %d", state, resp.StatusCode)
		}
	}
	fetchEntry := func() string {
		t.Helper()
		resp := mustGet(t, uic, base+"/ui/entry?id="+url.QueryEscape(hash))
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	postState("0")
	if body := fetchEntry(); strings.Contains(body, `class="entry-full read`) {
		t.Fatalf("entry still marked read after POST state=0: %s", body)
	}
	postState("1")
	if body := fetchEntry(); !strings.Contains(body, `class="entry-full read`) {
		t.Fatalf("entry not marked read after POST state=1: %s", body)
	}

	// --- prefix-mount correctness -------------------------------------
	// Verify every internal href/src/action in the rendered UI is
	// relative (no leading slash). An absolute /ui/... reference
	// would silently 404 under a path-prefix funnel deploy.
	for _, page := range []string{
		"/ui/",
		"/ui/feed?id=" + url.QueryEscape(feedURL),
		"/ui/entry?id=" + url.QueryEscape(hash),
	} {
		resp := mustGet(t, uic, base+page)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Authenticated UI pages must be uncacheable, otherwise back-
		// navigation (browser Back / our `u` shortcut) shows a stale
		// snapshot of the list with toggled entries still in their old
		// state until the user hits F5. See internal/ui/ui.go render().
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Fatalf("page %s missing Cache-Control: no-store (got %q) — back-nav will show stale state", page, cc)
		}
		for _, bad := range []string{`href="/ui/`, `src="/ui/`, `action="/ui/`,
			`hx-get="/ui/`, `hx-post="/ui/`, `hx-put="/ui/`, `hx-delete="/ui/`} {
			if strings.Contains(string(body), bad) {
				t.Fatalf("page %s contains absolute UI ref %q — breaks under path-prefix deploys", page, bad)
			}
		}
	}
}

// firstEntryHash extracts the first entry hash from a rendered
// /ui/feed page. Looks for an href like `entry?id=<hash>` (or
// `/ui/entry?id=<hash>` if templates ever go absolute again).
func firstEntryHash(html string) string {
	for _, prefix := range []string{`href="entry?id=`, `href="/ui/entry?id=`} {
		i := strings.Index(html, prefix)
		if i < 0 {
			continue
		}
		rest := html[i+len(prefix):]
		j := strings.IndexAny(rest, `"&`)
		if j <= 0 {
			continue
		}
		return rest[:j]
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// e2e package lives in <repo>/e2e
	return filepath.Dir(wd)
}
