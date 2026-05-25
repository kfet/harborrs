package reader

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGzipMiddlewareCompress verifies a gzip-capable client receives a
// gzipped body with Content-Encoding: gzip / Vary: Accept-Encoding /
// no Content-Length, and that the body round-trips through gzip.
func TestGzipMiddlewareCompress(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set a Content-Length the middleware should strip.
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(200)
		// Highly compressible payload.
		_, _ = io.WriteString(w, strings.Repeat("aaaa", 1024))
		// Flush mid-response to exercise Flusher path.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "tail")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Accept-Encoding", "br;q=1.0, gzip;q=0.5")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("missing Vary header: %v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length should have been stripped")
	}
	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(body), "tail") || len(body) < 4096 {
		t.Fatalf("decoded body wrong: len=%d", len(body))
	}
}

// TestGzipMiddlewarePassthrough confirms clients that don't advertise
// gzip receive the raw bytes.
func TestGzipMiddlewarePassthrough(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	// No Accept-Encoding header.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("should not have encoded; headers=%v", rec.Header())
	}
	if rec.Body.String() != "plain" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

// TestGzipMiddlewareSkipsPreEncoded confirms that handlers that have
// already set Content-Encoding (e.g. served a pre-compressed asset)
// are not double-encoded.
func TestGzipMiddlewareSkipsPreEncoded(t *testing.T) {
	preBody := []byte("pretend-this-is-gzip")
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		_, _ = w.Write(preBody)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("ce=%q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != string(preBody) {
		t.Fatalf("body=%q want %q", rec.Body.String(), string(preBody))
	}
}

// TestClientAcceptsGzip covers the header-token parsing branches.
func TestClientAcceptsGzip(t *testing.T) {
	cases := []struct {
		hdr  string
		want bool
	}{
		{"", false},
		{"identity", false},
		{"gzip", true},
		{"GZIP", true},
		{"deflate, gzip", true},
		{"gzip;q=0.5", true},
		{"br, deflate", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if tc.hdr != "" {
			r.Header.Set("Accept-Encoding", tc.hdr)
		}
		if got := clientAcceptsGzip(r); got != tc.want {
			t.Errorf("%q: got=%v want=%v", tc.hdr, got, tc.want)
		}
	}
}
