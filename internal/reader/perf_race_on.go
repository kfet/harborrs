//go:build race

package reader

// raceEnabled is true when the binary was built with -race. See the
// non-race twin for rationale.
const raceEnabled = true
