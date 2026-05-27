// Package subs holds the in-memory, atomic-pointer-backed
// subscriptions.opml for harborrs.
//
// State is loaded once at Open(); after that, readers call OPML() for
// a lock-free pointer to an immutable *store.OPML, and mutators call
// Mutate() which serialises writers via a mutex, clones the live
// pointer, applies the closure, persists to disk, and stores the new
// pointer. The pointer returned by OPML() MUST be treated as
// immutable — same contract as any atomic.Pointer payload.
//
// See AGENTS.md → "Concurrency model" for the full design.
package subs

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"

	"github.com/kfet/harborrs/internal/store"
)

// Subs is the in-memory + on-disk subscription list.
type Subs struct {
	path string
	op   atomic.Pointer[store.OPML]
	wmu  sync.Mutex // serialises Mutate calls

	// writeFn is the disk-write primitive. Production code uses
	// store.OPML.WriteOPML; tests can swap it via SetWriteHook to
	// exercise Mutate error paths without filesystem trickery.
	writeFn func(o *store.OPML, path string) error

	// inPlace, when true, makes Mutate apply the closure to the
	// current atomic pointer's struct in-place instead of
	// clone+swap. Set only by NewForTest; production Open() leaves
	// this false. The contract that callers must treat OPML() as
	// immutable is unchanged — this flag only changes the writer
	// behaviour, not the reader.
	inPlace bool
}

// Open reads + parses the OPML file once. Missing → empty OPML, no
// error. The returned *Subs holds the parsed pointer in memory; disk
// is touched again only inside Mutate.
func Open(path string) (*Subs, error) {
	s := &Subs{path: path, writeFn: func(o *store.OPML, p string) error { return o.WriteOPML(p) }}
	o, err := store.ReadOPML(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		o = &store.OPML{}
	}
	s.op.Store(o)
	return s, nil
}

// OPML returns the live pointer. Callers MUST treat the result as
// immutable; mutation goes via Mutate.
func (s *Subs) OPML() *store.OPML {
	return s.op.Load()
}

// Mutate applies fn to a clone of the current OPML, atomically
// persists the clone to disk, and replaces the live pointer. Mutators
// are serialised — concurrent Mutate calls are safe but linearised in
// arrival order.
//
// If fn or the disk write returns an error, the live pointer is
// untouched.
func (s *Subs) Mutate(fn func(*store.OPML)) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	live := s.op.Load()
	clone := live.Clone()
	fn(clone)
	if err := s.writeFn(clone, s.path); err != nil {
		return err
	}
	if s.inPlace {
		// Test-only: copy the clone's contents back into the live
		// struct so tests holding the initial pointer (NewForTest
		// stores it directly into the atomic.Pointer) keep seeing
		// post-Mutate state via that pointer. Crucially we still
		// clone+fn first so a writeFn error leaves the live pointer
		// untouched — same contract as the production path.
		*live = *clone
		return nil
	}
	s.op.Store(clone)
	return nil
}

// NewForTest constructs a Subs that wraps an externally-managed
// *store.OPML pointer with no filesystem backing.
//
// CRITICAL test-fixture contract: the supplied pointer is stored
// directly into the atomic.Pointer. Tests typically keep an alias to
// the same pointer (e.g. `m.opml = o`); Mutate runs clone+fn+writeFn
// and then struct-copies the clone's contents back into the live
// pointer (see Mutate). That keeps the test's alias valid across
// handler-triggered Mutate calls. Do NOT replace this with a
// constructor that clones `initial` — every test that mutates
// `op.opml.Feeds = …` and expects the server to see it will silently
// stop working.
//
// Intended for unit tests only; production code uses Open.
func NewForTest(initial *store.OPML) *Subs {
	if initial == nil {
		initial = &store.OPML{}
	}
	s := &Subs{
		writeFn: func(*store.OPML, string) error { return nil },
		inPlace: true,
	}
	s.op.Store(initial)
	return s
}

// SetWriteHook replaces the disk-write function (production default:
// store.OPML.WriteOPML). Returns a restore closure. Intended for
// tests that need to exercise Mutate write-error paths.
func (s *Subs) SetWriteHook(fn func(*store.OPML, string) error) func() {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	prev := s.writeFn
	s.writeFn = fn
	return func() {
		s.wmu.Lock()
		defer s.wmu.Unlock()
		s.writeFn = prev
	}
}
