// Package feedpreview provides a small HTTP-client + gofeed wrapper
// that the UI uses to render the add-feed preview page.
//
// Separated from internal/poll so the preview path doesn't accidentally
// touch the on-disk store. The interface is satisfied by *Previewer and
// consumed by internal/ui via the ui.FeedPreviewer interface.
package feedpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kfet/harborrs/internal/ui"
	"github.com/mmcdole/gofeed"
)

// MaxBytes is the per-fetch read cap. 5 MiB is comfortably more than
// any well-behaved feed and still rejects accidental endpoints that
// stream html or video.
const MaxBytes = 5 * 1024 * 1024

// Timeout is the wall-clock cap on a single Preview call.
const Timeout = 15 * time.Second

// Previewer is the concrete FeedPreviewer used by harborrs serve.
type Previewer struct {
	Client *http.Client
	Parser *gofeed.Parser
}

// New returns a Previewer with a sensible default client/parser pair.
func New() *Previewer {
	return &Previewer{
		Client: &http.Client{Timeout: Timeout},
		Parser: gofeed.NewParser(),
	}
}

// Preview fetches url, parses it as RSS/Atom/JSON-feed, and returns a
// trimmed view suitable for the add-feed UI. At most 10 items are
// returned to keep the page snappy.
func (p *Previewer) Preview(url string) (*ui.FeedPreview, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "harborrs-feed-preview/1")
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
	f, err := p.Parser.ParseString(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := &ui.FeedPreview{
		Title:       f.Title,
		Description: f.Description,
		Link:        f.Link,
	}
	max := len(f.Items)
	if max > 10 {
		max = 10
	}
	for i := 0; i < max; i++ {
		out.Items = append(out.Items, ui.FeedPreviewItem{Title: f.Items[i].Title})
	}
	return out, nil
}
