# ASSESSMENT — `work/inmem-opml-concurrent-poll` rebase onto current main

**Date:** 2026-06-08
**Branch:** `work/inmem-opml-concurrent-poll` (commits `2d3c240`, `5fa4a74`, authored 2026-05-26)
**Base:** `a3806a1` (≈ v0.4.12). Current `origin/main`: `4c87aa9` (v0.7.11) — **68 commits ahead**.

## Verdict: **SUPERSEDED — do not merge, do not release.**

The rebase was **not started** (aborted in the investigation phase; working tree clean,
HEAD still `5fa4a74`). No commits, merge, tag, or release were produced. The origin branch
is preserved as a historical record.

The feature's two headline goals — *in-memory authoritative OPML* and *non-serialised
polling* — were both solved independently on main while this branch sat unmerged, using
different mechanisms that are already shipped, tested, and behind the 100% coverage gate.
Landing this branch would mean ripping out working, proven main code (the `OPMLProvider`
interface + `config.FileOPML`, the pull-driven `Refresher`) and replacing it with a
competing redesign — a large, high-risk rebase (13 files, 51 conflict hunks across
`reader.go`, `ui.go`, `poll.go`, `config.go`, `store.go`, reedercompat, and their tests)
for a modest, mostly-already-captured benefit. Not worth it.

Notably, the **original author-agent itself halted on 2026-05-27** for this same reason
(main already 15 commits ahead with overlapping v0.4.14 + v0.4.18 work).

## What the branch proposed vs. what main already does

| Feature on branch | State on current main | Verdict |
|---|---|---|
| In-mem authoritative OPML, serialised mutations | `config.FileOPML` (`cur *store.OPML` + `sync.Mutex`); `Update(fn)` = serialised clone→mutate→atomic-write→store; `Load()` returns a deep copy. Shipped `e5e99ef` (serialise mutations), `dcef20c`/`c2cb0c4` (in-mem authoritative, ~100× Load). | **Done differently.** Branch's `subs.Subs` + `atomic.Pointer` is a *competing* design, now redundant. |
| Reader/UI grab OPML once per request, thread down | Handlers call `OPMLProvider.Load()` per request (deep copy). | **Effectively equivalent**; branch's lock-free zero-copy `Snapshot()` is a micro-refinement only. |
| Non-serialised polling (bounded pool, default 8) | Pull-driven single-flight `Refresher` (`29d08d6`) replaced the adaptive scheduler. `tickOnce`/`Run` (which the branch modified) **no longer exist**. | **Superseded mechanism.** See "still-missing" below. |
| Fresh `gofeed` parser per poll | Refresher cycle is sequential → shared `p.Parser` is safe; no fresh-parser need. | Moot under main's design. |

## Genuinely-unique improvements still NOT on main

Being precise (the branch is superseded *as a whole*, but a few specific optimisations are
real and not yet present on main). These are candidates for **small, separate PRs against
main's current architecture** — not reasons to land this branch:

1. **Concurrent poll fan-out.** Main's `Refresher.cycle` iterates feeds **sequentially**
   (deliberately — its comment: avoid fanning out to N goroutines on a big OPML). A
   *bounded* worker pool (e.g. 8) would parallelise a refresh cycle without that risk. Small,
   self-contained change to `refresher.go`; would also need a fresh `gofeed.Parser` per
   concurrent poll (parser is not goroutine-safe).
2. **In-memory dedup in `Store.AppendEntries`.** Main built the in-memory `idx[feedHash]`
   index but `AppendEntries` **still disk-scans via `knownHashes` on every poll** for dedup.
   Deduping against `idx` instead removes that disk read from the hot path. Caveat: main
   *exports* `KnownHashes` for poll-stage aggregator/Webflow enrichment (`enrich.go`), so the
   helper must stay — only the AppendEntries call site changes.
3. **O(feeds) unread-count counters.** Main's `unread-count` is O(feeds×entries) in memory,
   but gated by an ETag / `If-None-Match` 304 short-circuit (`236666e`), so the scan only
   runs when state actually changed. Per-feed `unreadN`/`unreadNewUs` counters would make the
   miss-path O(feeds). **Lowest marginal value, highest maintenance/correctness risk** of the
   three (the counters duplicate state derivable from `idx`+`state` and must be kept
   consistent across AppendEntries/setFlag/Open). Likely not worth it given the 304 gate.

If any of these is pursued, do it as a focused patch on main — do **not** resurrect the
`subs.Subs` / `OPMLProvider`-removal surgery to carry them.

## Also landed on main since the branch base (context for "why rebase is unattractive")

SSRF guards on outbound fetches, ETag/INM on subscription/list + tag/list + unread-count,
data-driven per-feed resolver chain + poll observability, full-text enrichment for link-only
aggregator feeds, Webflow-to-feed resolver, passkey/WebAuthn login, project rename
(`harborrs` → `harb`, module `github.com/kfet/harb`), desktop master-detail UI, touch swipe
gestures, feed rename, and the release-guard CI. The branch predates all of it.

## Build / test / coverage status

Not run — by design. The branch was never rebased onto main, so there is nothing new to
build or cover. Main itself is green at v0.7.11 behind its 100% gate.

## Recommendation

1. **Do not merge or release this branch.** Leave `origin/work/inmem-opml-concurrent-poll`
   pushed as a historical record of the design exploration.
2. Optionally open small, independent PRs for items (1) and (2) above against main's current
   architecture if the perf wins are wanted. Treat item (3) as probably-not-worth-it.
3. The operator may remove this worktree.
