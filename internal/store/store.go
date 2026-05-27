package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one feed item, stored as one JSON object per line in NDJSON.
type Entry struct {
	Hash      string    `json:"hash"`
	FeedHash  string    `json:"feed"`
	GUID      string    `json:"guid,omitempty"`
	Link      string    `json:"link,omitempty"`
	Title     string    `json:"title,omitempty"`
	Author    string    `json:"author,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Content   string    `json:"content,omitempty"`
	Published time.Time `json:"published"`
	FetchedAt time.Time `json:"fetched_at"`
}

// FeedState is the per-feed conditional-GET + scheduling state.
type FeedState struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	LastFetched  time.Time `json:"last_fetched,omitempty"`
	NextFetch    time.Time `json:"next_fetch,omitempty"`
	Interval     int64     `json:"interval_s,omitempty"`
	ErrorCount   int       `json:"error_count,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

// EntryState is the read/starred state for a single entry, derived by
// folding the append-only logs on startup.
type EntryState struct {
	Read    bool
	Starred bool
	// UpdatedAt is the time of the most recent fold-relevant event for
	// this entry.
	UpdatedAt time.Time
}

// Store is the in-memory + on-disk single-user store.
type Store struct {
	Dir string

	mu    sync.RWMutex
	state map[string]EntryState // entryHash -> state
	readN int                   // count of "r" lines currently on disk
	starN int                   // count of "s/S" lines currently on disk
	liveR int                   // live read entries (read=true)
	liveS int                   // live starred entries (starred=true)
	now   func() time.Time

	// In-memory entry index. Built at Open and maintained on
	// AppendEntries. Lets Reader handlers skip ReadDir + ndjson reparse
	// per request (the ~200ms-per-call hot path that made Reeder sync
	// slow).
	//
	// idx[feedHash]    → entries sorted by Published descending.
	// byHash[entryHash] → the same Entry (by-value copy).
	//
	// Guarded by mu (shared with state). Index callers take RLock for
	// reads and return defensive copies, so callers that mutate the
	// returned slice (e.g. filtering in place) cannot corrupt the cache.
	//
	// Consistency: AppendEntries writes to disk before taking mu to
	// update the index, so freshly-appended entries become visible on
	// disk a few µs before the index reflects them. Reader handlers
	// never read disk, so from their point of view the new batch
	// arrives atomically when the index lock is released.
	idx    map[string][]Entry
	byHash map[string]Entry

	// Per-feed unread counters. Maintained by AppendEntries (new
	// entries default unread → ++) and setFlag (read=true → --,
	// read=false → ++). Initialised at Open by folding the index
	// against the state map. handleUnreadCount reads these directly so
	// the GReader unread-count endpoint is O(feeds) instead of
	// O(entries).
	unreadN     map[string]int   // feedHash → unread count
	unreadNewUs map[string]int64 // feedHash → newest unread FetchedAt µs since epoch
}

// Open opens (and lazily creates) a data dir, folding state logs.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := migrateEntryHashes(dir); err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, state: map[string]EntryState{}, idx: map[string][]Entry{}, byHash: map[string]Entry{}, unreadN: map[string]int{}, unreadNewUs: map[string]int64{}, now: time.Now}
	if err := s.foldLog(filepath.Join(dir, "read.log"), 'r'); err != nil {
		return nil, err
	}
	if err := s.foldLog(filepath.Join(dir, "starred.log"), 's'); err != nil {
		return nil, err
	}
	s.recountLive()
	if err := s.buildIndex(); err != nil {
		return nil, err
	}
	s.recountUnread()
	return s, nil
}

// recountUnread rebuilds the per-feed unread counters from the
// in-memory index + state map. Called at Open after buildIndex.
func (s *Store) recountUnread() {
	for fh, list := range s.idx {
		var n int
		var newest int64
		for _, e := range list {
			if s.state[e.Hash].Read {
				continue
			}
			n++
			if us := e.FetchedAt.UnixMicro(); us > newest {
				newest = us
			}
		}
		if n > 0 {
			s.unreadN[fh] = n
			s.unreadNewUs[fh] = newest
		}
	}
}

// buildIndex scans every entries/<feedHash>/*.ndjson on disk and
// populates the in-memory index. Called once at Open after migration.
func (s *Store) buildIndex() error {
	root := filepath.Join(s.Dir, "entries")
	feeds, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, fd := range feeds {
		if !fd.IsDir() {
			continue
		}
		fh := fd.Name()
		dir := filepath.Join(root, fh)
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var list []Entry
		feedSeen := make(map[string]struct{})
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".ndjson") {
				continue
			}
			if err := scanEntries(filepath.Join(dir, e.Name()), func(en Entry) error {
				en.Hash = CanonicalEntryHash(en.Hash)
				if en.FeedHash == "" {
					en.FeedHash = fh
				}
				// Safety net: if duplicate hashes ever made it onto
				// disk *within this feed* (e.g. an old buggy version,
				// or a tightly raced AppendEntries in the past), keep
				// only the first occurrence in the index. Scoped
				// per-feed because cross-feed hash collisions are a
				// legitimate shape (shared-content syndication).
				if _, dup := feedSeen[en.Hash]; dup {
					return nil
				}
				feedSeen[en.Hash] = struct{}{}
				list = append(list, en)
				s.byHash[en.Hash] = en
				return nil
			}); err != nil {
				return err
			}
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Published.After(list[j].Published)
		})
		s.idx[fh] = list
	}
	return nil
}

// IndexedEntries returns a snapshot of the in-memory entries for a feed,
// already sorted by Published descending. The returned slice is a copy;
// callers may filter/mutate it freely without affecting the cache.
func (s *Store) IndexedEntries(feedHash string) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.idx[feedHash]
	out := make([]Entry, len(src))
	copy(out, src)
	return out
}

// EntryByHash looks up an indexed entry by hash. Second return is false
// if the hash is unknown.
func (s *Store) EntryByHash(hash string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.byHash[CanonicalEntryHash(hash)]
	return e, ok
}

func (s *Store) recountLive() {
	s.liveR, s.liveS = 0, 0
	for _, st := range s.state {
		if st.Read {
			s.liveR++
		}
		if st.Starred {
			s.liveS++
		}
	}
}

// foldLog reads a state log and applies it. Lines: "<rfc3339> <op> <hash>".
// kind 'r' uses ops {r,u} (read/unread); kind 's' uses ops {s,S} (star/unstar).
func (s *Store) foldLog(path string, kind byte) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, parts[0])
		if err != nil {
			continue
		}
		op, hash := parts[1], parts[2]
		st := s.state[hash]
		st.UpdatedAt = ts
		switch {
		case kind == 'r' && op == "r":
			st.Read = true
			s.readN++
		case kind == 'r' && op == "u":
			st.Read = false
			s.readN++
		case kind == 's' && op == "s":
			st.Starred = true
			s.starN++
		case kind == 's' && op == "S":
			st.Starred = false
			s.starN++
		default:
			continue
		}
		s.state[hash] = st
	}
	return sc.Err()
}

// EntryState returns the current state for an entry hash.
func (s *Store) EntryState(hash string) EntryState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state[CanonicalEntryHash(hash)]
}

// SetRead records a read/unread mutation. Idempotent.
func (s *Store) SetRead(hash string, read bool) error {
	return s.setFlag(hash, read, true)
}

// SetStarred records a starred/unstarred mutation. Idempotent.
func (s *Store) SetStarred(hash string, starred bool) error {
	return s.setFlag(hash, starred, false)
}

func (s *Store) setFlag(hash string, want, isRead bool) error {
	hash = CanonicalEntryHash(hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[hash]
	now := s.now().UTC()
	var op byte
	var path string
	// Compute the mutation but DO NOT apply it to in-memory state until
	// the persist succeeds. Otherwise a persist failure leaves the
	// reader showing a flag set that survives only until restart.
	newLiveR, newLiveS := s.liveR, s.liveS
	if isRead {
		if st.Read == want {
			return nil
		}
		if want {
			op = 'r'
			newLiveR++
		} else {
			op = 'u'
			newLiveR--
		}
		path = filepath.Join(s.Dir, "read.log")
	} else {
		if st.Starred == want {
			return nil
		}
		if want {
			op = 's'
			newLiveS++
		} else {
			op = 'S'
			newLiveS--
		}
		path = filepath.Join(s.Dir, "starred.log")
	}
	line := fmt.Sprintf("%s %c %s\n", now.Format(time.RFC3339), op, hash)
	if err := appendLine(path, line); err != nil {
		return err
	}
	// Persist succeeded — now apply the in-memory mutation.
	st.UpdatedAt = now
	if isRead {
		st.Read = want
		s.liveR = newLiveR
		s.readN++
	} else {
		st.Starred = want
		s.liveS = newLiveS
		s.starN++
	}
	s.state[hash] = st
	// Unread counter maintenance happens after s.state is updated so
	// recomputeNewestUnreadLocked observes the new Read flag.
	if isRead {
		if e, ok := s.byHash[hash]; ok {
			fh := e.FeedHash
			if want {
				if s.unreadN[fh] > 0 {
					s.unreadN[fh]--
					if s.unreadN[fh] == 0 {
						delete(s.unreadN, fh)
						delete(s.unreadNewUs, fh)
					} else if e.FetchedAt.UnixMicro() == s.unreadNewUs[fh] {
						// We just marked the newest-unread entry as
						// read. The remaining max may be lower —
						// rescan. If any other unread entry shared the
						// same UnixMicro, max stays unchanged either
						// way, so this is the only case worth the
						// recompute.
						s.recomputeNewestUnreadLocked(fh)
					}
				}
			} else {
				s.unreadN[fh]++
				if us := e.FetchedAt.UnixMicro(); us > s.unreadNewUs[fh] {
					s.unreadNewUs[fh] = us
				}
			}
		}
	}
	// Compact when log is 10× live set (and at least 32 entries to avoid churn).
	if isRead && s.readN > 32 && s.readN > 10*s.liveR {
		return s.compactLocked(path, 'r')
	}
	if !isRead && s.starN > 32 && s.starN > 10*s.liveS {
		return s.compactLocked(path, 's')
	}
	return nil
}

// compactLocked rewrites a state log from the current in-memory state.
// Caller must hold s.mu.
func (s *Store) compactLocked(path string, kind byte) error {
	hashes := make([]string, 0, len(s.state))
	for h, st := range s.state {
		if kind == 'r' && st.Read {
			hashes = append(hashes, h)
		} else if kind == 's' && st.Starred {
			hashes = append(hashes, h)
		}
	}
	sort.Strings(hashes)
	var b strings.Builder
	op := byte('r')
	if kind == 's' {
		op = 's'
	}
	now := s.now().UTC().Format(time.RFC3339)
	for _, h := range hashes {
		fmt.Fprintf(&b, "%s %c %s\n", now, op, h)
	}
	if err := atomicWriteFile(path, []byte(b.String())); err != nil {
		return err
	}
	if kind == 'r' {
		s.readN = len(hashes)
	} else {
		s.starN = len(hashes)
	}
	return nil
}

// recomputeNewestUnreadLocked rescans the index for feedHash and
// updates unreadNewUs to the newest FetchedAt among unread entries.
// Caller must hold s.mu and must guarantee s.unreadN[fh] > 0 — the
// loop will then find at least one unread entry and update the map.
func (s *Store) recomputeNewestUnreadLocked(fh string) {
	var newest int64
	for _, e := range s.idx[fh] {
		if s.state[e.Hash].Read {
			continue
		}
		if us := e.FetchedAt.UnixMicro(); us > newest {
			newest = us
		}
	}
	s.unreadNewUs[fh] = newest
}

// UnreadCount returns the cached per-feed unread count + newest unread
// FetchedAt µs.
func (s *Store) UnreadCount(feedHash string) (count int, newestUs int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unreadN[feedHash], s.unreadNewUs[feedHash]
}

// AppendEntries appends entries to feed's current.ndjson. Returns the
// subset that was actually new (de-duplicated against the in-memory
// entry index — see comments on Store.idx / Store.byHash).
func (s *Store) AppendEntries(feedHash string, entries []Entry) ([]Entry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	// Snapshot known hashes from the feed's own in-memory index. No
	// disk read. Dedup is per-feed by design: two feeds publishing the
	// same GUID+link (shared-content syndication) each get their own
	// entry — same scope as the pre-refactor knownHashes(feedHash).
	s.mu.RLock()
	feedList := s.idx[feedHash]
	known := make(map[string]struct{}, len(feedList))
	for _, e := range feedList {
		known[e.Hash] = struct{}{}
	}
	s.mu.RUnlock()
	dir := filepath.Join(s.Dir, "entries", feedHash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "current.ndjson")
	f, err := osOpenAppend(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var added []Entry
	for _, e := range entries {
		if e.Hash == "" {
			e.Hash = EntryHash(e.GUID, e.Link)
		} else {
			e.Hash = CanonicalEntryHash(e.Hash)
		}
		if e.FeedHash == "" {
			e.FeedHash = feedHash
		}
		if _, ok := known[e.Hash]; ok {
			continue
		}
		known[e.Hash] = struct{}{}
		line, err := jsonMarshal(e)
		if err != nil {
			return added, err
		}
		line = append(line, '\n')
		if _, err := f.Write(line); err != nil {
			return added, err
		}
		added = append(added, e)
	}
	if len(added) > 0 {
		s.mu.Lock()
		list := s.idx[feedHash]
		// Build a per-feed hash set for the write-locked re-check;
		// concurrent AppendEntries calls for the same feed could race
		// past the RLock snapshot above. Scope is per-feed for the
		// same reason as the snapshot — see comment up top.
		inFeed := make(map[string]struct{}, len(list))
		for _, e := range list {
			inFeed[e.Hash] = struct{}{}
		}
		for _, e := range added {
			if _, dup := inFeed[e.Hash]; dup {
				continue
			}
			inFeed[e.Hash] = struct{}{}
			list = append(list, e)
			s.byHash[e.Hash] = e
			// New entries are unread by default unless the state map
			// already says otherwise (rare: log entries for a hash
			// that pre-dates the entry file, e.g. after migration).
			if !s.state[e.Hash].Read {
				s.unreadN[feedHash]++
				if us := e.FetchedAt.UnixMicro(); us > s.unreadNewUs[feedHash] {
					s.unreadNewUs[feedHash] = us
				}
			}
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Published.After(list[j].Published)
		})
		s.idx[feedHash] = list
		s.mu.Unlock()
	}
	return added, nil
}

// scanEntries calls fn for each JSON object in the file.
func scanEntries(path string, fn func(Entry) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ListEntries returns all entries for a feed (current + all archives),
// most-recently-published first.
func (s *Store) ListEntries(feedHash string) ([]Entry, error) {
	dir := filepath.Join(s.Dir, "entries", feedHash)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, ent := range ents {
		if !strings.HasSuffix(ent.Name(), ".ndjson") {
			continue
		}
		if err := scanEntries(filepath.Join(dir, ent.Name()), func(e Entry) error {
			out = append(out, e)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Published.After(out[j].Published)
	})
	return out, nil
}

// RolloverArchives splits current.ndjson into quarterly archives for entries
// older than the cutoff. The hot file keeps everything newer-than cutoff.
// Returns the number of entries archived.
func (s *Store) RolloverArchives(feedHash string, cutoff time.Time) (int, error) {
	dir := filepath.Join(s.Dir, "entries", feedHash)
	curPath := filepath.Join(dir, "current.ndjson")
	if _, err := os.Stat(curPath); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var keep []Entry
	byQuarter := map[string][]Entry{}
	if err := scanEntries(curPath, func(e Entry) error {
		t := e.Published
		if t.IsZero() {
			t = e.FetchedAt
		}
		if t.Before(cutoff) {
			q := fmt.Sprintf("%04d-Q%d", t.Year(), (int(t.Month())-1)/3+1)
			byQuarter[q] = append(byQuarter[q], e)
		} else {
			keep = append(keep, e)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if len(byQuarter) == 0 {
		return 0, nil
	}
	archived := 0
	for q, items := range byQuarter {
		path := filepath.Join(dir, q+".ndjson")
		f, err := osOpenAppend(path)
		if err != nil {
			return archived, err
		}
		for _, e := range items {
			line, err := jsonMarshal(e)
			if err != nil {
				f.Close()
				return archived, err
			}
			line = append(line, '\n')
			if _, err := f.Write(line); err != nil {
				f.Close()
				return archived, err
			}
			archived++
		}
		if err := f.Close(); err != nil {
			return archived, err
		}
	}
	// Rewrite current atomically.
	var b strings.Builder
	for _, e := range keep {
		line, err := jsonMarshal(e)
		if err != nil {
			return archived, err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := atomicWriteFile(curPath, []byte(b.String())); err != nil {
		return archived, err
	}
	return archived, nil
}

// FeedState helpers --------------------------------------------------------

// LoadFeedState reads a per-feed state file. Missing → zero value, no error.
func (s *Store) LoadFeedState(feedHash string) (FeedState, error) {
	path := filepath.Join(s.Dir, "state", feedHash+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FeedState{}, nil
		}
		return FeedState{}, err
	}
	var fs FeedState
	if err := json.Unmarshal(data, &fs); err != nil {
		return FeedState{}, err
	}
	return fs, nil
}

// SaveFeedState atomically writes the per-feed state.
func (s *Store) SaveFeedState(feedHash string, fs FeedState) error {
	data, err := jsonMarshalIndent(fs, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(s.Dir, "state", feedHash+".json"), data)
}

// AllStates returns a snapshot of in-memory entry states. Mainly for tests.
func (s *Store) AllStates() map[string]EntryState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]EntryState, len(s.state))
	for k, v := range s.state {
		out[k] = v
	}
	return out
}

// CountRead returns the number of live read entries.
func (s *Store) CountRead() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.liveR
}

// CountStarred returns the number of live starred entries.
func (s *Store) CountStarred() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.liveS
}

// appendLine appends a single line to path via O_APPEND. Assumes < PIPE_BUF.
func appendLine(path string, line string) error {
	if len(line) > 4000 {
		return fmt.Errorf("appendLine: line too large: %d", len(line))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := osOpenAppend(path)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
