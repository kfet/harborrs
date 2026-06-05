package feedpreview

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/harb/internal/poll/resolve"
	"github.com/kfet/harb/internal/store"
)

// webflowPage is a compact stock Webflow CMS blog index: a feedless HTML
// page that only becomes a feed after the webflow-to-feed resolver runs.
const webflowPage = `<!DOCTYPE html>
<html data-wf-site="abc" data-wf-page="def">
<head><title>Acme Blog</title><meta name="generator" content="Webflow"></head>
<body>
<div class="w-dyn-list"><div role="list" class="w-dyn-items">
  <div role="listitem" class="w-dyn-item">
    <a href="/blog/hello" class="card"><h3>Hello World</h3><time datetime="2026-05-01">May 1, 2026</time></a>
  </div>
  <div role="listitem" class="w-dyn-item">
    <a href="/blog/category/news" class="card"><h3>News Category</h3></a>
  </div>
  <div role="listitem" class="w-dyn-item">
    <a href="/blog/second" class="card"><h3>Second Post</h3></a>
  </div>
</div></div>
</body></html>`

// TestPreviewAppliesWebflowSidecar is the regression pin for the bug: a
// feedless Webflow page with a webflow-to-feed sidecar in the data dir
// must preview into a real feed with items.
func TestPreviewAppliesWebflowSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, webflowPage)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	fh := store.FeedHash(srv.URL)
	sidecar := resolve.SidecarPath(dataDir, fh)
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `[{"name":"webflow-to-feed","params":{"exclude_link_contains":"/category/"},"source":"user"}]`
	if err := os.WriteFile(sidecar, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := New(dataDir).Preview(srv.URL)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if out.Title != "Acme Blog" {
		t.Errorf("title = %q, want Acme Blog", out.Title)
	}
	// hello + second; the /category/ item is excluded by the sidecar param.
	if len(out.Items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(out.Items), out.Items)
	}
	if out.Items[0].Title != "Hello World" {
		t.Errorf("item0 = %q, want Hello World", out.Items[0].Title)
	}
}

// Without a sidecar, the same Webflow page is not a feed and previewing it
// fails at parse — confirming the no-sidecar path is unchanged (plus
// builtins) and resolvers are not applied unconditionally.
func TestPreviewNoSidecarWebflowStillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, webflowPage)
	}))
	defer srv.Close()
	_, err := New(t.TempDir()).Preview(srv.URL)
	if err == nil {
		t.Fatal("expected parse failure with no sidecar")
	}
}

// errResolver is an injectable resolver whose ShapeRequest and/or
// Transform fail, used to exercise the chain error branches in Preview.
type errResolver struct {
	shapeErr, transformErr bool
}

func (errResolver) Name() string                  { return "err" }
func (errResolver) Applies(resolve.FeedMeta) bool { return true }
func (e errResolver) ShapeRequest(*http.Request) error {
	if e.shapeErr {
		return fmt.Errorf("boom-shape")
	}
	return nil
}
func (e errResolver) Transform(body []byte, _ resolve.FeedMeta) ([]byte, error) {
	if e.transformErr {
		return body, fmt.Errorf("boom-transform")
	}
	return body, nil
}

func TestPreviewShapeRequestError(t *testing.T) {
	p := New("")
	p.Resolve = func(string, string) (resolve.Chain, error) {
		return resolve.NewChain(errResolver{shapeErr: true}), nil
	}
	_, err := p.Preview("http://example.test/")
	if err == nil || !strings.Contains(err.Error(), "shape request") {
		t.Fatalf("err=%v, want shape request", err)
	}
}

func TestPreviewTransformError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "anything")
	}))
	defer srv.Close()
	p := New("")
	p.Resolve = func(string, string) (resolve.Chain, error) {
		return resolve.NewChain(errResolver{transformErr: true}), nil
	}
	_, err := p.Preview(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "transform") {
		t.Fatalf("err=%v, want transform", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
