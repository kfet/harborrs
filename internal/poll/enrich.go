package poll

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/kfet/harb/internal/store"
)

const (
	// richTextSelector is the Webflow rich-text container that holds a
	// post's article body on its detail page. A page may have several
	// (figure captions, callouts); the article body is the largest by text.
	richTextSelector = ".w-richtext"
	// enrichWorkers bounds concurrent detail-page fetches so the first
	// poll of a large blog (~26 pages for claude.com/blog) stays sane
	// without hammering the origin.
	enrichWorkers = 4
	// enrichTimeout caps each detail-page fetch.
	enrichTimeout = 15 * time.Second
	// enrichMaxBytes caps each detail-page response (matches the preview
	// path's 5 MiB read cap).
	enrichMaxBytes = 5 * 1024 * 1024
)

// enrichWebflowContent fills entry.Content for every entry whose hash is
// new (isNew returns true) by fetching the entry's detail page and
// extracting the largest rich-text block's inner HTML. It mutates entries
// in place and is strictly best-effort: any per-entry fetch/parse failure
// leaves that entry's Content untouched and it never returns an error, so
// the caller's poll cannot fail because of enrichment. Detail-page fetches
// are bounded to enrichWorkers concurrent requests through the SSRF-safe
// client passed in (the poller's own client). Each worker writes a
// distinct slice element, so the in-place mutation is race-free.
func enrichWebflowContent(ctx context.Context, client *http.Client, ua string, entries []store.Entry, isNew func(hash string) bool) {
	var idxs []int
	for i := range entries {
		if entries[i].Link != "" && isNew(entries[i].Hash) {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < enrichWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if c := fetchRichText(ctx, client, ua, entries[i].Link); c != "" {
					entries[i].Content = c
				}
			}
		}()
	}
	for _, i := range idxs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// fetchRichText GETs url with the SSRF-safe client, reads up to
// enrichMaxBytes, and returns the largest .w-richtext block's inner HTML,
// or "" on any failure (best-effort).
func fetchRichText(ctx context.Context, client *http.Client, ua, url string) string {
	ctx, cancel := context.WithTimeout(ctx, enrichTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, enrichMaxBytes+1))
	if err != nil {
		return ""
	}
	if int64(len(body)) > enrichMaxBytes {
		return ""
	}
	return largestRichText(body, richTextSelector)
}

// largestRichText returns the trimmed inner HTML of the selector-matching
// element with the most text content, or "" if none match or the winner is
// empty. Raw HTML is intentional: harb stores entry Content as HTML.
func largestRichText(body []byte, selector string) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var best *goquery.Selection
	bestLen := 0
	doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
		if n := len(strings.TrimSpace(s.Text())); n > bestLen {
			bestLen = n
			best = s
		}
	})
	if best == nil {
		return ""
	}
	h, _ := best.Html()
	return strings.TrimSpace(h)
}
