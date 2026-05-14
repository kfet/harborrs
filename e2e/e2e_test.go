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
<outline type="rss" text="E2E" title="E2E" xmlUrl="%s"/>
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

	// 6. Start server.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := exec.CommandContext(ctx, bin, "serve", "-data", dataDir)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
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
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &sresp); err != nil || len(sresp.Items) != 2 {
		t.Fatalf("stream items: %v %s", err, body)
	}

	// edit-tag: mark both as read
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
	resp = mustGet(t, uic, base+"/ui/")
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
