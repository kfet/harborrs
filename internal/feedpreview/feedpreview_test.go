package feedpreview

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const rss = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<title>Example</title>
<description>an example feed</description>
<link>https://example.com</link>
<item><title>One</title><link>https://example.com/1</link></item>
<item><title>Two</title><link>https://example.com/2</link></item>
</channel></rss>`

func TestPreviewOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rss)
	}))
	defer srv.Close()
	p := New()
	out, err := p.Preview(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Example" || out.Link != "https://example.com" {
		t.Fatalf("bad out: %+v", out)
	}
	if len(out.Items) != 2 || out.Items[0].Title != "One" {
		t.Fatalf("bad items: %+v", out.Items)
	}
}

func TestPreviewLimitsTo10Items(t *testing.T) {
	var items strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&items, "<item><title>e%d</title></item>", i)
	}
	body := fmt.Sprintf(`<rss version="2.0"><channel><title>X</title>%s</channel></rss>`, items.String())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	out, err := New().Preview(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 10 {
		t.Fatalf("items=%d", len(out.Items))
	}
}

func TestPreviewNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := New().Preview(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v", err)
	}
}

func TestPreviewBadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately so Client.Do fails to connect
	_, err := New().Preview(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "fetch:") {
		t.Fatalf("err=%v", err)
	}
}

func TestPreviewBadRequest(t *testing.T) {
	// NUL byte makes NewRequestWithContext error before any network call.
	_, err := New().Preview("http://example.com/\x00")
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("err=%v", err)
	}
}

func TestPreviewParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not a feed at all, just plain text")
	}))
	defer srv.Close()
	_, err := New().Preview(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("err=%v", err)
	}
}

func TestPreviewSizeCap(t *testing.T) {
	// Serve >5 MiB so the LimitReader trips.
	big := strings.Repeat("a", MaxBytes+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, big) }))
	defer srv.Close()
	_, err := New().Preview(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "5 MiB") {
		t.Fatalf("err=%v", err)
	}
}

func TestPreviewReadBodyError(t *testing.T) {
	// Server claims a large Content-Length but closes mid-stream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	defer srv.Close()
	p := &Previewer{Client: &http.Client{Timeout: 2 * time.Second}, Parser: New().Parser}
	_, err := p.Preview(srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
}
