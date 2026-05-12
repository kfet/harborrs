# harborrs

A small, single-binary self-hosted RSS server with a Google-Reader-compatible API.

**Status: pre-alpha, under construction.**

## What it is

- Polls RSS/Atom/JSON feeds, stores articles on disk as plain text (OPML + NDJSON).
- Speaks a subset of the **Google Reader API** sufficient for Reeder, NetNewsWire,
  FieryFeeds, ReadKit, Unread, lire, Newsify, and other FreshRSS-compatible clients.
- Serves an embedded htmx-based web UI on the same port. Themeable via CSS/template
  overrides in the config directory.
- Single static binary, SQLite-free, stdlib-mostly.

## Design

See [`AGENTS.md`](./AGENTS.md) for the project brief and constraints.

## License

MIT — see [LICENSE](./LICENSE).
