//go:build race

package cluster

// The race detector adds its own per-object bookkeeping to the heap, so a
// memory measurement taken under it describes the detector as much as the
// code. The transform's heap saving is measured in a normal run instead.
const raceEnabled = true
