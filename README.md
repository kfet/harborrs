# harborrs

A small, single-binary self-hosted RSS server with a Google-Reader-
compatible API. Plain-text storage on disk, no SQL, stdlib-mostly Go.

## What it does

- Polls RSS / Atom / JSON feeds (via `gofeed`), conditional GETs with
  `ETag` / `Last-Modified`, exponential backoff on errors, `Retry-After`
  honoured.
- Stores subscriptions in OPML, entries as NDJSON in per-feed
  directories with quarterly archives, read / starred state as append-
  only logs that compact themselves.
- Speaks the **Google Reader API** subset that
  FreshRSS-compatible clients (Reeder Classic, NetNewsWire, Fiery Feeds,
  ReadKit, Unread, lire, Newsify) talk: `ClientLogin`, token, user-info,
  subscription list / edit / quickadd, tag list, stream contents, item
  id queries, edit-tag, mark-all-as-read, unread-count.
- Serves an embedded htmx web UI on the same port — login, home, per-
  feed list, single-entry view, read / star toggles via hx-post.
- Themeable via CSS-variable presets (`light`, `dark`, `sepia`) and
  user overrides at `<data-dir>/overrides/templates/*.html` and
  `<data-dir>/overrides/theme.css`.
- Single static binary; subcommands `serve`, `import`, `poll-once`,
  `hashpass`, `version`.

## Quick start

```sh
# build
go build -o harborrs ./cmd/harborrs

# one-shot bootstrap: creates data dir, writes config.json, prints a
# generated password. Pass -password to set your own, -username to
# change the login name (default "admin").
./harborrs init

# import your existing subscriptions (optional)
./harborrs import subscriptions.opml

# one-shot poll (handy for cron)
./harborrs poll-once

# serve (HTTP API + UI on :8088)
./harborrs serve
```

Then point a FreshRSS-compatible client at `http://your-host:8088/` —
log in with the username (default `admin`) and the password printed by
`init`.

The web UI lives at `/ui/`; visiting `/` redirects there.

If you'd rather hand-roll the config, `harborrs hashpass <password>`
prints a hash you can drop into `<data-dir>/config.json` by hand.

## Storage layout

```
<data-dir>/
  config.json
  subscriptions.opml
  tokens.json
  read.log
  starred.log
  state/<feed-hash>.json
  entries/<feed-hash>/
    current.ndjson
    2024-Q3.ndjson
    2024-Q4.ndjson
  overrides/
    templates/*.html     # user template overrides
    theme.css            # user theme overrides
```

## Design

See [`AGENTS.md`](./AGENTS.md) for the full design brief and
constraints. Highlights:

- Stdlib-mostly. The only third-party dependency is
  `github.com/mmcdole/gofeed` for feed parsing.
- `make all` runs gofmt + vet + staticcheck + race tests with a 100%
  coverage gate (excluding entry-point and e2e via `.covignore`).
- `make e2e` builds the binary and exercises the full surface end-to-
  end against a canned RSS feed.

## Status

v0.1 — minimum viable single-user server. Roadmap: full-text search
(SQLite FTS5 or bleve), more aggressive feed-shape coverage in the
poller, optional multi-user.

## License

MIT — see [LICENSE](./LICENSE).
