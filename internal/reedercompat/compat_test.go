package reedercompat_test

// TestReederCompat wires the Reeder / GReader conformance suite from
// internal/reedercompat against the harborrs Server implementation.
//
// The contracts live in internal/reedercompat as a deliberately narrow,
// embedder-driven suite so that the harborrs server is exercised the
// same way a future OSS conformance kit would exercise any GReader-
// dialect server.

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/reader"
	"github.com/kfet/harborrs/internal/reedercompat"
	"github.com/kfet/harborrs/internal/store"
)

var testPwHash = mustHashPw()

func mustHashPw() string {
	h, err := auth.HashPassword("p")
	if err != nil {
		panic(err)
	}
	return h
}

// memOPML is a minimal in-memory OPMLProvider for the conformance run.
type memOPML struct {
	opml store.OPML
}

func (m *memOPML) Load() (*store.OPML, error) {
	cp := m.opml
	cp.Feeds = append([]store.Feed{}, m.opml.Feeds...)
	return &cp, nil
}
func (m *memOPML) Save(o *store.OPML) error {
	m.opml = *o
	m.opml.Feeds = append([]store.Feed{}, o.Feeds...)
	return nil
}

func TestReederCompat(t *testing.T) {
	reedercompat.Run(t, func(t *testing.T) reedercompat.Harness {
		dir := t.TempDir()
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		cfg := auth.Config{Username: "u", PasswordHash: testPwHash}
		as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), cfg)
		tok, _ := as.IssueAPIToken("u", "p")
		op := &memOPML{}
		s := reader.New(st, as, op)
		// Crank MaxPage down so the continuation-paging contract is
		// exercised on a small seed without needing 1000 entries.
		s.MaxPage = 10
		handler := s.Routes(http.NewServeMux())

		seedAt := func(t *testing.T, name, tag string, fetched []time.Time) (string, []string) {
			t.Helper()
			u := "https://feed.example/" + name
			op.opml.Feeds = append(op.opml.Feeds, store.Feed{
				XMLURL: u, Title: name, Tags: []string{tag},
				HTMLURL: "https://feed.example",
			})
			fh := store.FeedHash(u)
			es := make([]store.Entry, len(fetched))
			for i, ft := range fetched {
				es[i] = store.Entry{
					GUID:      name + "-g" + strconv.Itoa(i),
					Link:      "https://feed.example/" + name + "/" + strconv.Itoa(i),
					Title:     name + " " + strconv.Itoa(i),
					Content:   "c",
					Summary:   "s",
					Published: ft,
					FetchedAt: ft,
				}
			}
			if _, err := st.AppendEntries(fh, es); err != nil {
				t.Fatal(err)
			}
			got, err := st.ListEntries(fh)
			if err != nil {
				t.Fatal(err)
			}
			hashes := make([]string, len(got))
			// Re-list returns entries sorted (impl-specific); map back
			// to seed order via GUID.
			byGUID := map[string]string{}
			for _, e := range got {
				byGUID[e.GUID] = e.Hash
			}
			for i := range es {
				hashes[i] = byGUID[es[i].GUID]
			}
			return u, hashes
		}
		seed := func(t *testing.T, name, tag string, count int) (string, []string) {
			t.Helper()
			now := time.Now().UTC()
			fetched := make([]time.Time, count)
			for i := range fetched {
				fetched[i] = now
			}
			return seedAt(t, name, tag, fetched)
		}

		return reedercompat.Harness{
			Handler:    handler,
			Token:      tok,
			SeedFeed:   seed,
			SeedFeedAt: seedAt,
			SetRead: func(t *testing.T, hash string, v bool) time.Time {
				t.Helper()
				if err := st.SetRead(hash, v); err != nil {
					t.Fatal(err)
				}
				return st.EntryState(hash).UpdatedAt
			},
			SetStarred: func(t *testing.T, hash string, v bool) time.Time {
				t.Helper()
				if err := st.SetStarred(hash, v); err != nil {
					t.Fatal(err)
				}
				return st.EntryState(hash).UpdatedAt
			},
		}
	})
}
