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
}

// Open opens (and lazily creates) a data dir, folding state logs.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, state: map[string]EntryState{}, now: time.Now}
	if err := s.foldLog(filepath.Join(dir, "read.log"), 'r'); err != nil {
		return nil, err
	}
	if err := s.foldLog(filepath.Join(dir, "starred.log"), 's'); err != nil {
		return nil, err
	}
	s.recountLive()
	return s, nil
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
	return s.state[hash]
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
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[hash]
	now := s.now().UTC()
	var op byte
	var path string
	if isRead {
		if st.Read == want {
			return nil
		}
		if want {
			op = 'r'
			s.liveR++
		} else {
			op = 'u'
			s.liveR--
		}
		st.Read = want
		s.readN++
		path = filepath.Join(s.Dir, "read.log")
	} else {
		if st.Starred == want {
			return nil
		}
		if want {
			op = 's'
			s.liveS++
		} else {
			op = 'S'
			s.liveS--
		}
		st.Starred = want
		s.starN++
		path = filepath.Join(s.Dir, "starred.log")
	}
	st.UpdatedAt = now
	s.state[hash] = st
	line := fmt.Sprintf("%s %c %s\n", now.Format(time.RFC3339), op, hash)
	if err := appendLine(path, line); err != nil {
		return err
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

// AppendEntries appends entries to feed's current.ndjson. Returns the
// subset that was actually new (de-duplicated by entry hash within the
// feed by scanning current + most recent archive).
func (s *Store) AppendEntries(feedHash string, entries []Entry) ([]Entry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	known, err := s.knownHashes(feedHash)
	if err != nil {
		return nil, err
	}
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
		}
		if known[e.Hash] {
			continue
		}
		known[e.Hash] = true
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
	return added, nil
}

// knownHashes returns the set of entry hashes currently known for a feed
// across current + all archives.
func (s *Store) knownHashes(feedHash string) (map[string]bool, error) {
	set := map[string]bool{}
	dir := filepath.Join(s.Dir, "entries", feedHash)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return set, nil
		}
		return nil, err
	}
	for _, ent := range ents {
		if !strings.HasSuffix(ent.Name(), ".ndjson") {
			continue
		}
		if err := scanEntries(filepath.Join(dir, ent.Name()), func(e Entry) error {
			set[e.Hash] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return set, nil
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
