// Package harborrs is the root package for the harborrs RSS server.
//
// See AGENTS.md for the project brief. Subpackages will appear here as
// the implementation lands; this file exists to make `go build ./...`
// succeed on a freshly cloned tree.
package harborrs

// Version is the current build version, sourced from the VERSION file at
// release time. Kept as a constant for runtime reporting; production
// builds may override via -ldflags.
const Version = "0.1.0"
