# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [0.1.0] - 2026-05-12

### Added

- Initial scaffolding (Makefile, CI, LICENSE, AGENTS.md).
- `internal/atomic`: crash-safe tmp-file-and-rename file writer.
- `internal/store`: on-disk storage layer — OPML parse/write, feed/entry
  hashing, NDJSON entry append with archive rollover, append-only state
  logs (read/starred) with periodic compaction, and per-feed conditional-
  GET state files.
- `internal/poll`: feed poller — gofeed-based parsing, conditional GETs
  (ETag / Last-Modified), 304 handling, exponential backoff on errors,
  `Retry-After` honouring on 429/503, and a simple `Run` loop ticking
  feeds whose `NextFetch` has passed.
- `internal/auth`: single-user credential (stdlib SHA-256 salt-and-stretch),
  opaque API + cookie-session token store persisted to `tokens.json`,
  ClientLogin-style token extraction helpers.
- `internal/reader`: Google Reader API subset — ClientLogin, token,
  user-info, subscription list/edit/quickadd, tag list, stream contents
  (per-feed/label/starred/read/reading-list), item id queries, item
  contents lookup, edit-tag (read/starred), mark-all-as-read with `ts`
  cutoff, unread-count.
- `internal/ui`: htmx + html/template web UI — cookie-session login,
  home (feed list with unread counts), per-feed entry list, single-entry
  view, read/star toggles via hx-post returning fragment rows. Theme via
  `[ui] theme` config (light/dark/sepia) and optional overrides at
  `<config>/overrides/templates/*.html` and `<config>/overrides/theme.css`.
- `internal/config`: JSON config + FileOPML provider.
- `cmd/harborrs`: subcommands `serve`, `import`, `poll-once`, `hashpass`,
  `version`.
- `make e2e`: end-to-end smoke that builds the binary, polls a canned
  RSS feed, exercises ClientLogin → subscription/list →
  stream/contents → edit-tag → unread-count plus a UI login + home.
