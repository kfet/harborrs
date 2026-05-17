package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestInstallRootRedirectsRelativeLocations pins the prefix-agnostic
// behaviour at the http top-level. Both root-level redirects (GET /
// and GET /ui) must emit verbatim relative Location headers so the
// browser resolves them against the effective request URI — under any
// path prefix, including a Tailscale Funnel mount that strips the
// prefix before forwarding.
func TestInstallRootRedirectsRelativeLocations(t *testing.T) {
	mux := http.NewServeMux()
	installRootRedirects(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	cases := []struct {
		name       string
		path       string
		wantCode   int
		wantLoc    string // exact relative Location
		wantResolv string // path the browser would land on (no prefix)
	}{
		{"root", "/", http.StatusSeeOther, "ui/", "/ui/"},
		{"ui-no-slash", "/ui", http.StatusMovedPermanently, "ui/", "/ui/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := cli.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("code=%d want %d", resp.StatusCode, tc.wantCode)
			}
			loc := resp.Header.Get("Location")
			if loc == "" {
				t.Fatal("empty Location")
			}
			if strings.HasPrefix(loc, "/") {
				t.Fatalf("absolute-path Location leaked: %q", loc)
			}
			if strings.Contains(loc, "://") {
				t.Fatalf("absolute-URL Location leaked: %q", loc)
			}
			if loc != tc.wantLoc {
				t.Fatalf("Location=%q want %q", loc, tc.wantLoc)
			}
			// Sanity-check RFC 3986 resolution against the request URI.
			base, _ := url.Parse(srv.URL + tc.path)
			ref, _ := url.Parse(loc)
			if got := base.ResolveReference(ref).Path; got != tc.wantResolv {
				t.Fatalf("resolved to %q, want %q", got, tc.wantResolv)
			}
		})
	}
}

// TestInstallRootRedirectsUnderPrefix repeats the same check with the
// mux mounted under /rss via http.StripPrefix, mirroring the Tailscale
// Funnel --set-path=/rss topology. The relative Location header must
// resolve back into the /rss/... namespace.
//
// We test /rss/ui (no trailing slash) — the case that was emitting an
// absolute /ui/ Location pre-fix. The /rss → /rss/ trailing-slash hop
// is handled by Funnel itself (or by the browser hitting /rss/ in the
// first place) and is covered by the live smoke test, not here.
func TestInstallRootRedirectsUnderPrefix(t *testing.T) {
	mux := http.NewServeMux()
	installRootRedirects(mux)
	const prefix = "/rss"
	root := http.NewServeMux()
	root.Handle(prefix+"/", http.StripPrefix(prefix, mux))
	srv := httptest.NewServer(root)
	defer srv.Close()

	cli := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	cases := []struct {
		name       string
		path       string
		wantResolv string
	}{
		{"prefix-ui-no-slash", "/rss/ui", "/rss/ui/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := cli.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode < 300 || resp.StatusCode >= 400 {
				t.Fatalf("not a redirect: code=%d", resp.StatusCode)
			}
			loc := resp.Header.Get("Location")
			if strings.HasPrefix(loc, "/") {
				t.Fatalf("absolute-path Location leaked: %q", loc)
			}
			base, _ := url.Parse(srv.URL + tc.path)
			ref, _ := url.Parse(loc)
			if got := base.ResolveReference(ref).Path; got != tc.wantResolv {
				t.Fatalf("resolved to %q, want %q", got, tc.wantResolv)
			}
		})
	}
}

// TestInstallRootRedirectsNotFound asserts the catch-all on / returns
// 404 for paths that aren't exactly "/" — important so the redirect
// doesn't shadow real handlers or swallow unknown URLs silently.
func TestInstallRootRedirectsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	installRootRedirects(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("code=%d want 404", resp.StatusCode)
	}
}
