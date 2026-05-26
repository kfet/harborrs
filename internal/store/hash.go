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
// Hashing: feed hashes are 20-hex-char (10-byte) sha1 prefixes used only
// for local filenames. Entry hashes are 16-hex-char (8-byte) sha1 prefixes:
// that size is the Google Reader / FreshRSS item-id convention and fits in
// the signed-int64 ids used by Reeder and other clients.
package store

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

const (
	FeedHashLen  = 20
	EntryHashLen = 16
)

// FeedHash returns the short hex hash for a feed URL.
func FeedHash(url string) string {
	sum := sha1.Sum([]byte(url))
	return hex.EncodeToString(sum[:])[:FeedHashLen]
}

// EntryHash returns the Reader-compatible short hex hash for an entry,
// derived from the (GUID, link) pair. Either may be empty; both empty yields a
// stable hash too, which lets us de-dup degenerate "no identity" entries.
//
// The high bit of the first byte is masked off so the 16-hex hash always
// fits in a positive int64 when decoded. Google Reader's monotonic
// uint64 item ids never used the top bit, and at least one mature client
// (Reeder) silently drops items whose `longId` exceeds int64 max — manifesting
// as roughly half of items missing from the feed display. Masking the
// high bit costs us 1 bit of hash space (still ~63 bits, no collision
// risk at this scale) and keeps the wire format compatible.
func EntryHash(guid, link string) string {
	h := sha1.New()
	h.Write([]byte(guid))
	h.Write([]byte{0})
	h.Write([]byte(link))
	sum := h.Sum(nil)
	sum[0] &= 0x7F
	return hex.EncodeToString(sum)[:EntryHashLen]
}

// CanonicalEntryHash normalises legacy on-disk entry hashes to the current
// 16-hex-char format. v0.4.4 and earlier stored 20-hex-char sha1 prefixes;
// Google Reader item ids are 16 hex chars, so migration truncates old hashes.
func CanonicalEntryHash(hash string) string {
	if len(hash) >= EntryHashLen && isHex(hash) {
		return strings.ToLower(hash[:EntryHashLen])
	}
	return hash
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case '0' <= r && r <= '9':
		case 'a' <= r && r <= 'f':
		case 'A' <= r && r <= 'F':
		default:
			return false
		}
	}
	return true
}
