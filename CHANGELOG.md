# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed

- **Tags replace folders as the feed-organisation primitive.**
  `Feed` now carries a `Tags []string` instead of `Folder string`;
  feeds can be in many tags at once. OPML is now written flat with a
  comma-separated `category` attribute per OPML 2.0; reads merge the
  union of nested-outline parent names and the `category` attribute.
  Pre-existing OPML files load transparently: a single folder becomes
  a single tag, nested `a/b` folders become a single `a/b` tag (no
  splitting). First write rewrites the file in the new layout.
- `subscription/edit` now interprets multiple `a=` / `r=` params as
  add/remove tag operations (previously: overwrite a single folder).
- The "+ add feed" form takes a comma-separated `tags` input instead
  of a single `folder` field.

### Added

- GReader API: `rename-tag` and `disable-tag` endpoints, used by
  Reeder Classic + FreshRSS clients to manage labels.
- Web UI home page now renders a sidebar with pinned `All` /
  `Untagged` rows plus every tag, each with its unread count. The
  feed list filters via `?tag=<name>` (or `?tag=__untagged__`).
- Per-feed view shows clickable tag chips with hx-post add/remove.
- The pseudo-tag name `__untagged__` is reserved (sentinel for the
  no-tags sidebar bucket). User/client attempts to create or rename
  to it are silently dropped at every entry point (UI form, reader
  `subscription/edit`), except `rename-tag dest=` which 400s — that
  endpoint is an explicit user-visible op and deserves feedback.

## [0.3.1] - 2026-05-16

### Changed

- UI now emits only relative URLs in Location headers and templates,
  so harborrs can be served under any URL prefix (e.g.
  `tailscale funnel --set-path=/rss`) without breakage. Every
  rendered `href`/`action`/`hx-*` attribute and every 3xx
  `Location` header is now a relative reference resolved by the
  browser against the effective request URI (RFC 7231 §7.1.2). No
  config knob is needed — the prefix-agnosticism comes from the
  URLs themselves.

### Added

- The "source" link on the entry view opens the article in a new tab
  (`target="_blank" rel="noopener noreferrer"`) so the harborrs UI
  doesn't get clobbered by the article. Marked with a `↗` glyph.
- Pressing <kbd>u</kbd> now prefers `history.back()` when you arrived
  from a same-origin list page — so the "show unread only" pill state
  and scroll position are preserved on the way up. Falls back to the
  canonical parent URL when there's no list-page referrer.
- Keyboard shortcut: <kbd>N</kbd> toggles the "show unread only"
  filter on the home feed list and per-feed entry list (anywhere the
  pill is rendered). No-op on pages without the pill.
- Theme toggle button in the header — small circular button (◐/●/○)
  that cycles **auto → dark → light → auto** on click. Choice is
  persisted in the browser's `localStorage` (key `harborrs.theme`)
  and applied before paint via an inline bootstrap, so there's no
  flash between reloads. The server-side `[ui] theme = …` config
  still acts as the default for users who never touch the toggle.
- Per-feed entry list (`/ui/feed?id=…`) gained the same "show unread
  only" toggle pill as the home feed list. `?unread=1` hides read
  entries; counter shows how many are unread; empty state when nothing
  is unread says "all caught up".
- Keyboard nav: <kbd>u</kbd> now goes "up the hierarchy" universally —
  on the entry view it goes to the parent feed (unchanged), on
  `/ui/feed`, `/ui/all`, `/ui/starred`, `/ui/feed/new`, and `/ui/settings`
  it goes to the home feeds list. Help overlay text updated.
- Entry view auto-marks the entry as read after ~2.5 s of dwell. If
  you navigate away or hit <kbd>m</kbd> first, nothing happens.

### Changed

- Pages restored from the browser's back/forward cache now
  force-reload, so read / star toggles made on an entry are reflected
  on the list when you hit Back — no more F5.

### Fixed

- Auto-mark-read no longer races with a manual "mark unread" click —
  if you click the read/star buttons (or hit <kbd>m</kbd>/<kbd>s</kbd>)
  before the 2.5 s dwell elapses, the timer is cancelled. Previously
  it would silently flip the entry back to read 2.5 s later.
- Auto-mark-read now checks the response status before mutating local
  DOM, so a 4xx/5xx from the server doesn't leave the UI thinking the
  entry is read.
- Theme toggle now sits on the right edge of the header on logged-out
  pages too (was bunched next to the brand).
- Dark mode no longer flashes a white background on overscroll
  (rubber-band scroll). All themes now declare the matching
  `color-scheme` so the browser uses a dark canvas + scrollbars.
- Keyboard-focused row on the home feeds list (`j`/`k`) is now
  visible — previously the `.kb-focus` highlight only had styling for
  entry rows, not feed rows.

## [0.3.0] - 2026-05-14

### Added

- Home page (`/ui/`) gained a "show unread only" filter toggle. With
  `?unread=1` the feed list hides feeds whose unread count is 0, so
  triage-by-feed only walks the feeds that actually have something
  new. Counter on the toggle shows how many feeds qualify. Empty
  state when nothing's unread says "all caught up".
- Each entry row now shows the published timestamp (compact relative:
  `now` / `Nm` / `Nh` / `Nd`, then `Jan 02` for same-year, ISO date for
  older). `title` attribute carries the full local time on hover.
- Web UI: dedicated **add-feed page** at `/ui/feed/new`. GET shows an
  empty form; POST fetches the URL with a bounded HTTP client (15 s
  timeout, 5 MiB read cap) and renders a preview (feed title +
  description + up to 10 recent item titles). User then clicks
  *subscribe* to actually persist the subscription. Home page now
  links to it instead of carrying an inline form.
- New `internal/feedpreview` package wraps gofeed for the preview path
  so the live polling path stays untouched.
- Keyboard help: press <kbd>?</kbd> anywhere to toggle a shortcuts
  cheatsheet. <kbd>j</kbd>/<kbd>k</kbd>/<kbd>gg</kbd>/<kbd>G</kbd>/
  <kbd>Enter</kbd> now work on the **home feeds list** too (previously
  only on entry-list views) — walk down feeds, hit Enter to open. The
  `?` handler uses a capture-phase listener so it isn't shadowed by
  page hotkeys, and the helper ignores keys typed inside any
  `contenteditable` element on top of inputs/textareas/selects.
- New `auto` theme that follows `prefers-color-scheme`. It is now the
  default; existing configs with explicit `light` / `dark` / `sepia`
  continue to work unchanged. Polished default light + Dracula-ish
  dark palette.

### Changed

- `harborrs serve` now prints a clickable `http://host:port/` URL
  instead of the raw `host:port` listen address — terminals (iTerm2,
  Terminal.app, gnome-terminal, VS Code) auto-link it, so Cmd-click
  opens the UI in the browser. Wildcard listens (`:8088`, `0.0.0.0`,
  `[::]`) display as `localhost`.
- "remove feed" moved out of the home feed list (where a stray click
  could unsubscribe by mistake) onto the per-feed page header, next
  to "mark all read", as an explicit `unsubscribe` button with a
  confirm prompt. Cross-feed views (`/ui/all`, `/ui/starred`) don't
  show it — only the single-feed view (`/ui/feed?id=...`).
- "mark all read" on a single-feed view now redirects to the home
  feeds list filtered to unread-only (`/ui/?unread=1`) so you keep
  walking the queue of feeds-with-unread instead of either staring
  at the now-empty feed or context-switching into the flat unread
  list.
- Default theme in `harborrs init`, `config.Default()`, and the UI
  fallback is now `auto`.
- Light stylesheet refresh: sticky header, accent-coloured focus
  ring, pill-shaped unread count, hover-highlighted entry rows, a
  proper card for login + add-feed + settings, blockquote +
  underline styling tweaks for article body.

### Added

- `harborrs passwd [-data DIR] [-password NEW]` — CLI command to change
  the configured password. Reads from `-password`, otherwise prompts on
  stdin. Rewrites only `auth.password_hash` in `config.json`; a running
  `serve` won't pick up the change until restart.
- Web UI: `/ui/settings` with a change-password form (current + new +
  confirm). On success: persists the new hash, revokes every existing
  browser session, clears the current cookie, and bounces to
  `/ui/login?passwd=1` with a friendly notice. Reader API tokens stay
  valid until clients re-authenticate with the new password.
- e2e regression check that `/ui/static/style.css` is non-trivial
  (>4 KB and contains rules for key selectors), so the previous
  stub-CSS regression cannot silently come back.

### Fixed

- Static asset URLs (`style.css`, `htmx.min.js`, `keys.js`,
  `theme.css`) now carry a `?v=<commit>` cache-buster derived from the
  binary's build commit. Combined with a stricter `Cache-Control`
  (`immutable` when the version is pinned, `max-age=60` otherwise),
  a binary upgrade now invalidates the browser cache automatically —
  no more "I upgraded harborrs and the UI still looks unstyled".

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
