//go:build !race

package reader

// raceEnabled is true when the binary was built with -race. The perf
// guard test uses it to auto-skip under the race detector, which
// inflates wall-clock timings 5–10× and would produce false alarms.
const raceEnabled = false
