// Package feedpreview provides a small HTTP-client + gofeed wrapper
// that the UI uses to render the add-feed preview page.
//
// Separated from internal/poll so the preview path doesn't accidentally
// touch the on-disk store. The interface is satisfied by *Previewer and
// consumed by internal/ui via the ui.FeedPreviewer interface.
//
// The preview path applies the same resolver chain the poller uses
// (builtins + the per-feed sidecar at <data-dir>/resolvers/<feedHash>.json)
// so that a page which only becomes a feed after a resolver Transform — a
// feedless Webflow blog index fixed by a webflow-to-feed sidecar, say —
// previews (and thus adds) correctly instead of failing with "Failed to
// detect feed type".
package feedpreview

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"time"

	"github.com/kfet/harb/internal/poll/resolve"
	"github.com/kfet/harb/internal/safedial"
	"github.com/kfet/harb/internal/store"
	"github.com/kfet/harb/internal/ui"
	"github.com/mmcdole/gofeed"
)

// MaxBytes is the per-fetch read cap. 5 MiB is comfortably more than
// any well-behaved feed and still rejects accidental endpoints that
// stream html or video.
const MaxBytes = 5 * 1024 * 1024

// Timeout is the wall-clock cap on a single Preview call.
const Timeout = 15 * time.Second

// Previewer is the concrete FeedPreviewer used by harb serve.
type Previewer struct {
	Client *http.Client
	Parser *gofeed.Parser

	// DataDir is the harb data dir. When set, Preview loads each URL's
	// resolver sidecar from <DataDir>/resolvers/<feedHash>.json so the
	// preview applies the same transforms the poller would. Empty is
	// valid: the chain is then builtins-only (a missing sidecar is not
	// an error), so behaviour matches the legacy preview plus builtins.
	DataDir string

	// Resolve loads the resolver chain for a feed URL's hash. Defaults to
	// resolve.Load; overridable in tests to inject a chain.
	Resolve func(dir, feedHash string) (resolve.Chain, error)
}

// New returns a Previewer with a sensible default client/parser pair.
// dataDir is the harb data dir used to locate per-feed resolver sidecars;
// pass "" to run with builtins only (the SSRF-safe client and parser are
// unaffected). The client refuses connections to private/loopback/
// link-local addresses (SSRF guard); set HARB_ALLOW_PRIVATE_FETCH=1 to
// allow previewing feeds on a private network.
func New(dataDir string) *Previewer {
	return &Previewer{
		Client:  safedial.NewClient(Timeout),
		Parser:  gofeed.NewParser(),
		DataDir: dataDir,
	}
}

// Preview fetches url, applies the per-feed resolver chain (builtins +
// sidecar), parses the result as RSS/Atom/JSON-feed, and returns a
// trimmed view suitable for the add-feed UI. At most 10 items are
// returned to keep the page snappy.
func (p *Previewer) Preview(url string) (*ui.FeedPreview, error) {
	// Build the per-feed chain (builtins + sidecar). A sidecar error is
	// non-fatal — the returned chain is always usable — so it is ignored
	// here exactly as the poll path does, and any genuine breakage still
	// surfaces at parse time.
	loadChain := p.Resolve
	if loadChain == nil {
		loadChain = resolve.Load
	}
	chain, _ := loadChain(p.DataDir, store.FeedHash(url))

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "harb-feed-preview/1")
	// Request-shaping resolvers run after defaults so a sidecar can
	// override the User-Agent or add headers a feed's CDN demands.
	if err := chain.ShapeRequest(req, resolve.FeedMeta{URL: url}); err != nil {
		return nil, fmt.Errorf("shape request: %w", err)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > MaxBytes {
		return nil, errors.New("feed exceeds 5 MiB read cap")
	}
	meta := resolve.FeedMeta{URL: url, ContentType: resp.Header.Get("Content-Type"), Status: resp.StatusCode}
	transformed, err := chain.Transform(body, meta)
	if err != nil {
		return nil, fmt.Errorf("transform: %w", err)
	}
	f, err := p.Parser.ParseString(string(transformed))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := &ui.FeedPreview{
		Title:       html.UnescapeString(f.Title),
		Description: f.Description,
		Link:        f.Link,
	}
	max := len(f.Items)
	if max > 10 {
		max = 10
	}
	for i := 0; i < max; i++ {
		out.Items = append(out.Items, ui.FeedPreviewItem{Title: html.UnescapeString(f.Items[i].Title)})
	}
	return out, nil
}
