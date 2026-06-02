// Package harb is the root package for the Harbour RSS server (the
// binary is named harb).
//
// See AGENTS.md for the project brief. Subpackages will appear here as
// the implementation lands; this file exists to make `go build ./...`
// succeed on a freshly cloned tree.
package harb

// Version is the current build version, sourced from the VERSION file at
// release time. Kept as a `var` (not const) so release builds can
// override it via `-ldflags -X github.com/kfet/harb.Version=...`.
var (
	Version   = "0.1.0"
	Commit    = "unknown" // git short SHA, set via -ldflags at release time
	BuildDate = "unknown" // ISO-8601 UTC, set via -ldflags at release time
)
