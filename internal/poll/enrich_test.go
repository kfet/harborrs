package poll

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kfet/harb/internal/safedial"
	"github.com/kfet/harb/internal/store"
)

func TestLargestRichText(t *testing.T) {
	multi := `<div class="w-richtext"><p>short</p></div>` +
		`<div class="w-richtext"><p>this is a much longer article body with plenty of text</p></div>`
	got := largestRichText([]byte(multi), ".w-richtext")
	if !strings.Contains(got, "longer article body") {
		t.Errorf("did not pick largest block: %q", got)
	}
	if strings.Contains(got, "short") {
		t.Errorf("picked the wrong (smaller) block: %q", got)
	}
	// No matching selector -> "".
	if got := largestRichText([]byte(`<div>plain</div>`), ".w-richtext"); got != "" {
		t.Errorf("no-match should be empty, got %q", got)
	}
	// Whitespace-only block -> "" (best stays unset).
	if got := largestRichText([]byte(`<div class="w-richtext">   </div>`), ".w-richtext"); got != "" {
		t.Errorf("whitespace-only should be empty, got %q", got)
	}
}

func TestFetchRichText(t *testing.T) {
	ctx := context.Background()
	client := safedial.NewClient(5 * time.Second) // loopback allowed via TestMain env

	// NewRequest error (NUL in URL).
	if got := fetchRichText(ctx, client, "ua", "http://x/\x00"); got != "" {
		t.Errorf("bad url should be empty, got %q", got)
	}
	// Transport error: server closed before request.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	badURL := bad.URL
	bad.Close()
	if got := fetchRichText(ctx, client, "ua", badURL); got != "" {
		t.Errorf("conn error should be empty, got %q", got)
	}
	// Non-200.
	s500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer s500.Close()
	if got := fetchRichText(ctx, client, "ua", s500.URL); got != "" {
		t.Errorf("500 should be empty, got %q", got)
	}
	// Success.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<div class="w-richtext"><p>hello world article body</p></div>`)
	}))
	defer ok.Close()
	if got := fetchRichText(ctx, client, "ua", ok.URL); !strings.Contains(got, "hello world article body") {
		t.Errorf("success expected body, got %q", got)
	}
	// Size cap exceeded.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("a", enrichMaxBytes+1024))
	}))
	defer big.Close()
	if got := fetchRichText(ctx, client, "ua", big.URL); got != "" {
		t.Errorf("oversize should be empty, got %q", got)
	}
	// Read-body error: claim a large Content-Length then close mid-stream.
	rb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if hj, ok := w.(http.Hijacker); ok {
			c, _, _ := hj.Hijack()
			c.Close()
		}
	}))
	defer rb.Close()
	if got := fetchRichText(ctx, client, "ua", rb.URL); got != "" {
		t.Errorf("read error should be empty, got %q", got)
	}
}

// enrichWebflowContent skips entries with no link and entries that are not
// new, fetches only the new+linked ones, and mutates Content in place.
func TestEnrichWebflowContentSkips(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, `<div class="w-richtext"><p>the fetched body text</p></div>`)
	}))
	defer srv.Close()
	client := safedial.NewClient(5 * time.Second)
	entries := []store.Entry{
		{Hash: "new1", Link: srv.URL},
		{Hash: "empty", Link: ""},      // new but no link -> skipped
		{Hash: "known", Link: srv.URL}, // not new -> skipped
	}
	isNew := func(h string) bool { return h != "known" }
	enrichWebflowContent(context.Background(), client, "ua", entries, isNew)
	if !strings.Contains(entries[0].Content, "the fetched body text") {
		t.Errorf("new1 not enriched: %q", entries[0].Content)
	}
	if entries[1].Content != "" || entries[2].Content != "" {
		t.Errorf("skipped entries got content: %q / %q", entries[1].Content, entries[2].Content)
	}
	if hits != 1 {
		t.Errorf("hits=%d, want 1 (only the new+linked entry)", hits)
	}
}
