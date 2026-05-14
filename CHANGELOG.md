# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Fixed

- Web UI was rendering with browser-default styling on every page
  except `/ui/login` because the bundled `style.css` only had rules
  for a handful of selectors. Fleshed out to a real (still
  framework-less, ~7.5 KB) stylesheet covering header/nav, forms,
  buttons, feed list, entry list/detail, the three themes
  (light/dark/sepia), and a small-screen breakpoint. Overrides
  (`<config_dir>/overrides/theme.css`) still load after this file
  and continue to win.

### Changed

- README: `brew tap kfet/harborrs` no longer needs the explicit URL
  (proper `homebrew-harborrs` tap repo now exists); install.sh
  arch list updated to include `linux/armv6`.

## [0.2.1] - 2026-05-14

### Added

- Build matrix now also cross-compiles `linux/armv6` (Raspberry Pi 1 / Zero).
- Release pipeline auto-publishes the Homebrew formula to the
  `kfet/homebrew-harborrs` tap on every tagged release (rendered from
  `packaging/homebrew/harborrs.rb.tmpl` using the just-built tarball
  checksums). Requires a `HOMEBREW_TAP_TOKEN` repo secret.

### Changed

- `install.sh` and `harborrs update` now resolve `armv6l`/`armv7l` and
  `GOARCH=arm` to the new `linux-armv6` release asset (previously
  rejected as unsupported).

## [0.2.0] - 2026-05-13

### Added

- **Distribution & self-update**:
  - Homebrew formula (`Formula/harborrs.rb`) — `brew tap kfet/harborrs
    https://github.com/kfet/harborrs && brew install kfet/harborrs/harborrs`.
  - `install.sh` curl-pipe installer for Linux/macOS (linux/amd64,
    linux/arm64, darwin/amd64, darwin/arm64); verifies sha256 against
    `checksums.txt` from the release.
  - `harborrs update [-check] [-version vX.Y.Z] [-repo owner/name]`
    subcommand: downloads the matching release tarball, verifies
    checksum, and atomically replaces the running binary. Refuses to
    run when the binary lives under a package-manager-owned path
    (Homebrew, linuxbrew, /usr/bin).
  - GitHub Actions release workflow on tag push: builds the four-target
    matrix, ships tarballs + `checksums.txt` as release assets.
  - `make release-local`: build the same artefacts locally under
    `dist/` for testing the installer / updater without cutting a tag.
  - `harborrs version` now reports commit SHA + build date alongside
    the semver, populated via `-ldflags -X` at build time.
- `harborrs init` subcommand: one-shot bootstrap that creates the data
  dir, writes `config.json`, and generates (or accepts) a password.
  Flags: `-data`, `-username`, `-password`, `-listen`, `-theme`,
  `-force`. The top-level usage text and `serve`'s missing-config error
  now point at it.
- `make build` target produces `./harborrs`; wired into `make all` so a
  plain `go build` failure is caught without relying on the e2e harness.
- `make build-matrix` cross-compile check for linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64 (CGO disabled, compile-only). Wired into
  `make all`.
- Web UI: cross-feed views `/ui/all` (unread) and `/ui/starred`.
- Web UI: mark-all-read button on per-feed and `/ui/all` pages.
- Web UI: top nav linking feeds / unread / starred.
- Web UI: read/star toggle buttons on the single-entry view, with htmx
  fragment swap to update the panel in place.
- Web UI: add-feed form and per-feed remove button on the home page.

### Fixed

- Web UI now serves htmx from the embedded asset (`/ui/static/htmx.min.js`)
  instead of an unpkg.com CDN reference. The previous CDN reference
  violated the AGENTS.md requirement that templates and static assets
  ship via `go:embed`, and broke the UI on offline / air-gapped hosts.
- `store.setFlag` no longer mutates in-memory read/starred state when
  the state-log persist fails; the in-memory and on-disk views now
  stay in sync across crashes.

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
