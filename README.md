# Harbour RSS

[![CI](https://github.com/kfet/harb/actions/workflows/test.yml/badge.svg)](https://github.com/kfet/harb/actions/workflows/test.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/kfet/harb.svg)](https://pkg.go.dev/github.com/kfet/harb)
[![Go Report Card](https://goreportcard.com/badge/github.com/kfet/harb)](https://goreportcard.com/report/github.com/kfet/harb)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A small, single-binary self-hosted RSS server with a Google-Reader-
compatible API. Plain-text storage on disk, no SQL, stdlib-mostly Go.

> The project is **Harbour RSS** and lives at `github.com/kfet/harb`
> (Go module `github.com/kfet/harb`). The binary, CLI, and config keys
> keep the `harborrs` name.

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
  subscription list / edit / quickadd, tag list, rename-tag, disable-tag,
  stream contents, item id queries, edit-tag, mark-all-as-read,
  unread-count.
- Organises feeds with **tags** (many-to-many, flat). OPML 2.0
  `category` attributes round-trip; pre-existing folder-style OPML
  imports as one tag per folder name.
- Serves an embedded htmx web UI on the same port — login, home, per-
  feed list, single-entry view, read / star toggles via hx-post. The
  home page flags feeds that are failing to sync, with the last error
  and last-successful-sync time.
- Themeable via CSS-variable presets (`light`, `dark`, `sepia`) and
  user overrides at `<data-dir>/overrides/templates/*.html` and
  `<data-dir>/overrides/theme.css`.
- Single static binary; subcommands `serve`, `import`, `poll-once`,
  `hashpass`, `version`.

## Install

**macOS (and Linux with Homebrew):**

```sh
brew tap kfet/tap
brew install kfet/tap/harborrs
```

Updates come via `brew upgrade`.

**Raspberry Pi & other Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/kfet/harb/main/install.sh | sh
```

Drops the binary in `/usr/local/bin` (or `~/.local/bin` if that isn't
writable). Supports `linux/amd64`, `linux/arm64`, `linux/armv6`
(Raspberry Pi 1 / Zero), `darwin/amd64`, `darwin/arm64`. Override the
target version with `VERSION=v0.1.0` or the install prefix with
`PREFIX=$HOME/.local`.

Once installed, `harborrs update` will pull the latest release in
place — except when the binary is owned by a package manager (Homebrew,
apt), in which case it'll tell you to use that instead. `harborrs
update -check` reports without installing.

**From source:**

```sh
go install github.com/kfet/harb/cmd/harborrs@latest
```

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

The web UI lives at `/ui/`; visiting `/` redirects there. The build
version is shown in the UI footer and exposed on the API at
`GET /status` (unauthenticated JSON: `{"product","version","commit","buildDate"}`)
and as `harborrsVersion` on `/reader/api/0/user-info`.

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
