package reader

// GReader / Reeder compatibility contract suite.
//
// This file is the named, lightweight compatibility surface for the
// Google Reader API dialect spoken by Reeder Classic, NetNewsWire,
// FreshRSS-compatible clients, etc. Each sub-test below pins one
// observable shape that real clients have been seen to require. When a
// sub-test fails, the assertion message reads as a contract violation,
// not a generic unit-test diff.
//
// The contracts asserted here are deliberately narrow:
//
//   item-id-16-hex:        stream/contents emits
//                          tag:google.com,2005:reader/item/<16-hex>
//   item-ref-decimal:      stream/items/ids emits decimal signed-int64
//                          itemRefs[].id, matching Google Reader/Reeder
//   longid-int64:          longId parses as a signed int64 (high-bit
//                          ids surface as negative decimals)
//   longid-roundtrip:      longId decodes back to the 16-hex hash
//   accept-legacy-20hex:   edit-tag accepts legacy 20-hex tag ids
//   accept-decimal-longid: edit-tag accepts the decimal longId form
//   item-categories:       items carry reading-list and label(s)
//   direct-stream-ids:     itemRefs carry feed/<url> + label/<tag>
//   preserve-i-order:      stream/items/contents preserves i= order
//   xt-filter:             xt=read excludes read entries
//   it-filter:             it=starred selects only starred entries
//   continuation-paging:   c= walks the full set, no dups, terminates
//   unread-count-newest:   every row has newestItemTimestampUsec
//   output-json:           output=json is accepted on unread-count
//
// Implementation note: helpers (fixture, do, seedFeed, itemID,
// itemLongID, itemIDToHash, stateReadID, stateStarredID,
// streamReadingList, feedStreamID, labelStreamID) are reused from
// reader_test.go in the same package — no duplication.

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/store"
)

// itemIDHex16 matches a well-formed GReader stream/content item id with
// exactly the 16-hex-character item suffix that Reeder/FreshRSS clients
// expect.
var itemIDHex16 = regexp.MustCompile(`^tag:google\.com,2005:reader/item/[0-9a-f]{16}$`)

// assertItemHex16 reports a compat violation if id is not a well-formed
// 16-hex GReader item id.
func assertItemHex16(t *testing.T, contract, id string) {
	t.Helper()
	if !itemIDHex16.MatchString(id) {
		t.Errorf("compat %s: item id %q is not tag:google.com,2005:reader/item/<16-hex>", contract, id)
	}
}

// assertSignedInt64 reports a compat violation if s is not a decimal
// signed int64 (i.e. the shape Reeder expects for longId).
func assertSignedInt64(t *testing.T, contract, s string) {
	t.Helper()
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		t.Errorf("compat %s: %q does not parse as signed int64: %v", contract, s, err)
	}
}

// TestGReaderCompat is the consolidated compatibility surface. Run as
//
//	go test -run TestGReaderCompat ./internal/reader/...
//
// to exercise just the contracts (without the fault-injection /
// coverage-driving tests in reader_test.go).
func TestGReaderCompat(t *testing.T) {
	t.Run("item-id-16-hex/contents", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 2, "F")
		w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u, tok, nil)
		if w.Code != 200 {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		var resp streamResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Items) != 2 {
			t.Fatalf("items=%d, want 2", len(resp.Items))
		}
		for _, it := range resp.Items {
			assertItemHex16(t, "item-id-16-hex/contents", it.ID)
		}
	})

	t.Run("item-ref-decimal+longid-int64/items-ids", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		seedFeed(t, op, st, 3, "F")
		w := do(t, mux, "GET", "/reader/api/0/stream/items/ids?s="+streamReadingList, tok, nil)
		if w.Code != 200 {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			ItemRefs []struct {
				ID            string `json:"id"`
				LongID        string `json:"longId"`
				TimestampUsec string `json:"timestampUsec"`
			} `json:"itemRefs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.ItemRefs) != 3 {
			t.Fatalf("itemRefs=%d, want 3", len(resp.ItemRefs))
		}
		for _, r := range resp.ItemRefs {
			assertSignedInt64(t, "item-ref-decimal/items-ids", r.ID)
			if r.ID != r.LongID {
				t.Errorf("compat item-ref-decimal/items-ids: id=%q, longId=%q; Reeder expects itemRefs[].id to be the decimal long id", r.ID, r.LongID)
			}
			assertSignedInt64(t, "longid-int64", r.LongID)
			assertSignedInt64(t, "timestampUsec-int64", r.TimestampUsec)
		}
	})

	t.Run("longid-roundtrip/highbit-negative", func(t *testing.T) {
		// 0xba7fcb8d8885006e has the high bit set → longId must be
		// negative when interpreted as a signed int64.
		const hash16 = "ba7fcb8d8885006e"
		long := itemLongID(hash16)
		if !strings.HasPrefix(long, "-") {
			t.Errorf("compat longid-roundtrip: expected negative decimal for high-bit hash %q, got %q", hash16, long)
		}
		assertSignedInt64(t, "longid-roundtrip", long)
		if got := itemIDToHash(long); got != hash16 {
			t.Errorf("compat longid-roundtrip: %q -> %q, want %q", long, got, hash16)
		}
		if got := itemIDToHash(itemID(hash16)); got != hash16 {
			t.Errorf("compat longid-roundtrip: tag id -> hash %q, want %q", got, hash16)
		}
	})

	t.Run("accept-legacy-20hex+decimal-longid/edit-tag", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 2, "F")
		es, _ := st.ListEntries(store.FeedHash(u))
		if len(es) != 2 {
			t.Fatalf("seed entries=%d, want 2", len(es))
		}
		// 1) Legacy 20-hex item id (what v0.4.4 and earlier emitted).
		legacy := "tag:google.com,2005:reader/item/" + es[0].Hash + "abcd"
		body := url.Values{"i": {legacy}, "a": {stateReadID}}
		if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body); w.Code != 200 {
			t.Fatalf("compat accept-legacy-20hex: code=%d body=%s", w.Code, w.Body.String())
		}
		if !st.EntryState(es[0].Hash).Read {
			t.Errorf("compat accept-legacy-20hex: legacy id did not mark canonical hash %q read", es[0].Hash)
		}
		// 2) Decimal long id (Reeder uses this shape on writes).
		long := itemLongID(es[1].Hash)
		assertSignedInt64(t, "accept-decimal-longid", long)
		body2 := url.Values{"i": {long}, "a": {stateStarredID}}
		if w := do(t, mux, "POST", "/reader/api/0/edit-tag", tok, body2); w.Code != 200 {
			t.Fatalf("compat accept-decimal-longid: code=%d body=%s", w.Code, w.Body.String())
		}
		if !st.EntryState(es[1].Hash).Starred {
			t.Errorf("compat accept-decimal-longid: decimal longId %q did not star %q", long, es[1].Hash)
		}
	})

	t.Run("item-categories/reading-list+label", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 1, "Tech")
		w := do(t, mux, "GET", "/reader/api/0/stream/contents/feed/"+u, tok, nil)
		var resp streamResponse
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
		for _, want := range []string{streamReadingList, labelStreamID("Tech")} {
			if !has(want) {
				t.Errorf("compat item-categories: %v missing %q", cats, want)
			}
		}
		// Unread item must NOT carry the read state.
		if has(stateReadID) {
			t.Errorf("compat item-categories: unread item carries read state: %v", cats)
		}
	})

	t.Run("direct-stream-ids/items-ids", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 1, "News")
		w := do(t, mux, "GET", "/reader/api/0/stream/items/ids?s="+streamReadingList, tok, nil)
		var resp struct {
			ItemRefs []struct {
				DirectStreamIDs []string `json:"directStreamIds"`
			} `json:"itemRefs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.ItemRefs) != 1 {
			t.Fatalf("itemRefs=%d, want 1", len(resp.ItemRefs))
		}
		ds := resp.ItemRefs[0].DirectStreamIDs
		wantFeed := feedStreamID(u)
		wantLabel := labelStreamID("News")
		seen := map[string]bool{}
		for _, s := range ds {
			seen[s] = true
		}
		if !seen[wantFeed] || !seen[wantLabel] {
			t.Errorf("compat direct-stream-ids: %v missing %q and/or %q", ds, wantFeed, wantLabel)
		}
	})

	t.Run("preserve-i-order/items-contents", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 3, "F")
		es, _ := st.ListEntries(store.FeedHash(u))
		want := []string{itemID(es[2].Hash), itemID(es[0].Hash), itemID(es[1].Hash)}
		body := url.Values{}
		for _, id := range want {
			body.Add("i", id)
		}
		w := do(t, mux, "POST", "/reader/api/0/stream/items/contents", tok, body)
		if w.Code != 200 {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		var resp streamResponse
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
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 3, "F")
		es, _ := st.ListEntries(store.FeedHash(u))
		if err := st.SetRead(es[0].Hash, true); err != nil {
			t.Fatal(err)
		}

		all := do(t, mux, "GET", "/reader/api/0/stream/items/ids?s="+streamReadingList+"&n=10", tok, nil)
		unread := do(t, mux, "GET", "/reader/api/0/stream/items/ids?s="+streamReadingList+"&xt="+url.QueryEscape(stateReadID)+"&n=10", tok, nil)
		var allResp, unreadResp struct {
			ItemRefs []struct {
				ID string `json:"id"`
			} `json:"itemRefs"`
		}
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
		readID := itemLongID(es[0].Hash)
		for _, r := range unreadResp.ItemRefs {
			if r.ID == readID {
				t.Errorf("compat reading-list-xt-unread: read id %q leaked through xt=read", r.ID)
			}
		}
	})

	t.Run("ot-nt-filters/items-ids", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := "https://filters.example/feed"
		op.opml.Feeds = append(op.opml.Feeds, store.Feed{XMLURL: u, Title: "Filters", Tags: []string{"F"}})
		fh := store.FeedHash(u)
		base := time.Unix(1_700_000_000, 0).UTC()
		entries := []store.Entry{
			{GUID: "g0", Link: "https://filters.example/0", Title: "0", Published: base, FetchedAt: base},
			{GUID: "g1", Link: "https://filters.example/1", Title: "1", Published: base.Add(time.Hour), FetchedAt: base.Add(time.Hour)},
			{GUID: "g2", Link: "https://filters.example/2", Title: "2", Published: base.Add(2 * time.Hour), FetchedAt: base.Add(2 * time.Hour)},
		}
		if _, err := st.AppendEntries(fh, entries); err != nil {
			t.Fatal(err)
		}
		path := "/reader/api/0/stream/items/ids?s=" + url.QueryEscape("feed/"+u) +
			"&ot=" + strconv.FormatInt(base.Add(30*time.Minute).Unix(), 10) +
			"&nt=" + strconv.FormatInt(base.Add(90*time.Minute).Unix(), 10) + "&n=10"
		w := do(t, mux, "GET", path, tok, nil)
		var resp struct {
			ItemRefs []struct {
				ID string `json:"id"`
			} `json:"itemRefs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
		}
		got, want := len(resp.ItemRefs), 1
		if got != want {
			t.Errorf("compat ot-nt-filters: got %d refs, want %d item between ot and nt; body=%s", got, want, w.Body.String())
		}
	})

	t.Run("it-filter/selects-starred", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		u := seedFeed(t, op, st, 3, "F")
		es, _ := st.ListEntries(store.FeedHash(u))
		if err := st.SetStarred(es[2].Hash, true); err != nil {
			t.Fatal(err)
		}
		path := "/reader/api/0/stream/items/ids?s=" + url.QueryEscape("feed/"+u) +
			"&it=" + url.QueryEscape(stateStarredID)
		w := do(t, mux, "GET", path, tok, nil)
		var resp struct {
			ItemRefs []struct {
				ID string `json:"id"`
			} `json:"itemRefs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		wantID := itemLongID(es[2].Hash)
		if len(resp.ItemRefs) != 1 || resp.ItemRefs[0].ID != wantID {
			t.Errorf("compat it-filter: refs=%+v, want only %q", resp.ItemRefs, wantID)
		}
	})

	t.Run("continuation-paging/items-ids", func(t *testing.T) {
		srv, mux, tok, op, st := fixture(t)
		srv.MaxPage = 10
		const total = 25
		seedFeed(t, op, st, total, "F")
		seen := map[string]bool{}
		cont := ""
		pages := 0
		for {
			path := "/reader/api/0/stream/items/ids?s=" + streamReadingList + "&n=10"
			if cont != "" {
				path += "&c=" + cont
			}
			w := do(t, mux, "GET", path, tok, nil)
			if w.Code != 200 {
				t.Fatalf("page %d code=%d body=%s", pages, w.Code, w.Body.String())
			}
			var p struct {
				ItemRefs []struct {
					ID string `json:"id"`
				} `json:"itemRefs"`
				Continuation string `json:"continuation"`
			}
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
			// Sanity-check the offset shape so a future change can't
			// silently break it without a contract failure.
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
		if pages != 3 {
			t.Errorf("compat continuation-paging: pages=%d, want 3 for %d/%d", pages, total, 10)
		}
		if len(seen) != total {
			t.Errorf("compat continuation-paging: saw %d distinct ids, want %d", len(seen), total)
		}
	})

	t.Run("unread-count-newest/per-row+global+output-json", func(t *testing.T) {
		_, mux, tok, op, st := fixture(t)
		base := time.Unix(1_700_000_000, 0).UTC()
		urlA, _ := seedFeedWithFetchedAt(t, op, st, "A", []time.Time{
			base, base.Add(1 * time.Hour),
		})
		urlB, _ := seedFeedWithFetchedAt(t, op, st, "B", []time.Time{
			base.Add(10 * time.Hour),
		})
		// output=json is the form Reeder sends; the handler must accept it.
		w := do(t, mux, "GET", "/reader/api/0/unread-count?output=json", tok, nil)
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
		wantA := strconv.FormatInt(base.Add(1*time.Hour).UnixMicro(), 10)
		wantB := strconv.FormatInt(base.Add(10*time.Hour).UnixMicro(), 10)
		if got["feed/"+urlA] != wantA {
			t.Errorf("compat unread-count-newest: feed A=%q, want %q", got["feed/"+urlA], wantA)
		}
		if got["feed/"+urlB] != wantB {
			t.Errorf("compat unread-count-newest: feed B=%q, want %q", got["feed/"+urlB], wantB)
		}
		if got[streamReadingList] != wantB {
			t.Errorf("compat unread-count-newest: reading-list=%q, want %q (global = max per-feed)", got[streamReadingList], wantB)
		}
		if gotCount[streamReadingList] != 3 {
			t.Errorf("compat unread-count-newest: reading-list count=%d, want 3", gotCount[streamReadingList])
		}
	})
}
