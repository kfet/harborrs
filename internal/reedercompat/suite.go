package reedercompat

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Reeder/GReader response shapes the suite asserts against. These are
// JSON-shape mirrors of the implementation's internal types — keep
// the field tags exactly equal to what real clients parse.

type streamLinkJSON struct {
	HREF string `json:"href"`
	Type string `json:"type,omitempty"`
}

type streamContentJSON struct {
	Direction string `json:"direction,omitempty"`
	Content   string `json:"content"`
}

type streamOriginJSON struct {
	StreamID string `json:"streamId"`
	Title    string `json:"title,omitempty"`
	HTMLURL  string `json:"htmlUrl,omitempty"`
}

type streamItemJSON struct {
	ID            string            `json:"id"`
	LongID        string            `json:"longId"`
	Categories    []string          `json:"categories"`
	Title         string            `json:"title"`
	Published     int64             `json:"published"`
	Updated       int64             `json:"updated"`
	CrawlTimeMsec string            `json:"crawlTimeMsec"`
	TimestampUsec string            `json:"timestampUsec"`
	Author        string            `json:"author,omitempty"`
	Alternate     []streamLinkJSON  `json:"alternate"`
	Summary       streamContentJSON `json:"summary"`
	Origin        streamOriginJSON  `json:"origin"`
}

type streamResponseJSON struct {
	ID           string           `json:"id"`
	Updated      int64            `json:"updated"`
	Title        string           `json:"title,omitempty"`
	Items        []streamItemJSON `json:"items"`
	Continuation string           `json:"continuation,omitempty"`
}

type itemRefJSON struct {
	ID              string   `json:"id"`
	LongID          string   `json:"longId"`
	TimestampUsec   string   `json:"timestampUsec"`
	DirectStreamIDs []string `json:"directStreamIds"`
}

type itemsIDsResponseJSON struct {
	ItemRefs     []itemRefJSON `json:"itemRefs"`
	Continuation string        `json:"continuation"`
}

// ---- assertion helpers ----

func assertItemHex16(t *testing.T, contract, id string) {
	t.Helper()
	if !ItemIDHex16Pattern.MatchString(id) {
		t.Errorf("compat %s: item id %q is not tag:google.com,2005:reader/item/<16-hex>", contract, id)
	}
}

func assertSignedInt64(t *testing.T, contract, s string) {
	t.Helper()
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		t.Errorf("compat %s: %q does not parse as signed int64: %v", contract, s, err)
	}
}

// Run executes the full Reeder / GReader conformance suite against the
// embedder-supplied Harness factory. Each sub-test gets a fresh
// Harness via newH(t).
func Run(t *testing.T, newH NewHarness) {
	t.Run("item-id-16-hex+published-timestamps/contents", func(t *testing.T) {
		h := newH(t)
		u, hashes := h.SeedFeed(t, "F", "F", 2)
		w := Do(t, h, "GET", "/reader/api/0/stream/contents/feed/"+u, nil)
		if w.Code != 200 {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		var resp streamResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Items) != 2 {
			t.Fatalf("items=%d, want 2", len(resp.Items))
		}
		want := map[string]bool{}
		for _, hh := range hashes {
			want[ItemID(hh)] = true
		}
		for _, it := range resp.Items {
			assertItemHex16(t, "item-id-16-hex/contents", it.ID)
			if !want[it.ID] {
				t.Errorf("compat item-id-16-hex/contents: unexpected id %q", it.ID)
			}
			if it.TimestampUsec == "" {
				t.Errorf("compat published-timestamps/contents: missing timestampUsec for %q", it.ID)
			}
			if it.CrawlTimeMsec == "" {
				t.Errorf("compat published-timestamps/contents: missing crawlTimeMsec for %q", it.ID)
			}
		}
	})

	t.Run("item-ref-decimal+longid-int64/items-ids", func(t *testing.T) {
		h := newH(t)
		_, hashes := h.SeedFeed(t, "F", "F", 3)
		w := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+StreamReadingList, nil)
		if w.Code != 200 {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.ItemRefs) != 3 {
			t.Fatalf("itemRefs=%d, want 3", len(resp.ItemRefs))
		}
		want := map[string]bool{}
		for _, hh := range hashes {
			want[ItemLongID(hh)] = true
		}
		for _, r := range resp.ItemRefs {
			assertSignedInt64(t, "item-ref-decimal/items-ids", r.ID)
			if r.ID != r.LongID {
				t.Errorf("compat item-ref-decimal/items-ids: id=%q longId=%q must be equal", r.ID, r.LongID)
			}
			assertSignedInt64(t, "longid-int64", r.LongID)
			assertSignedInt64(t, "timestampUsec-int64", r.TimestampUsec)
			if !want[r.ID] {
				t.Errorf("compat item-ref-decimal/items-ids: unexpected longId %q", r.ID)
			}
		}
	})

	t.Run("longid-roundtrip/highbit-negative", func(t *testing.T) {
		// 0xba7fcb8d8885006e has the high bit set → longId must be
		// a negative signed-int64 decimal.
		const hash16 = "ba7fcb8d8885006e"
		long := ItemLongID(hash16)
		if !strings.HasPrefix(long, "-") {
			t.Errorf("compat longid-roundtrip: expected negative decimal for high-bit hash %q, got %q", hash16, long)
		}
		assertSignedInt64(t, "longid-roundtrip", long)
	})

	t.Run("accept-decimal-longid/edit-tag", func(t *testing.T) {
		h := newH(t)
		_, hashes := h.SeedFeed(t, "F", "F", 2)
		// Decimal long id (Reeder uses this shape on writes).
		long := ItemLongID(hashes[1])
		assertSignedInt64(t, "accept-decimal-longid", long)
		body := url.Values{"i": {long}, "a": {StateStarredID}}
		if w := Do(t, h, "POST", "/reader/api/0/edit-tag", body); w.Code != 200 {
			t.Fatalf("compat accept-decimal-longid: code=%d body=%s", w.Code, w.Body.String())
		}
		// Verify via stream/items/ids?s=starred
		w := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape(StateStarredID), nil)
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.ItemRefs) != 1 || resp.ItemRefs[0].ID != long {
			t.Errorf("compat accept-decimal-longid: starred set=%+v, want only %q", resp.ItemRefs, long)
		}
	})

	t.Run("item-categories/reading-list+label", func(t *testing.T) {
		h := newH(t)
		u, _ := h.SeedFeed(t, "Tech", "Tech", 1)
		w := Do(t, h, "GET", "/reader/api/0/stream/contents/feed/"+u, nil)
		var resp streamResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if len(resp.Items) != 1 {
			t.Fatalf("items=%d, want 1", len(resp.Items))
		}
		cats := resp.Items[0].Categories
		has := func(want string) bool {
			for _, c := range cats {
				if c == want {
					return true
				}
			}
			return false
		}
		for _, want := range []string{StreamReadingList, LabelStreamID("Tech")} {
			if !has(want) {
				t.Errorf("compat item-categories: %v missing %q", cats, want)
			}
		}
		if has(StateReadID) {
			t.Errorf("compat item-categories: unread item carries read state: %v", cats)
		}
	})

	t.Run("direct-stream-ids/items-ids", func(t *testing.T) {
		h := newH(t)
		u, _ := h.SeedFeed(t, "News", "News", 1)
		w := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+StreamReadingList, nil)
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.ItemRefs) != 1 {
			t.Fatalf("itemRefs=%d, want 1", len(resp.ItemRefs))
		}
		ds := resp.ItemRefs[0].DirectStreamIDs
		seen := map[string]bool{}
		for _, s := range ds {
			seen[s] = true
		}
		if !seen[FeedStreamID(u)] || !seen[LabelStreamID("News")] {
			t.Errorf("compat direct-stream-ids: %v missing %q and/or %q", ds, FeedStreamID(u), LabelStreamID("News"))
		}
	})

	t.Run("preserve-i-order/items-contents", func(t *testing.T) {
		h := newH(t)
		_, hashes := h.SeedFeed(t, "F", "F", 3)
		want := []string{ItemID(hashes[2]), ItemID(hashes[0]), ItemID(hashes[1])}
		body := url.Values{}
		for _, id := range want {
			body.Add("i", id)
		}
		w := Do(t, h, "POST", "/reader/api/0/stream/items/contents", body)
		if w.Code != 200 {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		var resp streamResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Items) != len(want) {
			t.Fatalf("items=%d, want %d", len(resp.Items), len(want))
		}
		for i, it := range resp.Items {
			if it.ID != want[i] {
				t.Errorf("compat preserve-i-order: item[%d]=%q, want %q", i, it.ID, want[i])
			}
		}
	})

	t.Run("reading-list-all-vs-xt-unread/items-ids", func(t *testing.T) {
		h := newH(t)
		_, hashes := h.SeedFeed(t, "F", "F", 3)
		h.SetRead(t, hashes[0], true)

		all := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+StreamReadingList+"&n=10", nil)
		unread := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+StreamReadingList+"&xt="+url.QueryEscape(StateReadID)+"&n=10", nil)
		var allResp, unreadResp itemsIDsResponseJSON
		if err := json.Unmarshal(all.Body.Bytes(), &allResp); err != nil {
			t.Fatalf("unmarshal all: %v", err)
		}
		if err := json.Unmarshal(unread.Body.Bytes(), &unreadResp); err != nil {
			t.Fatalf("unmarshal unread: %v", err)
		}
		if len(allResp.ItemRefs) != 3 {
			t.Errorf("compat reading-list-all: bare reading-list returned %d refs, want all 3", len(allResp.ItemRefs))
		}
		if len(unreadResp.ItemRefs) != 2 {
			t.Errorf("compat reading-list-xt-unread: itemRefs=%d, want 2 (read entry must be excluded only by xt=read)", len(unreadResp.ItemRefs))
		}
		readID := ItemLongID(hashes[0])
		for _, r := range unreadResp.ItemRefs {
			if r.ID == readID {
				t.Errorf("compat reading-list-xt-unread: read id %q leaked through xt=read", r.ID)
			}
		}
	})

	t.Run("ot-nt-filters-content-stream/items-ids", func(t *testing.T) {
		// On content streams (feed/, label/, reading-list) ot/nt
		// compare against entry fetch/publish time. This is the
		// pre-existing contract; assert it still holds.
		h := newH(t)
		base := time.Now().UTC().Add(-3 * time.Hour)
		u, _ := h.SeedFeedAt(t, "Filters", "F", []time.Time{
			base,
			base.Add(time.Hour),
			base.Add(2 * time.Hour),
		})
		path := "/reader/api/0/stream/items/ids?s=" + url.QueryEscape("feed/"+u) +
			"&ot=" + strconv.FormatInt(base.Add(30*time.Minute).Unix(), 10) +
			"&nt=" + strconv.FormatInt(base.Add(90*time.Minute).Unix(), 10) + "&n=10"
		w := Do(t, h, "GET", path, nil)
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if len(resp.ItemRefs) != 1 {
			t.Errorf("compat ot-nt-filters/content: got %d refs, want exactly the entry between ot and nt; body=%s", len(resp.ItemRefs), w.Body.String())
		}
		future := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape("feed/"+u)+
			"&ot="+strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10), nil)
		if !strings.Contains(future.Body.String(), `"itemRefs":[]`) {
			t.Errorf("compat ot-nt-filters: empty result must encode itemRefs as [], body=%s", future.Body.String())
		}
	})

	t.Run("ot-state-stream-uses-state-updatedat/read", func(t *testing.T) {
		// THE BUG: stream/items/ids?s=read&ot=X must return items
		// whose READ STATE was updated after X — not items whose
		// fetch/publish time is after X. Reeder uses this as its
		// incremental read-state sync; if it returns "everything
		// recently fetched", Reeder re-marks every just-polled item
		// as read on every sync and clobbers its own unread display.
		//
		// We construct a scenario that is observable through the
		// public HTTP surface and that DIFFERENTIATES the buggy
		// "FetchedAt-based" filter from the correct "UpdatedAt-based"
		// filter: entries with FetchedAt in the FAR FUTURE, then
		// marked read (UpdatedAt ≈ now). A query with
		// ot = (mark + 30min) MUST exclude them under the correct
		// semantics; under the buggy semantics they leak through
		// because FetchedAt is hours after ot.
		h := newH(t)
		const total, marked = 5, 3
		futureFetched := time.Now().UTC().Add(time.Hour)
		fetched := make([]time.Time, total)
		for i := range fetched {
			fetched[i] = futureFetched
		}
		_, hashes := h.SeedFeedAt(t, "RS", "RS", fetched)
		var tMark time.Time
		for i := 0; i < marked; i++ {
			tMark = h.SetRead(t, hashes[i], true)
		}
		// All-time s=read returns the marked set.
		full := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape(StateReadID)+"&n=100", nil)
		var fullResp itemsIDsResponseJSON
		if err := json.Unmarshal(full.Body.Bytes(), &fullResp); err != nil {
			t.Fatalf("unmarshal full: %v", err)
		}
		if len(fullResp.ItemRefs) != marked {
			t.Fatalf("compat ot-state-stream/baseline: s=read returned %d refs, want %d", len(fullResp.ItemRefs), marked)
		}
		// ot well before the mark → include all marked.
		before := tMark.Add(-30 * time.Minute).Unix()
		w := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape(StateReadID)+
			"&ot="+strconv.FormatInt(before, 10)+"&n=100", nil)
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if len(resp.ItemRefs) != marked {
			t.Errorf("compat ot-state-stream-uses-state-updatedat/read: ot=%d (before mark) returned %d refs, want %d", before, len(resp.ItemRefs), marked)
		}
		// ot AFTER the mark but well before the (future) FetchedAt:
		// the differentiating case. The buggy filter would compare
		// FetchedAt (= now+1h) against ot (= now+30min), keep the
		// items, and return `marked`. The correct filter compares
		// UpdatedAt (≈ now) against ot and returns 0.
		after := tMark.Add(30 * time.Minute).Unix()
		w2 := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape(StateReadID)+
			"&ot="+strconv.FormatInt(after, 10)+"&n=100", nil)
		var resp2 itemsIDsResponseJSON
		if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w2.Body.String())
		}
		if len(resp2.ItemRefs) != 0 {
			t.Errorf("compat ot-state-stream-uses-state-updatedat/read: ot=%d (after mark, before FetchedAt) returned %d refs, want 0 — filter is comparing entry fetch time instead of state UpdatedAt", after, len(resp2.ItemRefs))
		}
		// Cross-check: s=reading-list&xt=read returns the unread N-M.
		unread := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+StreamReadingList+
			"&xt="+url.QueryEscape(StateReadID)+"&n=100", nil)
		var unreadResp itemsIDsResponseJSON
		if err := json.Unmarshal(unread.Body.Bytes(), &unreadResp); err != nil {
			t.Fatalf("unmarshal unread: %v", err)
		}
		if len(unreadResp.ItemRefs) != total-marked {
			t.Errorf("compat ot-state-stream cross-check: reading-list&xt=read returned %d, want %d", len(unreadResp.ItemRefs), total-marked)
		}
	})

	t.Run("ot-state-stream-uses-state-updatedat/starred", func(t *testing.T) {
		// Same differentiating construction for the starred stream.
		h := newH(t)
		const total, marked = 4, 2
		futureFetched := time.Now().UTC().Add(time.Hour)
		fetched := make([]time.Time, total)
		for i := range fetched {
			fetched[i] = futureFetched
		}
		_, hashes := h.SeedFeedAt(t, "ST", "ST", fetched)
		var tMark time.Time
		for i := 0; i < marked; i++ {
			tMark = h.SetStarred(t, hashes[i], true)
		}
		before := tMark.Add(-30 * time.Minute).Unix()
		w := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape(StateStarredID)+
			"&ot="+strconv.FormatInt(before, 10)+"&n=100", nil)
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		if len(resp.ItemRefs) != marked {
			t.Errorf("compat ot-state-stream/starred: ot before mark returned %d, want %d", len(resp.ItemRefs), marked)
		}
		after := tMark.Add(30 * time.Minute).Unix()
		w2 := Do(t, h, "GET", "/reader/api/0/stream/items/ids?s="+url.QueryEscape(StateStarredID)+
			"&ot="+strconv.FormatInt(after, 10)+"&n=100", nil)
		var resp2 itemsIDsResponseJSON
		if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w2.Body.String())
		}
		if len(resp2.ItemRefs) != 0 {
			t.Errorf("compat ot-state-stream/starred: ot after mark (before FetchedAt) returned %d, want 0 — filter is comparing fetch time, not state UpdatedAt", len(resp2.ItemRefs))
		}
	})

	t.Run("it-filter/selects-starred", func(t *testing.T) {
		h := newH(t)
		_, hashes := h.SeedFeed(t, "F", "F", 3)
		h.SetStarred(t, hashes[2], true)
		u := "https://feed.example/F"
		path := "/reader/api/0/stream/items/ids?s=" + url.QueryEscape("feed/"+u) +
			"&it=" + url.QueryEscape(StateStarredID)
		w := Do(t, h, "GET", path, nil)
		var resp itemsIDsResponseJSON
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		wantID := ItemLongID(hashes[2])
		if len(resp.ItemRefs) != 1 || resp.ItemRefs[0].ID != wantID {
			t.Errorf("compat it-filter: refs=%+v, want only %q", resp.ItemRefs, wantID)
		}
	})

	t.Run("continuation-paging/items-ids", func(t *testing.T) {
		h := newH(t)
		const total = 25
		h.SeedFeed(t, "F", "F", total)
		seen := map[string]bool{}
		cont := ""
		pages := 0
		for {
			path := "/reader/api/0/stream/items/ids?s=" + StreamReadingList + "&n=10"
			if cont != "" {
				path += "&c=" + cont
			}
			w := Do(t, h, "GET", path, nil)
			if w.Code != 200 {
				t.Fatalf("page %d code=%d body=%s", pages, w.Code, w.Body.String())
			}
			var p itemsIDsResponseJSON
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, r := range p.ItemRefs {
				if seen[r.ID] {
					t.Errorf("compat continuation-paging: duplicate id %q on page %d", r.ID, pages)
				}
				seen[r.ID] = true
			}
			pages++
			if p.Continuation == "" {
				break
			}
			// Token shape sanity-check.
			dec, err := base64.RawURLEncoding.DecodeString(p.Continuation)
			if err != nil {
				t.Fatalf("compat continuation-paging: token not base64-raw: %v", err)
			}
			var off struct {
				Offset int `json:"o"`
			}
			if err := json.Unmarshal(dec, &off); err != nil {
				t.Fatalf("compat continuation-paging: token not {\"o\":int}: %v", err)
			}
			cont = p.Continuation
			if pages > 10 {
				t.Fatal("compat continuation-paging: walked too many pages (infinite loop)")
			}
		}
		if len(seen) != total {
			t.Errorf("compat continuation-paging: saw %d distinct ids, want %d", len(seen), total)
		}
	})

	t.Run("unread-count-newest/per-row+global+output-json", func(t *testing.T) {
		h := newH(t)
		base := time.Now().UTC().Add(-24 * time.Hour)
		urlA, _ := h.SeedFeedAt(t, "A", "A", []time.Time{base, base.Add(1 * time.Hour)})
		urlB, _ := h.SeedFeedAt(t, "B", "B", []time.Time{base.Add(10 * time.Hour)})
		w := Do(t, h, "GET", "/reader/api/0/unread-count?output=json", nil)
		if w.Code != 200 {
			t.Fatalf("compat output-json: code=%d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			UnreadCounts []struct {
				ID                      string `json:"id"`
				Count                   int    `json:"count"`
				NewestItemTimestampUsec string `json:"newestItemTimestampUsec"`
			} `json:"unreadcounts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		got := map[string]string{}
		gotCount := map[string]int{}
		for _, r := range resp.UnreadCounts {
			if r.NewestItemTimestampUsec == "" {
				t.Errorf("compat unread-count-newest: row %q has empty newestItemTimestampUsec", r.ID)
			}
			assertSignedInt64(t, "unread-count-newest", r.NewestItemTimestampUsec)
			got[r.ID] = r.NewestItemTimestampUsec
			gotCount[r.ID] = r.Count
		}
		if got["feed/"+urlA] == "" {
			t.Errorf("compat unread-count-newest: feed A missing")
		}
		if got["feed/"+urlB] == "" {
			t.Errorf("compat unread-count-newest: feed B missing")
		}
		if got[StreamReadingList] == "" {
			t.Errorf("compat unread-count-newest: reading-list missing")
		}
		if gotCount[StreamReadingList] != 3 {
			t.Errorf("compat unread-count-newest: reading-list count=%d, want 3", gotCount[StreamReadingList])
		}
	})
}
