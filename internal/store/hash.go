// Package store is the on-disk storage layer for harb.
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
	"regexp"
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
	h.Write([]byte(NormalizeGUID(guid)))
	h.Write([]byte{0})
	h.Write([]byte(link))
	sum := h.Sum(nil)
	sum[0] &= 0x7F
	return hex.EncodeToString(sum)[:EntryHashLen]
}

// trailingRFC1123 matches a single RFC 1123 date-time anchored at the end
// of a string, e.g. " Mon, 18 May 2026 21:12:26 EDT" or
// " Tue, 9 Jun 2026 13:05:18 +0000". The day-of-month may be 1 or 2 digits
// and the zone is a short alpha abbreviation or a numeric offset.
var trailingRFC1123 = regexp.MustCompile(
	` [A-Z][a-z]{2}, \d{1,2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} (?:[A-Za-z]{2,5}|[+-]\d{4})$`)

// NormalizeGUID strips a single trailing RFC 1123 date-time from a feed's
// item guid. Some feeds (e.g. Nintendo World Report) emit a non-permalink
// guid of the form "<stable-path> <pubDate>"; the pubDate's seconds drift
// between polls (…:26 → …:00), changing the guid and therefore the entry
// hash, so the same article is stored twice. Removing the volatile date
// tail yields a stable identity. Only an exact, fully-anchored RFC 1123
// tail is stripped — a guid that merely ends in digits is left untouched,
// so genuinely-distinct items are not collapsed.
func NormalizeGUID(guid string) string {
	return trailingRFC1123.ReplaceAllString(guid, "")
}

// CanonicalEntryHash normalises legacy on-disk entry hashes to the current
// 16-hex-char format. v0.4.4 and earlier stored 20-hex-char sha1 prefixes;
// Google Reader item ids are 16 hex chars, so migration truncates old hashes.
// It is length/case-only and does NOT touch the bits — the Reader API item-id
// round-trip relies on that identity.
func CanonicalEntryHash(hash string) string {
	if len(hash) >= EntryHashLen && isHex(hash) {
		return strings.ToLower(hash[:EntryHashLen])
	}
	return hash
}

// StoreEntryHash is the canonical *storage identity* of an entry hash:
// CanonicalEntryHash plus the high-bit mask that EntryHash applies
// (sum[0] &= 0x7F). The mask was added after some entries had already
// been persisted with the top bit set; on the next poll EntryHash
// produced the masked form, which no longer matched the stored unmasked
// hash, so the same article was stored — and displayed — twice. Masking
// here collapses a legacy unmasked hash and its masked re-poll to one
// id. The high bit lives in the first hex nibble, so clearing 0x8 off
// the leading hex digit is equivalent to sum[0] &= 0x7F.
//
// This is used only for on-disk/in-memory dedup, state-log keys and
// lookups — NOT for Reader item-id encoding, which keeps using
// CanonicalEntryHash so already-issued ids stay stable.
func StoreEntryHash(hash string) string {
	h := CanonicalEntryHash(hash)
	if len(h) != EntryHashLen || !isHex(h) {
		return h
	}
	b := []byte(h)
	switch c := b[0]; {
	case '8' <= c && c <= '9':
		b[0] = c - 8 // '8'..'9' -> '0'..'1'
	case 'a' <= c && c <= 'f':
		b[0] = c - 47 // 'a'..'f' (10..15) masked to 2..7 -> '2'..'7'
	}
	return string(b)
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
