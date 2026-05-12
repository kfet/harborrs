// Package store is the on-disk storage layer for harborrs.
//
// Layout under the data dir:
//
//	subscriptions.opml          # feeds + folders (source of truth)
//	state/<feed-hash>.json      # per-feed poll state
//	entries/<feed-hash>/
//	    current.ndjson          # hot file
//	    YYYY-Qn.ndjson          # quarterly archives
//	read.log                    # append-only state log
//	starred.log                 # append-only state log
//
// Hashing: short 20-hex-char (10-byte) sha1 prefix. Collisions are
// astronomically unlikely for single-user feed/entry sets.
package store

import (
	"crypto/sha1"
	"encoding/hex"
)

// FeedHash returns the short hex hash for a feed URL.
func FeedHash(url string) string {
	sum := sha1.Sum([]byte(url))
	return hex.EncodeToString(sum[:])[:20]
}

// EntryHash returns the short hex hash for an entry, derived from the
// (GUID, link) pair. Either may be empty; both empty yields a stable
// hash too, which lets us de-dup degenerate "no identity" entries.
func EntryHash(guid, link string) string {
	h := sha1.New()
	h.Write([]byte(guid))
	h.Write([]byte{0})
	h.Write([]byte(link))
	return hex.EncodeToString(h.Sum(nil))[:20]
}
