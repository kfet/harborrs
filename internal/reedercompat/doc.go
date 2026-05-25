// Package reedercompat is a behaviour-only conformance suite for the
// subset of the Google Reader API dialect that Reeder Classic (and
// kindred clients: NetNewsWire, Fiery Feeds, ReadKit, lire, Newsify,
// Unread, FreshRSS-compatible apps) exercises in the wild.
//
// Each sub-test pins one observable contract that has been seen to
// break a real client. Failure messages are written as contract
// violations, not generic diffs, so a regression report tells you
// which client-visible promise broke.
//
// The suite is intentionally decoupled from the rest of harborrs: it
// drives the server through its public HTTP surface plus a small
// Harness the embedder supplies to seed feeds and flip state. The
// long-term intent is to lift this package out into a standalone
// GReader / Reeder conformance kit. The exported API is therefore
// kept narrow on purpose; please don't grow it without considering
// "what would a third-party server using this kit have to implement".
//
// Embedders run the suite like so:
//
//	func TestReederCompat(t *testing.T) {
//	    reedercompat.Run(t, func(t *testing.T) reedercompat.Harness {
//	        // build a fresh server, return a Harness wired to it
//	    })
//	}
package reedercompat
