# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- Initial scaffolding (Makefile, CI, LICENSE, AGENTS.md).
- `internal/atomic`: crash-safe tmp-file-and-rename file writer.
- `internal/store`: on-disk storage layer — OPML parse/write, feed/entry
  hashing, NDJSON entry append with archive rollover, append-only state
  logs (read/starred) with periodic compaction, and per-feed conditional-
  GET state files.
