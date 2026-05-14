package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeRelease builds a fake release tarball + matching checksums.txt
// for the current GOOS/GOARCH. Returns (assetName, tarBytes, checks).
func makeRelease(t *testing.T, version, body string) (string, []byte, []byte) {
	t.Helper()
	verNoV := strings.TrimPrefix(version, "v")
	baseName := fmt.Sprintf("harborrs-%s-%s-%s", verNoV, runtime.GOOS, runtime.GOARCH)
	asset := baseName + ".tar.gz"
	var buf strings.Builder
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	// A non-regular header (covers the typeflag skip branch).
	_ = tw.WriteHeader(&tar.Header{Name: baseName + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	// A regular file with a different name (covers the name skip branch).
	_ = tw.WriteHeader(&tar.Header{Name: baseName + "/LICENSE", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("L"))
	// The binary itself.
	_ = tw.WriteHeader(&tar.Header{Name: baseName + "/harborrs", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gw.Close()
	tarBytes := []byte(buf.String())
	sum := sha256.Sum256(tarBytes)
	checks := []byte(fmt.Sprintf("%s  %s\nffffffff  other.tar.gz\n", hex.EncodeToString(sum[:]), asset))
	return asset, tarBytes, checks
}

// rewriteTransport rewrites the upstream URLs that selfupdate.Run
// hard-codes (api.github.com + github.com) to point at a test server.
type rewriteTransport struct {
	apiHost string
	dlHost  string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Host {
	case "api.github.com":
		req.URL.Scheme = "http"
		req.URL.Host = rt.apiHost
	case "github.com":
		req.URL.Scheme = "http"
		req.URL.Host = rt.dlHost
		req.URL.Path = "/dl" + req.URL.Path
	}
	return http.DefaultTransport.RoundTrip(req)
}

// clientFor writes a stub "old" exe under t.TempDir() and returns the
// exe path plus an http.Client whose transport rewrites GitHub URLs to
// the given test server.
func clientFor(t *testing.T, srv *httptest.Server) (exe string, client *http.Client) {
	t.Helper()
	exe = filepath.Join(t.TempDir(), "harborrs")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(srv.Close)
	return exe, &http.Client{Transport: &rewriteTransport{apiHost: host, dlHost: host}}
}

// fixture builds a synthetic release for tag/body and returns a stub
// exe + an http client wired to a server serving it. Most happy-path
// and verify-error tests can stand on top of this.
func fixture(t *testing.T, tag, body string, overrideChecks []byte) (exe string, client *http.Client, asset string) {
	t.Helper()
	asset, tarBytes, checks := makeRelease(t, tag, body)
	if overrideChecks != nil {
		checks = overrideChecks
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			w.Write(tarBytes)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			w.Write(checks)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	exe, client = clientFor(t, srv)
	return exe, client, asset
}

// runWith is a tiny helper around selfupdate.Run with the common option
// shape pre-filled.
func runWith(currentVersion, version string, exe string, client *http.Client, checkOnly bool, out io.Writer) error {
	return Run(currentVersion, Options{
		Repo:       "x/y",
		Version:    version,
		HTTPClient: client,
		CheckOnly:  checkOnly,
		Stdout:     out,
		Stderr:     out,
		ExecPath:   func() (string, error) { return exe, nil },
	})
}

func TestRun_HappyPath(t *testing.T) {
	exe, client, _ := fixture(t, "v0.2.0", "new-binary", nil)
	var out strings.Builder
	if err := runWith("0.1.0", "", exe, client, false, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new-binary" {
		t.Fatalf("binary not replaced: %q", got)
	}
	if !strings.Contains(out.String(), "updated") {
		t.Fatalf("missing success line: %s", out.String())
	}
}

func TestRun_AlreadyUpToDate(t *testing.T) {
	exe, client, _ := fixture(t, "v0.1.0", "x", nil)
	var out strings.Builder
	if err := runWith("0.1.0", "", exe, client, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("expected up-to-date: %s", out.String())
	}
}

func TestRun_CheckOnly(t *testing.T) {
	exe, client, _ := fixture(t, "v9.9.9", "x", nil)
	var out strings.Builder
	if err := runWith("0.1.0", "", exe, client, true, &out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old" {
		t.Fatalf("CheckOnly should not modify binary: %q", got)
	}
	if !strings.Contains(out.String(), "update available") {
		t.Fatalf("missing 'update available': %s", out.String())
	}
}

func TestRun_ExplicitVersion(t *testing.T) {
	// Server claims "irrelevant" as latest, but we pin "0.3.0" (without
	// v-prefix) — exercises both the explicit-version path and ensureV.
	exe, client, _ := fixture(t, "v0.3.0", "v3", nil)
	if err := runWith("v0.1.0", "0.3.0", exe, client, false, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "v3" {
		t.Fatalf("binary not replaced: %q", got)
	}
}

func TestRun_ChecksumMismatch(t *testing.T) {
	asset := fmt.Sprintf("harborrs-0.2.0-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	bad := []byte(fmt.Sprintf("deadbeef  %s\n", asset))
	exe, client, _ := fixture(t, "v0.2.0", "x", bad)
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestRun_NoChecksumEntry(t *testing.T) {
	exe, client, _ := fixture(t, "v0.2.0", "x", []byte("ffff  unrelated.tar.gz\n"))
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no checksum entry") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_ManagedPathRefused(t *testing.T) {
	err := Run("0.1.0", Options{
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		ExecPath: func() (string, error) { return "/opt/homebrew/bin/harborrs", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "managed by a package manager") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_ExecPathError(t *testing.T) {
	err := Run("0.1.0", Options{
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		ExecPath: func() (string, error) { return "", fmt.Errorf("boom") },
	})
	if err == nil || !strings.Contains(err.Error(), "locate self") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_LatestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	exe, client := clientFor(t, srv)
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "resolve latest") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_DownloadAssetError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0"}`)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	exe, client := clientFor(t, httptest.NewServer(mux))
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "download asset") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_DownloadChecksumsError(t *testing.T) {
	asset, tarBytes, _ := makeRelease(t, "v0.2.0", "x")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0"}`)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+asset) {
			w.Write(tarBytes)
			return
		}
		http.NotFound(w, r)
	})
	exe, client := clientFor(t, httptest.NewServer(mux))
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "download checksums") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_EmptyTagName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":""}`)
	}))
	exe, client := clientFor(t, srv)
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "empty tag_name") {
		t.Fatalf("got %v", err)
	}
}

func TestRun_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	exe, client := clientFor(t, srv)
	err := runWith("0.1.0", "", exe, client, false, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRun_TempDirError(t *testing.T) {
	// We need a working server, but ExecPath pointing at a non-existent
	// dir so MkdirTemp under filepath.Dir fails. Can't use fixture/
	// clientFor here because they own the exe-dir wiring.
	asset, tarBytes, checks := makeRelease(t, "v0.2.0", "x")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0"}`)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			w.Write(tarBytes)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			w.Write(checks)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	client := &http.Client{Transport: &rewriteTransport{apiHost: host, dlHost: host}}
	err := Run("0.1.0", Options{
		HTTPClient: client,
		Stdout:     io.Discard,
		Stderr:     io.Discard,
		ExecPath:   func() (string, error) { return "/no/such/dir/harborrs", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "tempdir") {
		t.Fatalf("got %v", err)
	}
}

func TestExtractBinary_NotFound(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "x.tar.gz")
	f, _ := os.Create(tarPath)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "x/other", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	tw.Write([]byte("z"))
	tw.Close()
	gw.Close()
	f.Close()
	if err := extractBinary(tarPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinary_BadGzip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.tar.gz")
	os.WriteFile(p, []byte("not gzip"), 0o644)
	if err := extractBinary(p, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinary_OpenError(t *testing.T) {
	if err := extractBinary("/no/such/file", "/dev/null"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSha256File_Error(t *testing.T) {
	if _, err := sha256File("/no/such/file"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLookupChecksum_FileError(t *testing.T) {
	if _, err := lookupChecksum("/no/such/file", "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDownload_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if err := download(http.DefaultClient, srv.URL, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error")
	}
}

func TestDownload_GetError(t *testing.T) {
	if err := download(http.DefaultClient, "http://127.0.0.1:1/never", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error")
	}
}

func TestDownload_CreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	if err := download(http.DefaultClient, srv.URL, "/no/such/dir/out"); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveLatest_NewRequestError(t *testing.T) {
	if _, err := resolveLatest(http.DefaultClient, "bad\x7f/repo"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAssetArch(t *testing.T) {
	cases := map[string]string{
		"arm":   "armv6",
		"arm64": "arm64",
		"amd64": "amd64",
	}
	for in, want := range cases {
		if got := assetArch(in); got != want {
			t.Errorf("assetArch(%q) = %q, want %q", in, got, want)
		}
	}
}
